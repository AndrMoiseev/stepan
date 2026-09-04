package qwenapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type acpObservation struct {
	ClientName               string `json:"clientName"`
	ClientTitle              string `json:"clientTitle"`
	ProtocolVersion          int    `json:"protocolVersion"`
	CWD                      string `json:"cwd"`
	MCPServerCount           int    `json:"mcpServerCount"`
	HasAdditionalDirectories bool   `json:"hasAdditionalDirectories"`
	ModelPromptSeen          bool   `json:"modelPromptSeen"`
}

func runQwenACPFake() int {
	transport := newTransport(os.Stdin, os.Stdout)
	initialize, err := transport.read()
	if err != nil || initialize.kind != requestMessage || initialize.method != "initialize" {
		return 61
	}
	var initialized initializeParams
	if json.Unmarshal(initialize.params, &initialized) != nil {
		return 62
	}
	observation := acpObservation{
		ClientName: initialized.ClientInfo.Name, ClientTitle: initialized.ClientInfo.Title,
		ProtocolVersion: initialized.ProtocolVersion,
	}
	capabilities := any(map[string]any{
		"promptCapabilities":  map[string]any{},
		"sessionCapabilities": map[string]any{},
	})
	scenario := os.Getenv("STEPAN_QWEN_ACP_CASE")
	if scenario == "missing-capability" {
		capabilities = nil
	} else if scenario == "empty-capabilities" {
		capabilities = map[string]any{}
	}
	if err := transport.sendResult(initialize.id, map[string]any{
		"protocolVersion":   acpProtocolVersion,
		"agentCapabilities": capabilities,
		"agentInfo":         map[string]string{"name": "compatible-third-party", "version": "unbranded"},
		"authMethods":       []any{},
	}); err != nil {
		return 63
	}
	if scenario == "missing-capability" || scenario == "empty-capabilities" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	}
	session, err := transport.read()
	if err != nil || session.kind != requestMessage || session.method != "session/new" {
		return 64
	}
	var fields map[string]json.RawMessage
	var params newSessionParams
	if json.Unmarshal(session.params, &fields) != nil || json.Unmarshal(session.params, &params) != nil {
		return 65
	}
	observation.CWD = params.CWD
	observation.MCPServerCount = len(params.MCPServers)
	_, observation.HasAdditionalDirectories = fields["additionalDirectories"]
	if err := writeACPObservation(observation); err != nil {
		return 66
	}
	if os.Getenv("STEPAN_QWEN_ACP_CASE") == "session-error" {
		if err := transport.sendError(session.id, -32602, "credential-body startup root refused"); err != nil {
			return 68
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	}
	if err := transport.sendResult(session.id, map[string]any{"sessionId": "session-one"}); err != nil {
		return 67
	}
	for {
		message, err := transport.read()
		if err != nil {
			return 0
		}
		if message.method == "session/prompt" {
			observation.ModelPromptSeen = true
			_ = writeACPObservation(observation)
		}
	}
}

func writeACPObservation(observation acpObservation) error {
	data, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	return os.WriteFile(os.Getenv("STEPAN_QWEN_ACP_OBSERVATION"), data, 0o600)
}

func TestOpenConnectionPreflightsWithoutAdditionalDirectories(t *testing.T) {
	workspace := makeGitRoot(t)
	observationPath := filepath.Join(t.TempDir(), "observation.json")
	t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
	t.Setenv("STEPAN_QWEN_ACP_CASE", "success")
	t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", observationPath)
	process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: workspace, JSONContract: testJSONContract}, t.TempDir())
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	connection, err := OpenConnection(process)
	if err != nil {
		t.Fatal(err)
	}
	if connection.SessionID() != "session-one" {
		t.Fatalf("session ID = %q", connection.SessionID())
	}
	var observation acpObservation
	readJSONEventually(t, observationPath, &observation)
	if observation.ClientName != "stepan" || observation.ClientTitle != "Stepan" || observation.ProtocolVersion != acpProtocolVersion {
		t.Fatalf("initialize = %+v", observation)
	}
	if observation.CWD != process.WorkspaceRoot() || observation.MCPServerCount != 0 || observation.HasAdditionalDirectories {
		t.Fatalf("session/new = %+v", observation)
	}
	if observation.ModelPromptSeen {
		t.Fatal("model prompt ran during preflight")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenConnectionClosesProcessOnMissingCapability(t *testing.T) {
	for _, scenario := range []string{"missing-capability", "empty-capabilities"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
			t.Setenv("STEPAN_QWEN_ACP_CASE", scenario)
			t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", filepath.Join(t.TempDir(), "observation.json"))
			process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
			if err := process.Start(); err != nil {
				t.Fatal(err)
			}
			connection, err := OpenConnection(process)
			if connection != nil || !errors.Is(err, ErrIncompatible) {
				t.Fatalf("OpenConnection = %v, %v", connection, err)
			}
			if process.command == nil || process.command.ProcessState == nil || !process.command.ProcessState.Exited() {
				t.Fatal("incompatible ACP child was not closed and reaped")
			}
			if code := process.ExitCode(); code == nil {
				t.Fatal("closed ACP child has no exit state")
			}
		})
	}
}

func TestOpenConnectionRejectsMissingStartupRootEvidence(t *testing.T) {
	t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
	t.Setenv("STEPAN_QWEN_ACP_CASE", "success")
	t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", filepath.Join(t.TempDir(), "observation.json"))
	process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	process.mu.Lock()
	process.command.Args = append([]string(nil), process.command.Args[:len(process.command.Args)-2]...)
	process.command.Args = append(process.command.Args, "--acp")
	process.mu.Unlock()
	connection, err := OpenConnection(process)
	if connection != nil || !errors.Is(err, ErrIncompatible) || !strings.Contains(err.Error(), "startup-root contract") {
		t.Fatalf("OpenConnection = %v, %v", connection, err)
	}
	if process.command.ProcessState == nil || !process.command.ProcessState.Exited() {
		t.Fatal("startup-contract failure did not close and reap child")
	}
}

func TestOpenConnectionClosesProcessOnSessionContractFailure(t *testing.T) {
	t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
	t.Setenv("STEPAN_QWEN_ACP_CASE", "session-error")
	t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", filepath.Join(t.TempDir(), "observation.json"))
	process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	connection, err := OpenConnection(process)
	if connection != nil || !errors.Is(err, ErrIncompatible) || !strings.Contains(err.Error(), "session/new lifecycle") {
		t.Fatalf("OpenConnection = %v, %v", connection, err)
	}
	if strings.Contains(err.Error(), "credential-body") {
		t.Fatalf("provider response leaked: %v", err)
	}
	if process.command == nil || process.command.ProcessState == nil || !process.command.ProcessState.Exited() {
		t.Fatal("incompatible ACP child was not closed and reaped")
	}
}

func TestPreflightValidatesOptionalToolInventory(t *testing.T) {
	tests := []struct {
		name      string
		inventory []string
		want      string
	}{
		{name: "exact", inventory: []string{"read_file", "write_file", "edit", "glob", "grep_search"}},
		{name: "missing", inventory: []string{"read_file", "write_file", "edit", "glob"}, want: "missing required tool grep_search"},
		{name: "shell", inventory: []string{"read_file", "write_file", "edit", "glob", "grep_search", "shell"}, want: "unexpected tool shell"},
		{name: "web", inventory: []string{"read_file", "write_file", "edit", "glob", "grep_search", "web"}, want: "unexpected tool web"},
		{name: "agent", inventory: []string{"read_file", "write_file", "edit", "glob", "grep_search", "agent"}, want: "unexpected tool agent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			init := map[string]any{
				"protocolVersion": acpProtocolVersion,
				"agentCapabilities": map[string]any{
					"promptCapabilities":  map[string]any{},
					"sessionCapabilities": map[string]any{},
					"_meta":               map[string]any{"toolInventory": test.inventory},
				},
			}
			connection, owner, _, raw, err := establishTestConnection(t, init, map[string]any{"sessionId": "s"}, connectionHandler{})
			defer raw.Close()
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if owner.closeCount.Load() != 0 {
					t.Fatal("accepted inventory closed owner")
				}
				_ = connection.Close()
				return
			}
			if err == nil || !errors.Is(err, ErrIncompatible) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("preflight error = %v, want %q", err, test.want)
			}
			if owner.closeCount.Load() != 1 {
				t.Fatalf("owner close count = %d", owner.closeCount.Load())
			}
		})
	}
}

func TestPreflightRejectsMissingMandatoryCapabilitiesAndContradictoryStartupStatus(t *testing.T) {
	tests := []struct {
		name       string
		initialize map[string]any
		want       string
	}{
		{name: "empty capabilities", initialize: map[string]any{"protocolVersion": acpProtocolVersion, "agentCapabilities": map[string]any{}}, want: "missing promptCapabilities"},
		{name: "missing session lifecycle", initialize: map[string]any{"protocolVersion": acpProtocolVersion, "agentCapabilities": map[string]any{"promptCapabilities": map[string]any{}}}, want: "missing sessionCapabilities"},
		{name: "contradictory startup root", initialize: map[string]any{
			"protocolVersion": acpProtocolVersion,
			"agentCapabilities": map[string]any{
				"promptCapabilities":  map[string]any{},
				"sessionCapabilities": map[string]any{},
			},
			"_meta": map[string]any{"startupRoot": filepath.Join(t.TempDir(), "foreign")},
		}, want: "contradictory startup-root status"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection, owner, _, raw, err := establishTestConnection(t, test.initialize, map[string]any{"sessionId": "s"}, connectionHandler{})
			defer raw.Close()
			if err == nil || !errors.Is(err, ErrIncompatible) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("preflight error = %v, want %q", err, test.want)
			}
			if connection.Err() == nil || owner.closeCount.Load() != 1 {
				t.Fatalf("connection error = %v, closes = %d", connection.Err(), owner.closeCount.Load())
			}
		})
	}
}

func TestConnectionCorrelationViolationsFailClosed(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *Connection, *transport, net.Conn)
		want string
	}{
		{name: "orphan response", want: "orphan response", run: func(t *testing.T, _ *Connection, _ *transport, raw net.Conn) {
			writeRaw(t, raw, `{"jsonrpc":"2.0","id":99,"result":{}}`+"\n")
		}},
		{name: "duplicate terminal response", want: "duplicate response", run: func(t *testing.T, connection *Connection, server *transport, raw net.Conn) {
			callErr := startPrompt(t, connection, server)
			if err := server.sendResult(integerID(3), map[string]string{"stopReason": "end_turn"}); err != nil {
				t.Fatal(err)
			}
			if err := <-callErr; err != nil {
				t.Fatalf("first terminal response failed: %v", err)
			}
			writeRaw(t, raw, `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`+"\n")
		}},
		{name: "foreign session update", want: "foreign or late session/update", run: func(t *testing.T, connection *Connection, server *transport, _ net.Conn) {
			callErr := startPrompt(t, connection, server)
			if err := server.sendNotification("session/update", map[string]any{
				"sessionId": "foreign", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "secret-response"}},
			}); err != nil {
				t.Fatal(err)
			}
			<-callErr
		}},
		{name: "terminal with pending permission", want: "pending permission", run: func(t *testing.T, connection *Connection, server *transport, _ net.Conn) {
			callErr := startPrompt(t, connection, server)
			if err := server.sendRequest(stringID("permission"), "session/request_permission", map[string]any{
				"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tool-1"}, "options": []any{map[string]any{"optionId": "reject", "kind": "reject_once", "name": "Reject"}},
			}); err != nil {
				t.Fatal(err)
			}
			if err := server.sendResult(integerID(3), map[string]string{"stopReason": "end_turn"}); err != nil {
				t.Fatal(err)
			}
			<-callErr
		}},
		{name: "malformed UTF-8", want: "invalid NDJSON", run: func(t *testing.T, _ *Connection, _ *transport, raw net.Conn) {
			if _, err := raw.Write([]byte{0xff, '\n'}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "truncated NDJSON", want: "truncated NDJSON", run: func(t *testing.T, _ *Connection, _ *transport, raw net.Conn) {
			writeRaw(t, raw, `{"jsonrpc":"2.0"}`)
			_ = raw.Close()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var updates atomic.Int32
			handler := connectionHandler{
				sessionUpdate: func(message) error { updates.Add(1); return nil },
				permission:    func(*Connection, message) error { return nil },
			}
			connection, owner, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, handler)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			test.run(t, connection, server, raw)
			waitForDone(t, connection.Done())
			got := connection.Err()
			if !errors.Is(got, ErrConnectionClosed) || !errors.Is(got, ErrProtocol) || !strings.Contains(got.Error(), test.want) {
				t.Fatalf("connection error = %v", got)
			}
			if strings.Contains(got.Error(), "secret-response") {
				t.Fatalf("wire body leaked: %v", got)
			}
			if owner.closeCount.Load() != 1 {
				t.Fatalf("owner close count = %d", owner.closeCount.Load())
			}
			if updates.Load() != 0 {
				t.Fatalf("late/foreign update reached handler %d times", updates.Load())
			}
		})
	}
}

func TestConnectionFailureUnblocksAllPendingCallsWithOneError(t *testing.T) {
	connection, owner, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	errorsOut := make(chan error, 2)
	for range 2 {
		go func() { errorsOut <- connection.call("probe", struct{}{}, nil) }()
	}
	for range 2 {
		if _, err := server.read(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Write([]byte{0xff, '\n'}); err != nil {
		t.Fatal(err)
	}
	first, second := <-errorsOut, <-errorsOut
	if first == nil || second == nil || first.Error() != second.Error() || !errors.Is(first, ErrProtocol) {
		t.Fatalf("pending errors = %v / %v", first, second)
	}
	if owner.closeCount.Load() != 1 {
		t.Fatalf("owner close count = %d", owner.closeCount.Load())
	}
	if err := connection.call("after-close", nil, nil); err == nil || err.Error() != first.Error() {
		t.Fatalf("late call error = %v, want %v", err, first)
	}
}

func TestPromptTerminalBarrierRejectsConcurrentPromptWithoutWireSend(t *testing.T) {
	connection, _, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	defer connection.Close()
	commitEntered := make(chan struct{})
	releaseCommit := make(chan struct{})
	firstErr := make(chan error, 1)
	go func() {
		var result promptResponse
		firstErr <- connection.callAndCommit("session/prompt", map[string]any{
			"sessionId": "s", "prompt": []any{map[string]string{"type": "text", "text": "first"}},
		}, &result, func() error {
			close(commitEntered)
			<-releaseCommit
			return nil
		})
	}()
	first, err := server.read()
	if err != nil || first.method != "session/prompt" {
		t.Fatalf("first prompt = %+v, %v", first, err)
	}
	if err := server.sendResult(first.id, map[string]string{"stopReason": "end_turn"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-commitEntered:
	case <-time.After(time.Second):
		t.Fatal("first prompt did not reach terminal commit barrier")
	}
	var second promptResponse
	err = connection.call("session/prompt", map[string]any{
		"sessionId": "s", "prompt": []any{map[string]string{"type": "text", "text": "second"}},
	}, &second)
	if !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("second prompt error = %v", err)
	}
	if err := raw.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if unexpected, readErr := server.read(); readErr == nil {
		t.Fatalf("second prompt reached wire: %+v", unexpected)
	} else if timeout, ok := readErr.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("checking second prompt wire send: %v", readErr)
	}
	_ = raw.SetReadDeadline(time.Time{})
	close(releaseCommit)
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
}

func TestConnectionRoutesUpdatesPermissionsAndCancelAcknowledgement(t *testing.T) {
	updated := make(chan struct{}, 1)
	permissionHandled := make(chan error, 1)
	handler := connectionHandler{
		sessionUpdate: func(message) error {
			updated <- struct{}{}
			return nil
		},
		permission: func(connection *Connection, message message) error {
			err := connection.respond(message.id, map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": "allow-once"}})
			permissionHandled <- err
			return err
		},
	}
	connection, _, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	callErr := startPrompt(t, connection, server)
	if err := server.sendNotification("session/update", map[string]any{
		"sessionId": "s", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "chunk"}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("session update was not routed")
	}
	if err := server.sendRequest(stringID("permission"), "session/request_permission", map[string]any{
		"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tool-1"}, "options": []any{map[string]any{"optionId": "allow-once", "kind": "allow_once", "name": "Allow once"}},
	}); err != nil {
		t.Fatal(err)
	}
	response, err := server.read()
	if err != nil || response.kind != responseMessage || response.id.key != stringID("permission").key {
		t.Fatalf("permission response = %+v, %v", response, err)
	}
	if err := <-permissionHandled; err != nil {
		t.Fatal(err)
	}
	cancelErr := make(chan error, 1)
	go func() { cancelErr <- connection.sendNotification("session/cancel", sessionParams{SessionID: "s"}) }()
	cancel, err := server.read()
	if err != nil || cancel.kind != notificationMessage || cancel.method != "session/cancel" {
		t.Fatalf("cancel = %+v, %v", cancel, err)
	}
	if err := <-cancelErr; err != nil {
		t.Fatal(err)
	}
	if err := server.sendResult(integerID(3), map[string]string{"stopReason": "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if err := <-callErr; err != nil {
		t.Fatal(err)
	}
	if connection.Err() != nil {
		t.Fatalf("healthy connection error = %v", connection.Err())
	}
	_ = connection.Close()
}

func TestPermissionResponseWriteFormsTerminalBarrier(t *testing.T) {
	responding := make(chan struct{})
	responded := make(chan error, 1)
	handler := connectionHandler{permission: func(connection *Connection, message message) error {
		close(responding)
		err := connection.respond(message.id, map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": "allow-once"}})
		responded <- err
		return err
	}}
	connection, _, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	defer connection.Close()
	promptErr := startPrompt(t, connection, server)
	if err := server.sendRequest(stringID("permission-barrier"), "session/request_permission", map[string]any{
		"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tool-1"}, "options": []any{map[string]any{"optionId": "allow-once", "kind": "allow_once", "name": "Allow once"}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-responding:
	case <-time.After(time.Second):
		t.Fatal("permission handler did not start response")
	}
	terminalSent := make(chan error, 1)
	go func() {
		terminalSent <- server.sendResult(integerID(3), map[string]string{"stopReason": "end_turn"})
	}()
	select {
	case err := <-terminalSent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal was not read while permission response write was blocked")
	}
	select {
	case err := <-promptErr:
		t.Fatalf("terminal crossed pending permission write barrier: %v", err)
	case <-connection.Done():
		t.Fatalf("connection failed during valid permission write: %v", connection.Err())
	case <-time.After(100 * time.Millisecond):
	}
	permissionResponse, err := server.read()
	if err != nil || permissionResponse.kind != responseMessage || permissionResponse.id.key != stringID("permission-barrier").key {
		t.Fatalf("permission response = %+v, %v", permissionResponse, err)
	}
	if err := <-responded; err != nil {
		t.Fatal(err)
	}
	if err := <-promptErr; err != nil {
		t.Fatal(err)
	}
}

func TestSessionUpdateHandlerErrorIsSanitized(t *testing.T) {
	const secret = "credential-body-handler-marker"
	handler := connectionHandler{sessionUpdate: func(message) error { return errors.New(secret) }}
	connection, owner, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	promptErr := startPrompt(t, connection, server)
	if err := server.sendNotification("session/update", map[string]any{
		"sessionId": "s", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": secret}},
	}); err != nil {
		t.Fatal(err)
	}
	<-promptErr
	waitForDone(t, connection.Done())
	got := connection.Err()
	if !errors.Is(got, ErrProtocol) || !strings.Contains(got.Error(), "session/update handler failed") || strings.Contains(got.Error(), secret) {
		t.Fatalf("unsafe handler error = %v", got)
	}
	if owner.closeCount.Load() != 1 {
		t.Fatalf("owner close count = %d", owner.closeCount.Load())
	}
}

func TestConnectionRejectsUnknownTerminalAndContentMessages(t *testing.T) {
	for _, test := range []struct {
		name   string
		update map[string]any
	}{
		{name: "update kind", update: map[string]any{"sessionUpdate": "future_content", "content": map[string]any{"type": "text", "text": "x"}}},
		{name: "content kind", update: map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "future", "text": "x"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection, owner, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{})
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			callErr := startPrompt(t, connection, server)
			if err := server.sendNotification("session/update", map[string]any{"sessionId": "s", "update": test.update}); err != nil {
				t.Fatal(err)
			}
			<-callErr
			waitForDone(t, connection.Done())
			if !errors.Is(connection.Err(), ErrProtocol) || owner.closeCount.Load() != 1 {
				t.Fatalf("connection = %v, closes = %d", connection.Err(), owner.closeCount.Load())
			}
		})
	}
}

type pipeOwner struct {
	connection net.Conn
	closeCount atomic.Int32
	once       sync.Once
}

func (owner *pipeOwner) Close() error {
	var err error
	owner.once.Do(func() {
		owner.closeCount.Add(1)
		err = owner.connection.Close()
	})
	return err
}

func validInitialize() map[string]any {
	return map[string]any{"protocolVersion": acpProtocolVersion, "agentCapabilities": map[string]any{
		"promptCapabilities":  map[string]any{},
		"sessionCapabilities": map[string]any{},
	}}
}

func establishTestConnection(t *testing.T, initializeResult, sessionResult any, handler connectionHandler) (*Connection, *pipeOwner, *transport, net.Conn, error) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	owner := &pipeOwner{connection: clientSide}
	connection := newConnection(newTransport(clientSide, clientSide), owner, handler)
	server := newTransport(serverSide, serverSide)
	serverErr := make(chan error, 1)
	go func() {
		initialize, err := server.read()
		if err != nil {
			serverErr <- err
			return
		}
		var params initializeParams
		if initialize.method != "initialize" || json.Unmarshal(initialize.params, &params) != nil || params.ClientInfo.Name != "stepan" {
			serverErr <- errors.New("invalid initialize request")
			return
		}
		if err := server.sendResult(initialize.id, initializeResult); err != nil {
			serverErr <- err
			return
		}
		if initializeResultMap, ok := initializeResult.(map[string]any); ok && initializeResultMap["agentCapabilities"] == nil {
			serverErr <- nil
			return
		}
		session, err := server.read()
		if err != nil {
			serverErr <- err
			return
		}
		var fields map[string]json.RawMessage
		var sessionParams newSessionParams
		if session.method != "session/new" || json.Unmarshal(session.params, &fields) != nil || json.Unmarshal(session.params, &sessionParams) != nil || sessionParams.CWD == "" {
			serverErr <- errors.New("invalid session/new request")
			return
		}
		if _, exists := fields["additionalDirectories"]; exists {
			serverErr <- errors.New("session/new contained additionalDirectories")
			return
		}
		serverErr <- server.sendResult(session.id, sessionResult)
	}()
	err := connection.preflight(filepath.Clean(t.TempDir()), filepath.Clean(t.TempDir()))
	if err != nil {
		connection.fail(err)
	}
	if peerErr := <-serverErr; peerErr != nil && err == nil {
		connection.fail(protocolError(peerErr))
		err = peerErr
	}
	return connection, owner, server, serverSide, err
}

func startPrompt(t *testing.T, connection *Connection, server *transport) <-chan error {
	t.Helper()
	errorsOut := make(chan error, 1)
	go func() {
		var result promptResponse
		errorsOut <- connection.call("session/prompt", map[string]any{
			"sessionId": "s", "prompt": []any{map[string]string{"type": "text", "text": "do not log me"}},
		}, &result)
	}()
	request, err := server.read()
	if err != nil || request.method != "session/prompt" {
		t.Fatalf("prompt request = %+v, %v", request, err)
	}
	return errorsOut
}

func writeRaw(t *testing.T, writer io.Writer, value string) {
	t.Helper()
	if _, err := io.WriteString(writer, value); err != nil {
		t.Fatal(err)
	}
}

func waitForDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("connection remained healthy")
	}
}

func readJSONEventually(t *testing.T, path string, target any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, target) == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not read %s", path)
}

func TestProtocolDiagnosticsAreBoundedAndSanitized(t *testing.T) {
	secret := strings.Repeat("credential-body-", 10_000)
	message := protocolError(fmt.Errorf("%w: %s", errInvalidEnvelope, secret)).Error()
	if strings.Contains(message, "credential-body") || len(message) > 256 {
		t.Fatalf("unsafe protocol diagnostic: len=%d %q", len(message), message)
	}
}
