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

	"github.com/AndrMoiseev/stepan/internal/agentruntime/conformance"
)

type acpObservation struct {
	Args                     []string `json:"args"`
	Prompts                  []string `json:"prompts"`
	ClientName               string   `json:"clientName"`
	ClientTitle              string   `json:"clientTitle"`
	ProtocolVersion          int      `json:"protocolVersion"`
	CWD                      string   `json:"cwd"`
	MCPServerCount           int      `json:"mcpServerCount"`
	HasAdditionalDirectories bool     `json:"hasAdditionalDirectories"`
	CanReadTextFile          bool     `json:"canReadTextFile"`
	CanWriteTextFile         bool     `json:"canWriteTextFile"`
	ModelPromptSeen          bool     `json:"modelPromptSeen"`
	PID                      int      `json:"pid"`
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
		Args:       append([]string(nil), os.Args[1:]...),
		ClientName: initialized.ClientInfo.Name, ClientTitle: initialized.ClientInfo.Title,
		ProtocolVersion:  initialized.ProtocolVersion,
		CanReadTextFile:  initialized.ClientCapabilities.FS.ReadTextFile,
		CanWriteTextFile: initialized.ClientCapabilities.FS.WriteTextFile,
		PID:              os.Getpid(),
	}
	capabilities := any(map[string]any{
		"promptCapabilities":  map[string]any{},
		"sessionCapabilities": map[string]any{},
	})
	scenario := os.Getenv("STEPAN_QWEN_ACP_CASE")
	var sharedScript conformance.Script
	if scenario == "conformance" {
		data, readErr := os.ReadFile(os.Getenv("STEPAN_QWEN_CONFORMANCE_SCRIPT"))
		if readErr != nil || json.Unmarshal(data, &sharedScript) != nil || len(sharedScript) == 0 {
			return 60
		}
	}
	if scenario == "missing-capability" {
		capabilities = nil
	} else if scenario == "empty-capabilities" {
		capabilities = map[string]any{}
	}
	if scenario == "hang-initialize" {
		_ = writeACPObservation(observation)
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
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
	if scenario == "hang-session" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	}
	if scenario == "session-error" || scenario == "session-auth-error" {
		code, message := int64(-32602), "credential-body startup root refused"
		if scenario == "session-auth-error" {
			code, message = -32000, "Authentication required: credential-body-do-not-echo"
		}
		if err := transport.sendError(session.id, code, message); err != nil {
			return 68
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	}
	sessionID := "session-one"
	if scenario == "conformance" {
		sessionID = fmt.Sprintf("session-%d", os.Getpid())
	}
	if err := transport.sendResult(session.id, map[string]any{"sessionId": sessionID}); err != nil {
		return 67
	}
	promptIndex := 0
	var conformanceCandidate string
	conformanceRole := ""
	conformanceTurn := 0
	for {
		message, err := transport.read()
		if err != nil {
			return 0
		}
		if message.method == "session/prompt" {
			observation.ModelPromptSeen = true
			var prompt sessionPromptParams
			if json.Unmarshal(message.params, &prompt) != nil || prompt.SessionID != sessionID || len(prompt.Prompt) != 1 {
				return 69
			}
			observation.Prompts = append(observation.Prompts, prompt.Prompt[0].Text)
			_ = writeACPObservation(observation)
			if scenario == "structured-turn" {
				responses := []string{"invalid process response", `{"answer":"repaired"}`, `{"answer":"next"}`}
				if promptIndex >= len(responses) {
					return 70
				}
				candidate := responses[promptIndex]
				promptIndex++
				if err := transport.sendNotification("session/update", map[string]any{
					"sessionId": "session-one",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk", "messageId": fmt.Sprintf("answer-%d", promptIndex),
						"content": map[string]any{"type": "text", "text": candidate},
					},
				}); err != nil {
					return 71
				}
				if err := transport.sendResult(message.id, map[string]string{"stopReason": "end_turn"}); err != nil {
					return 72
				}
			} else if scenario == "conformance" {
				text := prompt.Prompt[0].Text
				repair := strings.Contains(text, "Your previous response could not be accepted")
				if !repair {
					if conformanceRole == "" {
						conformanceRole = conformanceRoleFromPrompt(text)
					}
					conformanceTurn++
				}
				candidate, ok := conformanceResponse(sharedScript, text, conformanceRole, conformanceTurn, conformanceCandidate, conformanceArtifactRoot(os.Args[1:]))
				if !ok {
					return 73
				}
				conformanceCandidate = candidate
				if err := transport.sendNotification("session/update", map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk", "messageId": fmt.Sprintf("answer-%d", promptIndex),
						"content": map[string]any{"type": "text", "text": candidate},
					},
				}); err != nil {
					return 74
				}
				if err := transport.sendResult(message.id, map[string]string{"stopReason": "end_turn"}); err != nil {
					return 75
				}
			}
		}
	}
}

func conformanceResponse(script conformance.Script, prompt, role string, turn int, previous, artifactRoot string) (string, bool) {
	if strings.Contains(prompt, "Your previous response could not be accepted") {
		return previous, previous != ""
	}
	for _, step := range script {
		promptMatch := step.Prompt != "" && (strings.HasSuffix(prompt, "User request:\n"+step.Prompt) || prompt == step.Prompt)
		roleMatch := step.Role != "" && step.Role == role && step.Turn == turn
		if !promptMatch && !roleMatch {
			continue
		}
		if step.ArtifactName != "" {
			if artifactRoot == "" || os.WriteFile(filepath.Join(artifactRoot, step.ArtifactName), []byte(step.ArtifactContent), 0o600) != nil {
				return "", false
			}
		}
		return string(step.Output), true
	}
	return "", false
}

func conformanceRoleFromPrompt(prompt string) string {
	if strings.Contains(prompt, `"feature_id"`) {
		return "feature-id"
	}
	for _, role := range []string{"intent-author", "spec-author", "spec-reviewer", "plan-author", "plan-reviewer"} {
		if strings.Contains(prompt, "[roles/"+role+"]") {
			return role
		}
	}
	return "unknown"
}

func conformanceArtifactRoot(args []string) string {
	for index, argument := range args {
		if argument == "--include-directories" && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func TestConformanceResponseUsesSuppliedNamedScript(t *testing.T) {
	script := conformance.Script{{
		Name: "caller-owned outcome", Prompt: "unique caller prompt",
		Output: json.RawMessage(`{"kind":"message","message":"caller-owned response","decisions":[]}`),
	}}
	got, ok := conformanceResponse(script, "unique caller prompt", "unknown", 1, "", "")
	if !ok || got != string(script[0].Output) {
		t.Fatalf("scripted response = %q, %t; want supplied %s", got, ok, script[0].Output)
	}
	if _, ok := conformanceResponse(script, "hardcoded legacy prompt", "unknown", 1, "", ""); ok {
		t.Fatal("fake accepted an outcome absent from the supplied script")
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
	process := NewProcess(Config{Executable: testExecutableName(t), Workspace: workspace, JSONContract: testJSONContract}, t.TempDir())
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
	if !observation.CanReadTextFile || observation.CanWriteTextFile {
		t.Fatalf("client filesystem capabilities = %+v", observation)
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
			process := NewProcess(Config{Executable: testExecutableName(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
			if err := process.Start(); err != nil {
				t.Fatal(err)
			}
			connection, err := OpenConnection(process)
			if connection != nil || !errors.Is(err, ErrIncompatible) {
				t.Fatalf("OpenConnection = %v, %v", connection, err)
			}
			assertProcessReaped(t, process.command, "incompatible ACP child was not closed and reaped")
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
	process := NewProcess(Config{Executable: testExecutableName(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
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
	assertProcessReaped(t, process.command, "startup-contract failure did not close and reap child")
}

func TestOpenConnectionClosesProcessOnSessionContractFailure(t *testing.T) {
	t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
	t.Setenv("STEPAN_QWEN_ACP_CASE", "session-error")
	t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", filepath.Join(t.TempDir(), "observation.json"))
	process := NewProcess(Config{Executable: testExecutableName(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
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
	assertProcessReaped(t, process.command, "incompatible ACP child was not closed and reaped")
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
				audit := connection.auditSnapshot()
				if !audit.PreflightInventoryPresent || !equalStrings(audit.PreflightInventory, allowedToolNames[:]) {
					t.Fatalf("recorded preflight inventory = %#v, present=%t", audit.PreflightInventory, audit.PreflightInventoryPresent)
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

func TestPreflightRejectsOversizedSessionIdentity(t *testing.T) {
	connection, owner, _, raw, err := establishTestConnection(t, validInitialize(), map[string]any{
		"sessionId": strings.Repeat("s", maxCorrelationIDBytes+1),
	}, connectionHandler{})
	defer raw.Close()
	if err == nil || !errors.Is(err, ErrIncompatible) || !strings.Contains(err.Error(), "session/new sessionId") {
		t.Fatalf("oversized session identity = %v", err)
	}
	if connection.Err() == nil || owner.closeCount.Load() != 1 {
		t.Fatalf("oversized session cleanup error=%v closes=%d", connection.Err(), owner.closeCount.Load())
	}
}

func TestConnectionAcceptsInformationalSessionUpdateBetweenSessionAndFirstPrompt(t *testing.T) {
	observed := make(chan struct{}, 1)
	connection, _, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{
		sessionUpdate: func(message) error {
			observed <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := server.sendNotification("session/update", map[string]any{
		"sessionId": "s",
		"update": map[string]any{
			"sessionUpdate":     "available_commands_update",
			"availableCommands": []any{},
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	case <-connection.Done():
		t.Fatalf("informational update closed connection: %v", connection.Err())
	case <-time.After(3 * time.Second):
		t.Fatal("informational update was not dispatched")
	}
	callErr := startPrompt(t, connection, server)
	if err := server.sendResult(integerID(3), map[string]string{"stopReason": "end_turn"}); err != nil {
		t.Fatal(err)
	}
	if err := <-callErr; err != nil {
		t.Fatalf("first prompt after informational update failed: %v", err)
	}
}

func TestConnectionRejectsTurnContentBetweenSessionAndFirstPrompt(t *testing.T) {
	connection, owner, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := server.sendNotification("session/update", map[string]any{
		"sessionId": "s",
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "must not be accepted"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	waitForDone(t, connection.Done())
	if err := connection.Err(); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "foreign or late session/update") {
		t.Fatalf("out-of-turn content error = %v", err)
	}
	if owner.closeCount.Load() != 1 {
		t.Fatalf("owner close count = %d", owner.closeCount.Load())
	}
}

func TestConnectionProtocolFailureReportsSafeUpdateCategory(t *testing.T) {
	const secret = "credential-body-do-not-echo"
	connection, _, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	callErr := startPrompt(t, connection, server)
	if err := server.sendNotification("session/update", map[string]any{
		"sessionId": "s",
		"update": map[string]any{
			"sessionUpdate": secret,
		},
	}); err != nil {
		t.Fatal(err)
	}
	<-callErr
	diagnostic := safeRuntimeError("run turn", connection.Err()).Error()
	if !strings.Contains(diagnostic, "session/update kind") {
		t.Fatalf("protocol diagnostic = %q", diagnostic)
	}
	if strings.Contains(diagnostic, secret) {
		t.Fatalf("protocol diagnostic leaked provider data: %q", diagnostic)
	}
}

func TestConnectionCorrelationViolationsFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		run     func(*testing.T, *Connection, *transport, net.Conn)
		want    string
		context diagnosticContext
	}{
		{name: "orphan response", want: "orphan response", context: diagnosticJSONRPCCorrelation, run: func(t *testing.T, _ *Connection, _ *transport, raw net.Conn) {
			writeRaw(t, raw, `{"jsonrpc":"2.0","id":99,"result":{}}`+"\n")
		}},
		{name: "duplicate terminal response", want: "duplicate response", context: diagnosticJSONRPCCorrelation, run: func(t *testing.T, connection *Connection, server *transport, raw net.Conn) {
			callErr := startPrompt(t, connection, server)
			if err := server.sendResult(integerID(3), map[string]string{"stopReason": "end_turn"}); err != nil {
				t.Fatal(err)
			}
			if err := <-callErr; err != nil {
				t.Fatalf("first terminal response failed: %v", err)
			}
			writeRaw(t, raw, `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`+"\n")
		}},
		{name: "foreign session update", want: "foreign or late session/update", context: diagnosticSessionUpdateLifecycle, run: func(t *testing.T, connection *Connection, server *transport, _ net.Conn) {
			callErr := startPrompt(t, connection, server)
			if err := server.sendNotification("session/update", map[string]any{
				"sessionId": "foreign", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "secret-response"}},
			}); err != nil {
				t.Fatal(err)
			}
			<-callErr
		}},
		{name: "terminal with pending permission", want: "pending inbound request", context: diagnosticAgentRequest, run: func(t *testing.T, connection *Connection, server *transport, _ net.Conn) {
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
		{name: "malformed UTF-8", want: "invalid NDJSON", context: diagnosticACPTransport, run: func(t *testing.T, _ *Connection, _ *transport, raw net.Conn) {
			if _, err := raw.Write([]byte{0xff, '\n'}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "truncated NDJSON", want: "truncated NDJSON", context: diagnosticACPTransport, run: func(t *testing.T, _ *Connection, _ *transport, raw net.Conn) {
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
			diagnostic := safeRuntimeError("connection", got).Error()
			if !strings.Contains(diagnostic, test.context.String()) || strings.Contains(diagnostic, "secret-response") {
				t.Fatalf("safe diagnostic = %q", diagnostic)
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
	if diagnostic := safeRuntimeError("connection", got).Error(); !strings.Contains(diagnostic, "session/update handler") {
		t.Fatalf("handler diagnostic = %q", diagnostic)
	}
	if owner.closeCount.Load() != 1 {
		t.Fatalf("owner close count = %d", owner.closeCount.Load())
	}
}

func TestConnectionClassifiesMalformedSessionUpdatePayloadShape(t *testing.T) {
	tests := []struct {
		name   string
		update any
		want   string
	}{
		{name: "non-object", update: []any{}, want: "session/update payload shape"},
		{name: "missing discriminator", update: map[string]any{"content": map[string]any{"type": "text"}}, want: "session/update discriminator missing"},
		{name: "non-string discriminator", update: map[string]any{"sessionUpdate": map[string]any{}}, want: "session/update discriminator type"},
		{name: "empty discriminator", update: map[string]any{"sessionUpdate": ""}, want: "session/update discriminator empty"},
		{name: "event envelope", update: map[string]any{"type": "session_update", "data": map[string]any{}}, want: "session/update event envelope"},
		{name: "snake-case discriminator", update: map[string]any{"session_update": "agent_message_chunk"}, want: "session/update snake_case discriminator"},
		{name: "type discriminator", update: map[string]any{"type": "agent_message_chunk"}, want: "session/update type discriminator"},
		{name: "nested update", update: map[string]any{"update": map[string]any{}}, want: "session/update nested update"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection, _, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{})
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			callErr := startPrompt(t, connection, server)
			if err := server.sendNotification("session/update", map[string]any{"sessionId": "s", "update": test.update}); err != nil {
				t.Fatal(err)
			}
			<-callErr
			diagnostic := safeRuntimeError("connection", connection.Err()).Error()
			if !strings.Contains(diagnostic, test.want) {
				t.Fatalf("payload diagnostic = %q, want %q", diagnostic, test.want)
			}
		})
	}
}

func TestConnectionClassifiesAgentRequestFailure(t *testing.T) {
	const secret = "credential-body-do-not-echo"
	tests := []struct {
		name   string
		method string
		params any
		want   string
	}{
		{name: "permission envelope", method: "session/request_permission", params: map[string]any{}, want: "permission request envelope"},
		{name: "permission tool call", method: "session/request_permission", params: map[string]any{"sessionId": "s", "toolCall": map[string]any{}, "options": []any{}}, want: "permission request toolCall"},
		{name: "permission lifecycle", method: "session/request_permission", params: map[string]any{"sessionId": "foreign", "toolCall": map[string]any{"toolCallId": "tool"}, "options": []any{}}, want: "permission request lifecycle"},
		{name: "read envelope", method: "fs/read_text_file", params: map[string]any{}, want: "fs/read_text_file request envelope"},
		{name: "read lifecycle", method: "fs/read_text_file", params: map[string]any{"sessionId": "foreign", "path": secret}, want: "fs/read_text_file request lifecycle"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection, _, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{})
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			callErr := startPrompt(t, connection, server)
			if err := server.sendRequest(stringID("request"), test.method, test.params); err != nil {
				t.Fatal(err)
			}
			<-callErr
			diagnostic := safeRuntimeError("connection", connection.Err()).Error()
			if !strings.Contains(diagnostic, test.want) {
				t.Fatalf("agent request diagnostic = %q, want %q", diagnostic, test.want)
			}
			if strings.Contains(diagnostic, secret) {
				t.Fatalf("agent request diagnostic leaked provider data: %q", diagnostic)
			}
		})
	}
}

func TestConnectionRejectsUnsupportedAgentRequestWithoutClosing(t *testing.T) {
	const secret = "credential-body-do-not-echo"
	connection, owner, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	promptErr := startPrompt(t, connection, server)
	if err := server.sendRequest(stringID("unsupported-request"), "workspace/read", map[string]any{"content": secret}); err != nil {
		t.Fatal(err)
	}
	response, err := server.read()
	if err != nil {
		t.Fatal(err)
	}
	if response.kind != responseMessage || response.id.key != stringID("unsupported-request").key || response.err == nil {
		t.Fatalf("unsupported request response = %+v", response)
	}
	if response.err.Code != -32601 || response.err.Message != "Method not found" || strings.Contains(response.err.Message, secret) {
		t.Fatalf("unsupported request error = %+v", response.err)
	}
	if err := server.sendResult(integerID(3), map[string]string{"stopReason": "end_turn"}); err != nil {
		t.Fatal(err)
	}
	if err := <-promptErr; err != nil {
		t.Fatalf("prompt failed after unsupported request: %v", err)
	}
	if connection.Err() != nil || owner.closeCount.Load() != 0 {
		t.Fatalf("connection = %v, closes = %d", connection.Err(), owner.closeCount.Load())
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
