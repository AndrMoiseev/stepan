package qwenapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const invalidParamsCode int64 = -32602

var errPermissionAlreadyResolved = errors.New("ACP permission request already resolved")

// filePolicy is immutable after connection setup. writableRoot remains empty
// for a read-only thread even though its process has a transport include root.
type filePolicy struct {
	workspaceRoot string
	writableRoot  string
	readRoots     []string
}

func newFilePolicy(workspaceRoot, writableRoot string) filePolicy {
	readRoots := []string{workspaceRoot}
	if writableRoot != "" {
		readRoots = append(readRoots, writableRoot)
	}
	return filePolicy{
		workspaceRoot: strings.Clone(workspaceRoot),
		writableRoot:  strings.Clone(writableRoot),
		readRoots:     append([]string(nil), readRoots...),
	}
}

func (policy filePolicy) context(sessionID, turnID string) permissionContext {
	return permissionContext{
		sessionID:     strings.Clone(sessionID),
		turnID:        strings.Clone(turnID),
		workspaceRoot: strings.Clone(policy.workspaceRoot),
		writableRoot:  strings.Clone(policy.writableRoot),
		readRoots:     append([]string(nil), policy.readRoots...),
	}
}

type permissionContext struct {
	sessionID     string
	turnID        string
	workspaceRoot string
	writableRoot  string
	readRoots     []string
}

type announcedTool struct {
	name   string
	target string
	valid  bool
}

type permissionTurn struct {
	context   permissionContext
	tools     map[string]announcedTool
	consumed  map[string]struct{}
	accepting bool
	cause     error
}

func newPermissionTurn(context permissionContext) *permissionTurn {
	return &permissionTurn{
		context:   context,
		tools:     make(map[string]announcedTool),
		consumed:  make(map[string]struct{}),
		accepting: true,
	}
}

func (connection *Connection) configureFilePolicy(workspaceRoot, writableRoot string) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.filePolicy = newFilePolicy(workspaceRoot, writableRoot)
	if connection.handler.permission == nil {
		connection.handler.permission = func(connection *Connection, received message) error {
			return connection.mediatePermission(received)
		}
	}
}

type toolCallWire struct {
	ToolCallID string             `json:"toolCallId"`
	Kind       string             `json:"kind"`
	Status     string             `json:"status"`
	Title      *string            `json:"title,omitempty"`
	Content    json.RawMessage    `json:"content,omitempty"`
	RawInput   json.RawMessage    `json:"rawInput"`
	RawOutput  json.RawMessage    `json:"rawOutput,omitempty"`
	Locations  []toolLocationWire `json:"locations"`
	Meta       json.RawMessage    `json:"_meta"`
}

type toolLocationWire struct {
	Path string          `json:"path"`
	Line json.RawMessage `json:"line,omitempty"`
	Meta json.RawMessage `json:"_meta,omitempty"`
}

func toolNameFromMeta(raw json.RawMessage) string {
	var meta struct {
		ToolName string `json:"toolName"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return ""
	}
	return meta.ToolName
}

func (connection *Connection) observeToolCall(raw json.RawMessage) error {
	var header struct {
		Kind string `json:"sessionUpdate"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Kind != "tool_call" {
		return nil
	}
	var call toolCallWire
	if json.Unmarshal(raw, &call) != nil || call.ToolCallID == "" {
		return fmt.Errorf("%w: malformed tool_call correlation", ErrProtocol)
	}
	name := toolNameFromMeta(call.Meta)
	if name == "" {
		if call.Kind == "edit" {
			return fmt.Errorf("%w: unidentifiable edit tool_call", ErrProtocol)
		}
		return nil
	}
	if !isAllowedTool(name) {
		return fmt.Errorf("%w: unexpected tool_call", ErrProtocol)
	}
	if name != "write_file" && name != "edit" {
		// Native read/search events are evidence, not an OS-level boundary.
		return nil
	}

	connection.mu.Lock()
	turn := connection.activePermission
	if turn == nil {
		connection.mu.Unlock()
		return fmt.Errorf("%w: tool_call outside active turn", ErrProtocol)
	}
	context := turn.context
	if _, duplicate := turn.tools[call.ToolCallID]; duplicate {
		connection.mu.Unlock()
		return fmt.Errorf("%w: duplicate tool_call ID", ErrProtocol)
	}
	connection.mu.Unlock()

	path, pathErr := toolPathFromInput(call.RawInput)
	target, targetErr := canonicalTargetWithin(context.workspaceRoot, context.writableRoot, path)
	valid := call.Kind == "edit" && (call.Status == "pending" || call.Status == "") && pathErr == nil && targetErr == nil
	if valid && len(call.Locations) > 0 {
		if len(call.Locations) != 1 {
			valid = false
		} else if location, err := canonicalTarget(context.workspaceRoot, call.Locations[0].Path); err != nil || location != target {
			valid = false
		}
	}

	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.activePermission != turn || turn.context.turnID != context.turnID {
		return fmt.Errorf("%w: stale tool_call", ErrProtocol)
	}
	if _, duplicate := turn.tools[call.ToolCallID]; duplicate {
		return fmt.Errorf("%w: duplicate tool_call ID", ErrProtocol)
	}
	turn.tools[call.ToolCallID] = announcedTool{name: name, target: target, valid: valid}
	return nil
}

func isAllowedTool(name string) bool {
	for _, allowed := range allowedToolNames {
		if name == allowed {
			return true
		}
	}
	return false
}

func toolPathFromInput(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", ErrPermissionDenied
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil || input == nil {
		return "", ErrPermissionDenied
	}
	var found string
	for _, field := range []string{"file_path", "path"} {
		value, ok := input[field]
		if !ok {
			continue
		}
		if found != "" {
			return "", ErrPermissionDenied
		}
		if json.Unmarshal(value, &found) != nil || strings.TrimSpace(found) == "" {
			return "", ErrPermissionDenied
		}
	}
	if found == "" {
		return "", ErrPermissionDenied
	}
	return found, nil
}

type permissionOption struct {
	OptionID string          `json:"optionId"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Meta     json.RawMessage `json:"_meta,omitempty"`
}

type permissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  toolCallWire       `json:"toolCall"`
	Options   []permissionOption `json:"options"`
	Meta      json.RawMessage    `json:"_meta"`
}

func decodePermissionRequest(raw json.RawMessage) (permissionRequest, error) {
	var request permissionRequest
	if err := decodeStrict(raw, &request); err != nil || request.SessionID == "" || request.ToolCall.ToolCallID == "" || len(request.Options) == 0 {
		return permissionRequest{}, ErrPermissionDenied
	}
	return request, nil
}

func allowOnceOption(options []permissionOption) (string, error) {
	seen := make(map[string]struct{}, len(options))
	allowOnce := ""
	for _, option := range options {
		if option.OptionID == "" || option.Name == "" {
			return "", ErrPermissionDenied
		}
		if _, duplicate := seen[option.OptionID]; duplicate {
			return "", ErrPermissionDenied
		}
		seen[option.OptionID] = struct{}{}
		switch option.Kind {
		case "allow_once":
			if allowOnce != "" {
				return "", ErrPermissionDenied
			}
			allowOnce = option.OptionID
		// Compatible Qwen releases commonly offer persistent choices beside
		// allow_once. They may be displayed but are never selected by Stepan.
		case "allow_always", "reject_once", "reject_always":
		default:
			return "", ErrPermissionDenied
		}
	}
	if allowOnce == "" {
		return "", ErrPermissionDenied
	}
	return allowOnce, nil
}

func (connection *Connection) mediatePermission(received message) error {
	request, decodeErr := decodePermissionRequest(received.params)
	allowOption := ""
	grantErr := decodeErr
	if grantErr == nil {
		allowOption, grantErr = allowOnceOption(request.Options)
	}

	connection.mu.Lock()
	pending := connection.inboundCalls[received.id.key]
	turn := connection.activePermission
	requestErr := decodeErr
	correlated := pending != nil && turn != nil && turn.accepting && pending.turnID == turn.context.turnID && pending.sessionID == turn.context.sessionID
	if !correlated || (decodeErr == nil && (request.SessionID != pending.sessionID || pending.toolCallID != request.ToolCall.ToolCallID)) {
		requestErr = ErrPermissionDenied
	}
	var announced announcedTool
	if correlated {
		var ok bool
		announced, ok = turn.tools[pending.toolCallID]
		if !ok {
			requestErr = ErrPermissionDenied
		} else if _, used := turn.consumed[pending.toolCallID]; used {
			requestErr = ErrPermissionDenied
		} else {
			// A tool call gets one decision regardless of allow or deny.
			turn.consumed[pending.toolCallID] = struct{}{}
		}
	}
	if requestErr == nil && grantErr != nil {
		requestErr = grantErr
	}
	context := permissionContext{}
	if turn != nil {
		context = turn.context
	}
	connection.mu.Unlock()

	if requestErr == nil {
		// Defense in depth: the permission request is validated independently
		// from the earlier tool_call announcement, then both immutable snapshots
		// are compared. Sharing one decoded value would weaken correlation.
		name := toolNameFromMeta(request.ToolCall.Meta)
		path, err := toolPathFromInput(request.ToolCall.RawInput)
		target, targetErr := canonicalTargetWithin(context.workspaceRoot, context.writableRoot, path)
		if err != nil || targetErr != nil || !announced.valid || name != announced.name ||
			(name != "write_file" && name != "edit") || request.ToolCall.Kind != "edit" ||
			(request.ToolCall.Status != "pending" && request.ToolCall.Status != "") || target != announced.target {
			requestErr = ErrPermissionDenied
		}
		if requestErr == nil && len(request.ToolCall.Locations) > 0 {
			if len(request.ToolCall.Locations) != 1 {
				requestErr = ErrPermissionDenied
			} else if location, locationErr := canonicalTarget(context.workspaceRoot, request.ToolCall.Locations[0].Path); locationErr != nil || location != target {
				requestErr = ErrPermissionDenied
			}
		}
	}

	if requestErr != nil {
		connection.recordPermissionDenial(turn)
		return connection.respond(received.id, cancelledPermissionResult())
	}
	return connection.respond(received.id, selectedPermissionResult(allowOption))
}

func selectedPermissionResult(optionID string) any {
	return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": optionID}}
}

func cancelledPermissionResult() any {
	return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
}

type pendingCancellation struct {
	id      requestID
	pending *inboundCall
}

func (connection *Connection) claimPendingCancellationsLocked() []pendingCancellation {
	var responses []pendingCancellation
	for _, pending := range connection.inboundCalls {
		if pending.method != "session/request_permission" || pending.responding {
			continue
		}
		pending.responding = true
		responses = append(responses, pendingCancellation{id: pending.id, pending: pending})
	}
	return responses
}

func (connection *Connection) cancelPendingPermissions(responses []pendingCancellation) error {
	for _, response := range responses {
		if err := connection.transport.sendResult(response.id, cancelledPermissionResult()); err != nil {
			connection.finishPendingResponse(response.id, response.pending)
			return err
		}
		connection.finishPendingResponse(response.id, response.pending)
	}
	return nil
}

type readTextFileRequest struct {
	SessionID string          `json:"sessionId"`
	Path      string          `json:"path"`
	Line      *uint32         `json:"line,omitempty"`
	Limit     *uint32         `json:"limit,omitempty"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

func (connection *Connection) dispatchReadTextFile(received message) error {
	var request readTextFileRequest
	if err := decodeStrict(received.params, &request); err != nil || request.SessionID == "" || request.Path == "" || request.Line != nil && *request.Line == 0 {
		return fmt.Errorf("%w: malformed fs/read_text_file", ErrProtocol)
	}
	connection.mu.Lock()
	valid := connection.hasActivePromptLocked(request.SessionID) && connection.activePermission != nil
	accepting := valid && connection.activePermission.accepting
	if valid {
		connection.inboundCalls[received.id.key] = &inboundCall{
			id: received.id, method: "fs/read_text_file", sessionID: request.SessionID,
			turnID: connection.activePrompt, done: make(chan struct{}),
		}
	}
	connection.mu.Unlock()
	if !valid {
		return fmt.Errorf("%w: foreign or stale fs/read_text_file", ErrProtocol)
	}
	if !accepting {
		return connection.respondError(received.id, invalidParamsCode, "filesystem read denied")
	}
	go connection.readTextFile(received.id, request)
	return nil
}

func (connection *Connection) readTextFile(id requestID, request readTextFileRequest) {
	connection.mu.Lock()
	pending := connection.inboundCalls[id.key]
	turn := connection.activePermission
	valid := pending != nil && turn != nil && turn.accepting && pending.turnID == turn.context.turnID &&
		pending.sessionID == turn.context.sessionID && request.SessionID == pending.sessionID
	context := permissionContext{}
	if valid {
		context = turn.context
	}
	connection.mu.Unlock()
	if !valid {
		_ = connection.respondError(id, invalidParamsCode, "filesystem read denied")
		return
	}

	target, err := canonicalReadableTarget(context, request.Path)
	if err != nil {
		connection.recordPermissionDenial(turn)
		_ = connection.respondError(id, invalidParamsCode, "filesystem read denied")
		return
	}
	content, err := os.ReadFile(target)
	if err != nil || !utf8.Valid(content) {
		_ = connection.respondError(id, invalidParamsCode, "filesystem read unavailable")
		return
	}
	text := selectLines(string(content), request.Line, request.Limit)
	if err := connection.respond(id, map[string]string{"content": text}); err != nil && !errors.Is(err, errPermissionAlreadyResolved) {
		connection.fail(safeHandlerError("fs/read_text_file"))
	}
}

func canonicalReadableTarget(context permissionContext, supplied string) (string, error) {
	target, err := canonicalTarget(context.workspaceRoot, supplied)
	if err != nil {
		return "", ErrPermissionDenied
	}
	allowed := false
	for _, root := range context.readRoots {
		if root != "" && target != root && pathWithin(root, target) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", ErrPermissionDenied
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrPermissionDenied
	}
	return target, nil
}

func selectLines(content string, line, limit *uint32) string {
	if line == nil && limit == nil {
		return content
	}
	lines := strings.SplitAfter(content, "\n")
	start := 0
	if line != nil {
		start = int(*line - 1)
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit != nil && uint64(start)+uint64(*limit) < uint64(end) {
		end = start + int(*limit)
	}
	return strings.Join(lines[start:end], "")
}

func decodeStrict(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func (connection *Connection) respondError(id requestID, code int64, text string) error {
	pending, err := connection.claimPendingResponse(id)
	if err != nil {
		return err
	}
	if err := connection.transport.sendError(id, code, text); err != nil {
		connection.finishPendingResponse(id, pending)
		connection.fail(protocolError(err))
		return connection.Err()
	}
	connection.finishPendingResponse(id, pending)
	return nil
}

func (connection *Connection) recordPermissionDenial(turn *permissionTurn) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.activePermission == turn && turn != nil && turn.accepting && turn.cause == nil {
		turn.cause = ErrPermissionDenied
	}
}

func (connection *Connection) claimPendingResponse(id requestID) (*inboundCall, error) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.err != nil {
		return nil, connection.err
	}
	pending := connection.inboundCalls[id.key]
	if pending == nil {
		return nil, errPermissionAlreadyResolved
	}
	if pending.responding {
		return nil, errPermissionAlreadyResolved
	}
	pending.responding = true
	return pending, nil
}

func (connection *Connection) finishPendingResponse(id requestID, pending *inboundCall) {
	connection.mu.Lock()
	if current := connection.inboundCalls[id.key]; current == pending {
		delete(connection.inboundCalls, id.key)
	}
	connection.mu.Unlock()
	close(pending.done)
}
