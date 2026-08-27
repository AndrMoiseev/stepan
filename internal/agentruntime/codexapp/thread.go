package codexapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

var ErrTurnInProgress = agentruntime.ErrTurnInProgress

type Thread struct {
	ID         string
	connection *Connection
	cwd        string
}

type TurnOptions = agentruntime.TurnOptions
type TurnPolicy = agentruntime.TurnPolicy

func ReadOnlyTurnPolicy() TurnPolicy { return agentruntime.ReadOnlyTurnPolicy() }

func SingleWriteRootTurnPolicy(root string) (TurnPolicy, error) {
	root, err := canonicalPath(root)
	if err != nil {
		return TurnPolicy{}, fmt.Errorf("canonicalize writable root: %w", err)
	}
	return agentruntime.SingleWriteRootTurnPolicy(root)
}

type turnRun struct {
	mu        sync.Mutex
	threadID  string
	turnID    string
	events    []Message
	wake      chan struct{}
	terminal  bool
	approvals *ApprovalEvaluator
	pending   map[string]bool
	done      chan struct{}
}

type terminalItem struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	Phase *string `json:"phase"`
	Text  string  `json:"text"`
}

type terminalTurnError struct {
	status  string
	message string
	details string
}

func (err *terminalTurnError) Error() string {
	if err.message == "" {
		return fmt.Sprintf("turn terminal status is %q", err.status)
	}
	if err.details == "" {
		return fmt.Sprintf("turn %s: %s", err.status, err.message)
	}
	return fmt.Sprintf("turn %s: %s; details: %s", err.status, err.message, err.details)
}

func (connection *Connection) StartThread(cwd string) (*Thread, error) {
	cwd, err := canonicalPath(cwd)
	if err != nil {
		return nil, fmt.Errorf("canonicalize Git root: %w", err)
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := connection.Call("thread/start", map[string]string{"cwd": cwd}, &response); err != nil {
		return nil, err
	}
	if response.Thread.ID == "" {
		err := errors.New("thread/start response has no thread.id")
		connection.fail(err)
		return nil, connection.Err()
	}
	return &Thread{ID: response.Thread.ID, connection: connection, cwd: cwd}, nil
}

func (connection *Connection) RunTurn(thread *Thread, prompt string, options TurnOptions) (json.RawMessage, error) {
	options = options.Clone()
	if thread == nil || thread.connection != connection || thread.ID == "" {
		return nil, errors.New("invalid thread handle")
	}
	if prompt == "" {
		return nil, errors.New("turn prompt is required")
	}
	var schema map[string]json.RawMessage
	if len(options.OutputSchema) == 0 || json.Unmarshal(options.OutputSchema, &schema) != nil || schema == nil {
		return nil, errors.New("output schema must be a JSON object")
	}
	if !connection.turnMu.TryLock() {
		return nil, ErrTurnInProgress
	}
	defer connection.turnMu.Unlock()

	approvals, err := NewApprovalEvaluator(thread.cwd, AccessPolicy{WritableRoots: writableRoots(options.Policy)})
	if err != nil {
		return nil, err
	}
	run := &turnRun{
		threadID: thread.ID, approvals: approvals,
		pending: make(map[string]bool), wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	connection.mu.Lock()
	if connection.err != nil {
		err := connection.err
		connection.mu.Unlock()
		return nil, err
	}
	connection.turn = run
	connection.mu.Unlock()
	defer func() {
		close(run.done)
		connection.mu.Lock()
		if connection.turn == run {
			connection.turn = nil
		}
		connection.mu.Unlock()
	}()

	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	params := map[string]any{
		"threadId":       thread.ID,
		"input":          []map[string]string{{"type": "text", "text": prompt}},
		"cwd":            thread.cwd,
		"approvalPolicy": "on-request",
		"sandboxPolicy":  map[string]any{"type": "readOnly", "networkAccess": false},
		"outputSchema":   json.RawMessage(options.OutputSchema),
	}
	if err := connection.Call("turn/start", params, &started); err != nil {
		return nil, err
	}
	if started.Turn.ID == "" {
		return nil, connection.failTurn(errors.New("turn/start response has no turn.id"))
	}
	if err := run.setTurnID(started.Turn.ID); err != nil {
		return nil, connection.failTurn(err)
	}

	completed := make(map[string]terminalItem)
	for {
		message, err := run.next(connection.done)
		if err != nil {
			return nil, connection.Err()
		}
		switch message.Method {
		case "item/completed":
			item, err := decodeCompletedItem(message.Params)
			if err != nil {
				return nil, connection.failTurn(err)
			}
			if _, exists := completed[item.ID]; exists {
				return nil, connection.failTurn(fmt.Errorf("duplicate item/completed for %q", item.ID))
			}
			completed[item.ID] = item
		case "turn/completed":
			if run.hasPending() {
				return nil, connection.failTurn(errors.New("terminal notification arrived with unresolved approval"))
			}
			output, err := decodeCompletedTurn(message.Params, completed)
			if err != nil {
				var terminalErr *terminalTurnError
				if errors.As(err, &terminalErr) {
					return nil, err
				}
				return nil, connection.failTurn(err)
			}
			return output, nil
		}
	}
}

func (connection *Connection) activeTurn() (threadID, turnID string, done <-chan struct{}, active bool) {
	connection.mu.Lock()
	run := connection.turn
	connection.mu.Unlock()
	if run == nil {
		return "", "", nil, false
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.threadID, run.turnID, run.done, true
}

func (connection *Connection) failTurn(err error) error {
	connection.fail(err)
	return connection.Err()
}

func (connection *Connection) dispatchTurnMessage(message Message) error {
	if !strings.HasPrefix(message.Method, "turn/") && !strings.HasPrefix(message.Method, "item/") {
		return nil
	}
	connection.mu.Lock()
	run := connection.turn
	connection.mu.Unlock()
	if run == nil {
		return fmt.Errorf("%s arrived without an active turn", message.Method)
	}
	if err := run.correlate(message); err != nil {
		return err
	}
	if err := run.observeFileChanges(message); err != nil {
		return err
	}
	return run.push(message)
}

func (connection *Connection) validateTurnMessage(message Message) error {
	connection.mu.Lock()
	run := connection.turn
	connection.mu.Unlock()
	if run == nil {
		return fmt.Errorf("%s arrived without an active turn", message.Method)
	}
	return run.correlate(message)
}

func (connection *Connection) registerTurnApproval(message Message) (func() error, error) {
	connection.mu.Lock()
	run := connection.turn
	connection.mu.Unlock()
	if run == nil {
		return nil, errors.New("approval arrived without an active turn")
	}
	request, err := run.approvals.Decode(message)
	if err != nil {
		return nil, err
	}
	if err := run.addPending(message.ID.Key()); err != nil {
		return nil, err
	}
	return func() error {
		decision := run.evaluateApproval(request)
		if err := connection.Respond(message.ID, request.Response(decision)); err != nil {
			return err
		}
		return run.resolvePending(message.ID.Key())
	}, nil
}

func (run *turnRun) evaluateApproval(request ApprovalRequest) ApprovalDecision {
	if request.Kind != FileChangeApproval {
		return DecisionDecline
	}
	return run.approvals.Evaluate(request)
}

func writableRoots(policy TurnPolicy) []string {
	root, ok := policy.WritableRoot()
	if !ok {
		return nil
	}
	return []string{root}
}

func (run *turnRun) observeFileChanges(message Message) error {
	return run.approvals.Observe(message, run.threadID, run.turnID)
}

func (run *turnRun) addPending(key string) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.terminal {
		return errors.New("approval arrived after turn/completed")
	}
	if run.pending[key] {
		return ErrDuplicateRequestID
	}
	run.pending[key] = true
	return nil
}

func (run *turnRun) resolvePending(key string) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if !run.pending[key] {
		return ErrDuplicateResponse
	}
	delete(run.pending, key)
	return nil
}

func (run *turnRun) hasPending() bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	return len(run.pending) != 0
}

func (run *turnRun) clearPending() {
	run.mu.Lock()
	clear(run.pending)
	run.mu.Unlock()
}

func (run *turnRun) setTurnID(id string) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.turnID != "" && run.turnID != id {
		return fmt.Errorf("turn/start returned %q after event for %q", id, run.turnID)
	}
	run.turnID = id
	return nil
}

func (run *turnRun) correlate(message Message) error {
	var ids struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(message.Params, &ids); err != nil {
		return fmt.Errorf("invalid %s params: %w", message.Method, err)
	}
	if ids.TurnID == "" {
		ids.TurnID = ids.Turn.ID
	}
	if ids.ItemID == "" {
		ids.ItemID = ids.Item.ID
	}
	if ids.ThreadID == "" || ids.TurnID == "" || strings.HasPrefix(message.Method, "item/") && ids.ItemID == "" {
		return fmt.Errorf("%s has incomplete correlation IDs", message.Method)
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.terminal {
		return fmt.Errorf("%s arrived after turn/completed", message.Method)
	}
	if ids.ThreadID != run.threadID {
		return fmt.Errorf("%s thread ID does not match active turn", message.Method)
	}
	if run.turnID == "" {
		run.turnID = ids.TurnID
	} else if ids.TurnID != run.turnID {
		return fmt.Errorf("%s turn ID does not match active turn", message.Method)
	}
	return nil
}

func (run *turnRun) push(message Message) error {
	run.mu.Lock()
	if run.terminal {
		run.mu.Unlock()
		return fmt.Errorf("%s arrived after turn/completed", message.Method)
	}
	if message.Method == "turn/completed" {
		run.terminal = true
	}
	run.events = append(run.events, message)
	run.mu.Unlock()
	select {
	case run.wake <- struct{}{}:
	default:
	}
	return nil
}

func (run *turnRun) next(done <-chan struct{}) (Message, error) {
	for {
		run.mu.Lock()
		if len(run.events) != 0 {
			message := run.events[0]
			run.events = run.events[1:]
			run.mu.Unlock()
			return message, nil
		}
		run.mu.Unlock()
		select {
		case <-run.wake:
		case <-done:
			return Message{}, ErrConnectionClosed
		}
	}
}

func decodeCompletedItem(raw json.RawMessage) (terminalItem, error) {
	var params struct {
		Item terminalItem `json:"item"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.Item.ID == "" || params.Item.Type == "" {
		return terminalItem{}, errors.New("invalid item/completed notification")
	}
	return params.Item, nil
}

func decodeCompletedTurn(raw json.RawMessage, completed map[string]terminalItem) (json.RawMessage, error) {
	var params struct {
		Turn struct {
			Status string            `json:"status"`
			Items  []json.RawMessage `json:"items"`
			Error  *struct {
				Message           string `json:"message"`
				AdditionalDetails string `json:"additionalDetails"`
			} `json:"error"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, errors.New("invalid turn/completed notification")
	}
	if params.Turn.Status != "completed" {
		terminalErr := &terminalTurnError{status: params.Turn.Status}
		if params.Turn.Error != nil {
			terminalErr.message = strings.TrimSpace(params.Turn.Error.Message)
			terminalErr.details = strings.TrimSpace(params.Turn.Error.AdditionalDetails)
		}
		return nil, terminalErr
	}
	var finals, unknownPhase []terminalItem
	for _, rawItem := range params.Turn.Items {
		var item terminalItem
		if err := json.Unmarshal(rawItem, &item); err != nil || item.ID == "" || item.Type == "" {
			return nil, errors.New("invalid item in turn/completed notification")
		}
		seen, exists := completed[item.ID]
		if exists && seen.Type != item.Type {
			return nil, fmt.Errorf("terminal item %q does not match item/completed", item.ID)
		}
		if item.Type != "agentMessage" {
			continue
		}
		if item.Phase != nil && *item.Phase == "final_answer" {
			finals = append(finals, item)
		} else if item.Phase == nil {
			unknownPhase = append(unknownPhase, item)
		}
	}
	if len(finals) == 0 && len(unknownPhase) == 1 {
		finals = unknownPhase
	}
	if len(finals) != 1 {
		return nil, fmt.Errorf("terminal turn has %d final agent messages", len(finals))
	}
	seen, exists := completed[finals[0].ID]
	if exists && (seen.Text != finals[0].Text || !samePhase(seen.Phase, finals[0].Phase)) {
		return nil, errors.New("final agent message contradicts item/completed")
	}
	return decodeStructuredObject(finals[0].Text)
}

func decodeStructuredObject(text string) (json.RawMessage, error) {
	data := bytes.TrimSpace([]byte(text))
	if len(data) == 0 || data[0] != '{' {
		return nil, errors.New("structured final output must be a JSON object")
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("structured final output must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("structured final output has trailing data")
	}
	return append(json.RawMessage(nil), data...), nil
}

func samePhase(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
