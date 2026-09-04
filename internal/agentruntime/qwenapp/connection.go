package qwenapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

type connectionOwner interface {
	Close() error
}

type connectionHandler struct {
	sessionUpdate func(message) error
	permission    func(*Connection, message) error
}

type pendingCall struct {
	method   string
	response chan message
	resume   chan struct{}
}

// inboundCall tracks responses to agent-initiated permission and delegated
// filesystem requests. These calls share one serialized response registry.
type inboundCall struct {
	id         requestID
	method     string
	sessionID  string
	toolCallID string
	turnID     string
	responding bool
	done       chan struct{}
}

// Connection owns one strict ACP stream and exactly one session. Its wire
// types stay private to qwenapp and are never part of agentruntime.Runtime.
type Connection struct {
	transport *transport
	owner     connectionOwner
	handler   connectionHandler

	mu               sync.Mutex
	nextID           int64
	pending          map[string]*pendingCall
	inboundCalls     map[string]*inboundCall
	filePolicy       filePolicy
	activePermission *permissionTurn
	sessionID        string
	activePrompt     string
	initialized      bool
	ready            bool
	err              error
	done             chan struct{}
}

func newConnection(transport *transport, owner connectionOwner, handler connectionHandler) *Connection {
	connection := &Connection{
		transport:    transport,
		owner:        owner,
		handler:      handler,
		nextID:       1,
		pending:      make(map[string]*pendingCall),
		inboundCalls: make(map[string]*inboundCall),
		done:         make(chan struct{}),
	}
	go connection.read()
	return connection
}

// SessionID returns the process-local session identity after successful
// preflight. The identity is never persisted by this layer.
func (connection *Connection) SessionID() string {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.sessionID
}

func (connection *Connection) Done() <-chan struct{} { return connection.done }

func (connection *Connection) Err() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.err
}

func (connection *Connection) Close() error {
	connection.fail(ErrConnectionClosed)
	return nil
}

func (connection *Connection) call(method string, params, result any) error {
	return connection.callAndCommit(method, params, result, nil)
}

func (connection *Connection) callAndCommit(method string, params, result any, commit func() error) error {
	connection.mu.Lock()
	if connection.err != nil {
		err := connection.err
		connection.mu.Unlock()
		return err
	}
	if err := connection.validateOutgoingCallLocked(method, params); err != nil {
		connection.mu.Unlock()
		return err
	}
	id := integerID(connection.nextID)
	connection.nextID++
	pending := &pendingCall{method: method, response: make(chan message, 1), resume: make(chan struct{})}
	connection.pending[id.key] = pending
	if method == "session/prompt" {
		connection.activePrompt = id.key
		connection.activePermission = newPermissionTurn(connection.filePolicy.context(connection.sessionID, id.key))
	}
	connection.mu.Unlock()

	if err := connection.transport.sendRequest(id, method, params); err != nil {
		connection.fail(protocolError(err))
		return connection.Err()
	}

	select {
	case received := <-pending.response:
		var callErr error
		invalidResult := false
		if received.err != nil {
			callErr = fmt.Errorf("qwen ACP %s failed with JSON-RPC code %d", safeMethod(method), received.err.Code)
		} else if result != nil {
			callErr = decodeResult(received.result, result)
			invalidResult = callErr != nil
		}
		if callErr == nil && commit != nil {
			callErr = commit()
		}
		if invalidResult {
			connection.fail(fmt.Errorf("%w: invalid %s response", ErrProtocol, safeMethod(method)))
			close(pending.resume)
			return connection.Err()
		}
		turnErr := connection.releasePrompt(id)
		close(pending.resume)
		if callErr == nil {
			callErr = turnErr
		}
		return callErr
	case <-connection.done:
		return connection.Err()
	}
}

func (connection *Connection) validateOutgoingCallLocked(method string, params any) error {
	switch method {
	case "initialize":
		if connection.nextID != 1 || connection.initialized || connection.ready || connection.sessionID != "" {
			return fmt.Errorf("%w: initialize lifecycle", ErrProtocol)
		}
	case "session/new":
		if !connection.initialized || connection.ready || connection.sessionID != "" {
			return fmt.Errorf("%w: session/new lifecycle", ErrProtocol)
		}
	case "session/prompt":
		if !connection.ready {
			return fmt.Errorf("%w: session/prompt before preflight", ErrProtocol)
		}
		if connection.activePrompt != "" {
			return ErrTurnInProgress
		}
		if sessionIDFrom(params) != connection.sessionID {
			return fmt.Errorf("%w: session/prompt sessionId", ErrProtocol)
		}
	default:
		if !connection.ready {
			return fmt.Errorf("%w: %s before preflight", ErrProtocol, safeMethod(method))
		}
	}
	return nil
}

func sessionIDFrom(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var params sessionParams
	if json.Unmarshal(data, &params) != nil {
		return ""
	}
	return params.SessionID
}

func decodeResult(raw json.RawMessage, result any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(result); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing response data")
	}
	return nil
}

func (connection *Connection) sendNotification(method string, params any) error {
	connection.mu.Lock()
	if connection.err != nil {
		err := connection.err
		connection.mu.Unlock()
		return err
	}
	if !connection.ready || method != "session/cancel" || sessionIDFrom(params) != connection.sessionID {
		connection.mu.Unlock()
		return fmt.Errorf("%w: invalid %s notification", ErrProtocol, safeMethod(method))
	}
	var cancellations []pendingCancellation
	if method == "session/cancel" {
		if connection.activePermission != nil {
			connection.activePermission.accepting = false
		}
		cancellations = connection.claimPendingCancellationsLocked()
	}
	connection.mu.Unlock()
	if err := connection.transport.sendNotification(method, params); err != nil {
		connection.fail(protocolError(err))
		return connection.Err()
	}
	if method == "session/cancel" {
		if err := connection.cancelPendingPermissions(cancellations); err != nil {
			connection.fail(protocolError(err))
			return connection.Err()
		}
	}
	return nil
}

func (connection *Connection) respond(id requestID, result any) error {
	pending, err := connection.claimPendingResponse(id)
	if err != nil {
		return err
	}
	if err := connection.transport.sendResult(id, result); err != nil {
		connection.fail(protocolError(err))
		connection.finishPendingResponse(id, pending)
		return connection.Err()
	}
	connection.finishPendingResponse(id, pending)
	return nil
}

func (connection *Connection) read() {
	for {
		received, err := connection.transport.read()
		if err != nil {
			connection.fail(protocolError(err))
			return
		}
		switch received.kind {
		case responseMessage:
			if !connection.deliverResponse(received) {
				return
			}
		case notificationMessage:
			if err := connection.dispatchNotification(received); err != nil {
				connection.fail(err)
				return
			}
		case requestMessage:
			if err := connection.dispatchRequest(received); err != nil {
				connection.fail(err)
				return
			}
		}
	}
}

func (connection *Connection) deliverResponse(received message) bool {
	connection.mu.Lock()
	pending := connection.pending[received.id.key]
	if pending == nil {
		connection.mu.Unlock()
		connection.fail(fmt.Errorf("%w: orphan response", ErrProtocol))
		return false
	}
	delete(connection.pending, received.id.key)
	isPrompt := pending.method == "session/prompt"
	connection.mu.Unlock()
	if isPrompt {
		if !connection.waitForInboundResponses() {
			return false
		}
		if received.err == nil {
			var terminal promptResponse
			if decodeResult(received.result, &terminal) != nil || !knownStopReason(terminal.StopReason) {
				connection.fail(fmt.Errorf("%w: unknown session/prompt terminal response", ErrProtocol))
				return false
			}
		}
	}
	pending.response <- received
	select {
	case <-pending.resume:
		return connection.Err() == nil
	case <-connection.done:
		return false
	}
}

// waitForInboundResponses preserves wire order without accepting a prompt
// terminal while an agent-initiated response is only partially written. An
// unresolved inbound call is a protocol violation; in-flight responses form a
// short barrier and are rechecked after their serialized write completes.
func (connection *Connection) waitForInboundResponses() bool {
	for {
		connection.mu.Lock()
		if connection.err != nil {
			connection.mu.Unlock()
			return false
		}
		var barrier <-chan struct{}
		for _, pending := range connection.inboundCalls {
			if !pending.responding {
				connection.mu.Unlock()
				connection.fail(fmt.Errorf("%w: session/prompt completed with pending inbound request", ErrProtocol))
				return false
			}
			barrier = pending.done
		}
		connection.mu.Unlock()
		if barrier == nil {
			return true
		}
		select {
		case <-barrier:
		case <-connection.done:
			return false
		}
	}
}

func (connection *Connection) releasePrompt(id requestID) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.activePrompt == id.key {
		var turnErr error
		if connection.activePermission != nil {
			turnErr = connection.activePermission.cause
		}
		connection.activePrompt = ""
		connection.activePermission = nil
		return turnErr
	}
	return nil
}

func (connection *Connection) dispatchNotification(received message) error {
	if received.method != "session/update" {
		return fmt.Errorf("%w: unexpected notification %s", ErrProtocol, safeMethod(received.method))
	}
	var params sessionUpdateParams
	if err := decodeResult(received.params, &params); err != nil || params.SessionID == "" || len(params.Update) == 0 {
		return fmt.Errorf("%w: malformed session/update", ErrProtocol)
	}
	connection.mu.Lock()
	valid := connection.hasActivePromptLocked(params.SessionID)
	connection.mu.Unlock()
	if !valid {
		return fmt.Errorf("%w: foreign or late session/update", ErrProtocol)
	}
	if err := validateSessionUpdate(params.Update); err != nil {
		return err
	}
	if err := connection.observeToolCall(params.Update); err != nil {
		return err
	}
	if connection.handler.sessionUpdate != nil {
		if err := connection.handler.sessionUpdate(received); err != nil {
			return safeHandlerError("session/update")
		}
	}
	return nil
}

func (connection *Connection) dispatchRequest(received message) error {
	if received.method == "fs/read_text_file" {
		return connection.dispatchReadTextFile(received)
	}
	if received.method != "session/request_permission" {
		return fmt.Errorf("%w: unexpected request %s", ErrProtocol, safeMethod(received.method))
	}
	var params struct {
		SessionID string          `json:"sessionId"`
		ToolCall  json.RawMessage `json:"toolCall"`
		Options   json.RawMessage `json:"options"`
		Meta      json.RawMessage `json:"_meta,omitempty"`
	}
	if err := decodeResult(received.params, &params); err != nil || params.SessionID == "" || len(params.ToolCall) == 0 || len(params.Options) == 0 {
		return fmt.Errorf("%w: malformed session/request_permission", ErrProtocol)
	}
	var toolCall struct {
		ToolCallID string `json:"toolCallId"`
	}
	if json.Unmarshal(params.ToolCall, &toolCall) != nil || toolCall.ToolCallID == "" {
		return fmt.Errorf("%w: malformed session/request_permission toolCall", ErrProtocol)
	}
	connection.mu.Lock()
	valid := connection.hasActivePromptLocked(params.SessionID) && connection.activePermission != nil
	accepting := valid && connection.activePermission.accepting
	if valid {
		connection.inboundCalls[received.id.key] = &inboundCall{
			id: received.id, method: "session/request_permission", sessionID: params.SessionID, toolCallID: toolCall.ToolCallID,
			turnID: connection.activePrompt, done: make(chan struct{}),
		}
	}
	connection.mu.Unlock()
	if !valid {
		return fmt.Errorf("%w: foreign or stale session/request_permission", ErrProtocol)
	}
	if !accepting {
		return connection.respond(received.id, cancelledPermissionResult())
	}
	if connection.handler.permission == nil {
		return fmt.Errorf("%w: permission mediation unavailable", ErrIncompatible)
	}
	go func() {
		if err := connection.handler.permission(connection, received); err != nil {
			if errors.Is(err, errPermissionAlreadyResolved) {
				return
			}
			connection.fail(safeHandlerError("session/request_permission"))
		}
	}()
	return nil
}

func (connection *Connection) hasActivePromptLocked(sessionID string) bool {
	return connection.ready && sessionID == connection.sessionID && connection.activePrompt != ""
}

func (connection *Connection) fail(cause error) {
	connection.mu.Lock()
	if connection.err != nil {
		connection.mu.Unlock()
		return
	}
	if errors.Is(cause, ErrConnectionClosed) {
		connection.err = ErrConnectionClosed
	} else {
		connection.err = connectionError{cause: cause}
	}
	connection.pending = nil
	connection.inboundCalls = nil
	connection.activePrompt = ""
	connection.activePermission = nil
	connection.ready = false
	connection.initialized = false
	owner := connection.owner
	connection.mu.Unlock()
	if owner != nil {
		_ = owner.Close()
	}
	close(connection.done)
}

type connectionError struct{ cause error }

func (err connectionError) Error() string {
	return fmt.Sprintf("%s: %v", ErrConnectionClosed, err.cause)
}
func (err connectionError) Unwrap() []error {
	return []error{ErrConnectionClosed, err.cause}
}

func protocolError(cause error) error {
	category := "transport failure"
	switch {
	case errors.Is(cause, errInvalidNDJSON):
		category = "invalid NDJSON"
	case errors.Is(cause, errTruncatedNDJSON):
		category = "truncated NDJSON"
	case errors.Is(cause, errNDJSONLineTooLong):
		category = "oversized NDJSON"
	case errors.Is(cause, errInvalidEnvelope):
		category = "invalid JSON-RPC envelope"
	case errors.Is(cause, errDuplicateRequestID):
		category = "duplicate request ID"
	case errors.Is(cause, errOrphanResponse):
		category = "orphan response"
	case errors.Is(cause, errDuplicateResponse):
		category = "duplicate response"
	case errors.Is(cause, io.EOF):
		category = "unexpected transport close"
	}
	return fmt.Errorf("%w: %s", ErrProtocol, category)
}

func safeHandlerError(method string) error {
	return fmt.Errorf("%w: %s handler failed", ErrProtocol, safeMethod(method))
}

func safeMethod(method string) string {
	switch method {
	case "initialize", "session/new", "session/prompt", "session/cancel", "session/update", "session/request_permission", "fs/read_text_file":
		return method
	default:
		return "unknown method"
	}
}

func knownStopReason(reason string) bool {
	switch reason {
	case "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled":
		return true
	default:
		return false
	}
}

func validateSessionUpdate(raw json.RawMessage) error {
	var header struct {
		Kind    string `json:"sessionUpdate"`
		Content *struct {
			Type string `json:"type"`
		} `json:"content,omitempty"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Kind == "" {
		return fmt.Errorf("%w: malformed session/update payload", ErrProtocol)
	}
	switch header.Kind {
	case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk":
		if header.Content == nil || !knownContentType(header.Content.Type) {
			return fmt.Errorf("%w: unknown session/update content", ErrProtocol)
		}
	case "tool_call", "tool_call_update", "plan", "available_commands_update", "current_mode_update", "config_option_update", "session_info_update", "usage_update":
	default:
		return fmt.Errorf("%w: unknown session/update kind", ErrProtocol)
	}
	return nil
}

func knownContentType(kind string) bool {
	switch kind {
	case "text", "image", "audio", "resource", "resource_link":
		return true
	default:
		return false
	}
}
