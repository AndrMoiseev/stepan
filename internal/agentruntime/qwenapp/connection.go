package qwenapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

type connectionOwner interface {
	Close() error
}

const maxConnectionAuditIdentities = 256

type connectionHandler struct {
	sessionUpdate func(message) error
	permission    func(*Connection, message) error
}

type pendingCall struct {
	method    string
	response  chan message
	resume    chan struct{}
	assembler *responseAssembler
	terminal  bool
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

// connectionAudit is bounded process-local evidence used by the opt-in
// conformance harness. It contains method/tool names and counters only: wire
// payloads, prompts, paths, content, and provider identifiers are excluded.
type connectionAudit struct {
	tools                 map[string]int
	permissionRequestIDs  map[string]struct{}
	permissionToolCallIDs map[string]struct{}
	permissionRequests    int
	allowOnceSelections   int
	permissionDenials     int
}

type connectionAuditSnapshot struct {
	Tools                       map[string]int
	PermissionRequests          int
	UniquePermissionRequestIDs  int
	UniquePermissionToolCallIDs int
	AllowOnceSelections         int
	PermissionDenials           int
	PreflightInventory          []string
	PreflightInventoryPresent   bool
}

// Connection owns one strict ACP stream and exactly one session. Its wire
// types stay private to qwenapp and are never part of agentruntime.Runtime.
type Connection struct {
	transport *transport
	owner     connectionOwner
	handler   connectionHandler
	dispatch  sync.Mutex

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
	audit            connectionAudit
	preflightTools   []string
	preflightPresent bool
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
		audit: connectionAudit{
			tools: make(map[string]int), permissionRequestIDs: make(map[string]struct{}),
			permissionToolCallIDs: make(map[string]struct{}),
		},
	}
	go connection.read()
	return connection
}

func (connection *Connection) auditSnapshot() connectionAuditSnapshot {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	tools := make(map[string]int, len(connection.audit.tools))
	for name, count := range connection.audit.tools {
		tools[name] = count
	}
	return connectionAuditSnapshot{
		Tools: tools, PermissionRequests: connection.audit.permissionRequests,
		UniquePermissionRequestIDs:  len(connection.audit.permissionRequestIDs),
		UniquePermissionToolCallIDs: len(connection.audit.permissionToolCallIDs),
		AllowOnceSelections:         connection.audit.allowOnceSelections,
		PermissionDenials:           connection.audit.permissionDenials,
		PreflightInventory:          append([]string(nil), connection.preflightTools...),
		PreflightInventoryPresent:   connection.preflightPresent,
	}
}

func (connection *Connection) recordPreflightInventory(inventory []string) error {
	copyOfInventory := append([]string(nil), inventory...)
	sort.Strings(copyOfInventory)
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.preflightPresent && !equalStrings(connection.preflightTools, copyOfInventory) {
		return withDiagnosticContext(fmt.Errorf("%w: contradictory tool inventory status", ErrIncompatible), diagnosticToolInventory)
	}
	connection.preflightTools = copyOfInventory
	connection.preflightPresent = true
	return nil
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

// checkpointClose fences the reader before process teardown. It waits for an
// already-dispatched frame to finish, then invalidates all wire state without
// waiting on the child; the owning thread closes the contained process next.
func (connection *Connection) checkpointClose() {
	connection.dispatch.Lock()
	defer connection.dispatch.Unlock()
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.err != nil {
		return
	}
	connection.err = ErrConnectionClosed
	connection.pending = nil
	connection.inboundCalls = nil
	connection.activePrompt = ""
	connection.activePermission = nil
	connection.ready = false
	connection.initialized = false
	close(connection.done)
}

func (connection *Connection) call(method string, params, result any) error {
	return connection.callAndCommit(method, params, result, nil)
}

func (connection *Connection) callAndCommit(method string, params, result any, commit func() error) error {
	return connection.callAndCommitResponse(method, params, result, commit, nil)
}

func (connection *Connection) callPrompt(params sessionPromptParams, result *promptResponse, assembler *responseAssembler, commit func() error) error {
	return connection.callAndCommitResponse("session/prompt", params, result, commit, assembler)
}

func (connection *Connection) callAndCommitResponse(method string, params, result any, commit func() error, assembler *responseAssembler) error {
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
	pending := &pendingCall{method: method, response: make(chan message, 1), resume: make(chan struct{}), assembler: assembler}
	connection.pending[id.key] = pending
	if method == "session/prompt" {
		if assembler != nil {
			if err := assembler.bind(turnIdentity{sessionID: connection.sessionID, promptID: id.key}); err != nil {
				delete(connection.pending, id.key)
				connection.mu.Unlock()
				return err
			}
		}
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
			callErr = withRPCDiagnostic(
				fmt.Errorf("qwen ACP %s failed with JSON-RPC code %d", safeMethod(method), received.err.Code),
				received.err.Code,
				received.err.Message,
			)
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
		if connectionErr := connection.Err(); connectionErr != nil {
			callErr = connectionErr
		} else if turnErr != nil {
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
		connection.dispatch.Lock()
		if err != nil {
			connection.fail(protocolError(err))
			connection.dispatch.Unlock()
			return
		}
		keepReading := true
		switch received.kind {
		case responseMessage:
			if !connection.deliverResponse(received) {
				keepReading = false
			}
		case notificationMessage:
			if err := connection.dispatchNotification(received); err != nil {
				connection.fail(err)
				keepReading = false
			}
		case requestMessage:
			if err := connection.dispatchRequest(received); err != nil {
				connection.fail(err)
				keepReading = false
			}
		}
		connection.dispatch.Unlock()
		if !keepReading {
			return
		}
	}
}

func (connection *Connection) deliverResponse(received message) bool {
	connection.mu.Lock()
	pending := connection.pending[received.id.key]
	if pending == nil {
		connection.mu.Unlock()
		connection.fail(withDiagnosticContext(fmt.Errorf("%w: orphan response", ErrProtocol), diagnosticJSONRPCCorrelation))
		return false
	}
	isPrompt := pending.method == "session/prompt"
	if !isPrompt {
		delete(connection.pending, received.id.key)
	}
	connection.mu.Unlock()
	if isPrompt {
		if !connection.waitForInboundResponses() {
			return false
		}
		if received.err == nil {
			var terminal promptResponse
			if decodeResult(received.result, &terminal) != nil || !knownStopReason(terminal.StopReason) {
				connection.fail(withDiagnosticContext(fmt.Errorf("%w: unknown session/prompt terminal response", ErrProtocol), diagnosticPromptTerminal))
				return false
			}
		}
		if pending.assembler != nil {
			connection.mu.Lock()
			sessionID := connection.sessionID
			connection.mu.Unlock()
			if err := pending.assembler.terminal(turnIdentity{sessionID: sessionID, promptID: received.id.key}); err != nil {
				connection.fail(withDiagnosticContext(err, diagnosticAssistantContent))
				return false
			}
		}
		connection.mu.Lock()
		if current := connection.pending[received.id.key]; current != pending || pending.terminal {
			connection.mu.Unlock()
			connection.fail(withDiagnosticContext(fmt.Errorf("%w: duplicate session/prompt terminal", ErrProtocol), diagnosticPromptTerminal))
			return false
		}
		pending.terminal = true
		if connection.activePermission != nil {
			connection.activePermission.accepting = false
		}
		connection.mu.Unlock()
		if !connection.dispatchBufferedAfterPromptTerminal() {
			return false
		}
		pending.response <- received
		return true
	}
	pending.response <- received
	select {
	case <-pending.resume:
		return connection.Err() == nil
	case <-connection.done:
		return false
	}
}

// dispatchBufferedAfterPromptTerminal handles frames already decoded into the
// NDJSON reader before the caller is allowed to commit and release the prompt.
// Since no following prompt can have been sent yet, a session update here
// unambiguously belongs to the terminaled assembler and must fail closed.
func (connection *Connection) dispatchBufferedAfterPromptTerminal() bool {
	for connection.transport.hasBufferedFrame() {
		received, err := connection.transport.read()
		if err != nil {
			connection.fail(protocolError(err))
			return false
		}
		switch received.kind {
		case responseMessage:
			if !connection.deliverResponse(received) {
				return false
			}
		case notificationMessage:
			if err := connection.dispatchNotification(received); err != nil {
				connection.fail(err)
				return false
			}
		case requestMessage:
			if err := connection.dispatchRequest(received); err != nil {
				connection.fail(err)
				return false
			}
		}
	}
	return true
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
				connection.fail(withDiagnosticContext(fmt.Errorf("%w: session/prompt completed with pending inbound request", ErrProtocol), diagnosticAgentRequest))
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
		delete(connection.pending, id.key)
		return turnErr
	}
	return nil
}

func (connection *Connection) dispatchNotification(received message) error {
	if received.method != "session/update" {
		return withDiagnosticContext(fmt.Errorf("%w: unexpected notification %s", ErrProtocol, safeMethod(received.method)), diagnosticNotificationMethod)
	}
	var params sessionUpdateParams
	if err := decodeResult(received.params, &params); err != nil || !validAgentIdentity(params.SessionID) || len(params.Update) == 0 {
		return withDiagnosticContext(fmt.Errorf("%w: malformed session/update", ErrProtocol), diagnosticSessionUpdateEnvelope)
	}
	kind, err := validateSessionUpdate(params.Update)
	if err != nil {
		return err
	}
	connection.mu.Lock()
	promptID := connection.activePrompt
	pending := connection.pending[promptID]
	validSession := connection.ready && params.SessionID == connection.sessionID
	connection.mu.Unlock()
	if !validSession {
		return withDiagnosticContext(fmt.Errorf("%w: foreign or late session/update", ErrProtocol), diagnosticSessionUpdateLifecycle)
	}
	if promptID == "" || pending == nil {
		if !informationalSessionUpdate(kind) {
			return withDiagnosticContext(fmt.Errorf("%w: foreign or late session/update", ErrProtocol), diagnosticSessionUpdateLifecycle)
		}
		if connection.handler.sessionUpdate != nil {
			if err := connection.handler.sessionUpdate(received); err != nil {
				return withDiagnosticContext(safeHandlerError("session/update"), diagnosticSessionUpdatePayload)
			}
		}
		return nil
	}
	if pending.assembler != nil {
		if err := pending.assembler.observe(turnIdentity{sessionID: params.SessionID, promptID: promptID}, params.Update); err != nil {
			return withDiagnosticContext(err, diagnosticAssistantContent)
		}
	}
	if pending.terminal {
		return withDiagnosticContext(fmt.Errorf("%w: late session/update", ErrProtocol), diagnosticSessionUpdateLifecycle)
	}
	if err := connection.observeToolCall(params.Update); err != nil {
		return withDiagnosticContext(err, diagnosticToolCallUpdate)
	}
	if connection.handler.sessionUpdate != nil {
		if err := connection.handler.sessionUpdate(received); err != nil {
			return withDiagnosticContext(safeHandlerError("session/update"), diagnosticSessionUpdatePayload)
		}
	}
	return nil
}

func (connection *Connection) dispatchRequest(received message) error {
	if received.method == "fs/read_text_file" {
		if err := connection.dispatchReadTextFile(received); err != nil {
			return withDiagnosticContext(err, diagnosticAgentRequest)
		}
		return nil
	}
	if received.method != "session/request_permission" {
		return withDiagnosticContext(fmt.Errorf("%w: unexpected request %s", ErrProtocol, safeMethod(received.method)), diagnosticAgentRequest)
	}
	var params struct {
		SessionID string          `json:"sessionId"`
		ToolCall  json.RawMessage `json:"toolCall"`
		Options   json.RawMessage `json:"options"`
		Meta      json.RawMessage `json:"_meta,omitempty"`
	}
	if err := decodeResult(received.params, &params); err != nil || !validAgentIdentity(params.SessionID) || len(params.ToolCall) == 0 || len(params.Options) == 0 {
		return withDiagnosticContext(fmt.Errorf("%w: malformed session/request_permission", ErrProtocol), diagnosticAgentRequest)
	}
	var toolCall struct {
		ToolCallID string `json:"toolCallId"`
	}
	if json.Unmarshal(params.ToolCall, &toolCall) != nil || !validToolCallID(toolCall.ToolCallID) {
		return withDiagnosticContext(fmt.Errorf("%w: malformed session/request_permission toolCall", ErrProtocol), diagnosticAgentRequest)
	}
	connection.mu.Lock()
	valid := connection.hasActivePromptLocked(params.SessionID) && connection.activePermission != nil
	accepting := valid && connection.activePermission.accepting
	if valid {
		if len(connection.inboundCalls) >= maxOutstandingACPCalls {
			connection.mu.Unlock()
			return fmt.Errorf("%w: too many active inbound requests", ErrProtocol)
		}
		connection.inboundCalls[received.id.key] = &inboundCall{
			id: received.id, method: "session/request_permission", sessionID: params.SessionID, toolCallID: toolCall.ToolCallID,
			turnID: connection.activePrompt, done: make(chan struct{}),
		}
		connection.audit.permissionRequests++
		if len(connection.audit.permissionRequestIDs) < maxConnectionAuditIdentities {
			connection.audit.permissionRequestIDs[received.id.key] = struct{}{}
		}
		if len(connection.audit.permissionToolCallIDs) < maxConnectionAuditIdentities {
			connection.audit.permissionToolCallIDs[toolCall.ToolCallID] = struct{}{}
		}
	}
	connection.mu.Unlock()
	if !valid {
		return withDiagnosticContext(fmt.Errorf("%w: foreign or stale session/request_permission", ErrProtocol), diagnosticAgentRequest)
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
			connection.fail(withDiagnosticContext(safeHandlerError("session/request_permission"), diagnosticAgentRequest))
		}
	}()
	return nil
}

func (connection *Connection) hasActivePromptLocked(sessionID string) bool {
	pending := connection.pending[connection.activePrompt]
	return connection.ready && sessionID == connection.sessionID && connection.activePrompt != "" && pending != nil && !pending.terminal
}

func (connection *Connection) fail(cause error) {
	connection.mu.Lock()
	if connection.err != nil {
		connection.mu.Unlock()
		return
	}
	phase := connection.pendingDiagnosticContextLocked()
	if errors.Is(cause, ErrProtocol) && errorDiagnosticContext(cause) == "" && phase != diagnosticNone {
		cause = withDiagnosticContext(cause, phase)
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
	if errors.Is(cause, agentruntime.ErrRuntimeExited) {
		if source, ok := owner.(interface{ failureDiagnostic() processExitDiagnostic }); ok {
			diagnostic := source.failureDiagnostic()
			diagnostic.phase = phase
			cause = withProcessExitDiagnostic(cause, diagnostic)
			connection.mu.Lock()
			connection.err = connectionError{cause: cause}
			connection.mu.Unlock()
		}
	}
	close(connection.done)
}

func (connection *Connection) pendingDiagnosticContextLocked() diagnosticContext {
	if len(connection.pending) != 1 {
		return diagnosticNone
	}
	for _, pending := range connection.pending {
		switch pending.method {
		case "initialize":
			return diagnosticInitializeLifecycle
		case "session/new":
			return diagnosticSessionLifecycle
		case "session/prompt":
			return diagnosticPromptLifecycle
		}
	}
	return diagnosticNone
}

type connectionError struct{ cause error }

func (err connectionError) Error() string {
	return fmt.Sprintf("%s: %v", ErrConnectionClosed, err.cause)
}
func (err connectionError) Unwrap() []error {
	return []error{ErrConnectionClosed, err.cause}
}

func protocolError(cause error) error {
	if errors.Is(cause, io.EOF) && !errors.Is(cause, errTruncatedNDJSON) {
		return agentruntime.ErrRuntimeExited
	}
	category := "transport failure"
	context := diagnosticACPTransport
	switch {
	case errors.Is(cause, errInvalidNDJSON):
		category = "invalid NDJSON"
	case errors.Is(cause, errTruncatedNDJSON):
		category = "truncated NDJSON"
	case errors.Is(cause, errNDJSONLineTooLong):
		category = "oversized NDJSON"
	case errors.Is(cause, errInvalidEnvelope):
		category = "invalid JSON-RPC envelope"
		context = diagnosticJSONRPCEnvelope
	case errors.Is(cause, errDuplicateRequestID):
		category = "duplicate request ID"
		context = diagnosticJSONRPCCorrelation
	case errors.Is(cause, errOrphanResponse):
		category = "orphan response"
		context = diagnosticJSONRPCCorrelation
	case errors.Is(cause, errDuplicateResponse):
		category = "duplicate response"
		context = diagnosticJSONRPCCorrelation
	case errors.Is(cause, errRequestIDTooLong):
		category = "oversized request ID"
		context = diagnosticJSONRPCCorrelation
	case errors.Is(cause, errTooManyRequests):
		category = "too many outstanding requests"
		context = diagnosticJSONRPCCorrelation
	}
	return withDiagnosticContext(fmt.Errorf("%w: %s", ErrProtocol, category), context)
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

func validateSessionUpdate(raw json.RawMessage) (string, error) {
	var header struct {
		Kind    string `json:"sessionUpdate"`
		Content *struct {
			Type string `json:"type"`
		} `json:"content,omitempty"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Kind == "" {
		return "", withDiagnosticContext(fmt.Errorf("%w: malformed session/update payload", ErrProtocol), diagnosticSessionUpdatePayload)
	}
	switch header.Kind {
	case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk":
		if header.Content == nil || !knownContentType(header.Content.Type) {
			return "", withDiagnosticContext(fmt.Errorf("%w: unknown session/update content", ErrProtocol), diagnosticSessionUpdateContent)
		}
	case "tool_call", "tool_call_update", "plan", "available_commands_update", "current_mode_update", "config_option_update", "session_info_update", "usage_update":
	default:
		return "", withDiagnosticContext(fmt.Errorf("%w: unknown session/update kind", ErrProtocol), diagnosticSessionUpdateKind)
	}
	return header.Kind, nil
}

func informationalSessionUpdate(kind string) bool {
	switch kind {
	case "available_commands_update", "current_mode_update", "config_option_update", "session_info_update", "usage_update":
		return true
	default:
		return false
	}
}

func knownContentType(kind string) bool {
	switch kind {
	case "text", "image", "audio", "resource", "resource_link":
		return true
	default:
		return false
	}
}
