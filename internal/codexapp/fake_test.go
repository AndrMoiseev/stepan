package codexapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(main *testing.M) {
	if scenario := os.Getenv("GO_WANT_CODEXAPP_FAKE"); scenario != "" {
		os.Exit(runFake(scenario))
	}
	os.Exit(main.Run())
}

func runFake(scenario string) int {
	switch scenario {
	case "correlation":
		transport := NewTransport(os.Stdin, os.Stdout)
		first, err := transport.Read()
		if err != nil {
			return 10
		}
		second, err := transport.Read()
		if err != nil {
			return 11
		}
		if err := transport.SendNotification("future/notification", map[string]bool{"preserved": true}); err != nil {
			return 12
		}
		if err := transport.SendResult(second.ID, map[string]string{"echo": second.ID.Key()}); err != nil {
			return 13
		}
		if err := transport.SendResult(first.ID, map[string]string{"echo": first.ID.Key()}); err != nil {
			return 14
		}
		return 0
	case "invalid":
		fmt.Fprintln(os.Stdout, `{"id":`)
		return 0
	case "truncated":
		fmt.Fprint(os.Stdout, `{"id":1`)
		return 0
	case "oversized":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'x'}, MaxMessageBytes+1))
		fmt.Fprintln(os.Stdout)
		return 0
	case "vertical", "resume", "structured-extra", "structured-missing", "structured-wrapped", "config-denied", "approvals":
		return runVerticalFake(scenario)
	case "no-terminal":
		return 0
	case "nonzero":
		return 7
	default:
		return 9
	}
}

func runVerticalFake(scenario string) int {
	wantArgs := []string{"app-server", "--stdio", "--strict-config", "-c", `approvals_reviewer="user"`}
	if len(os.Args) != len(wantArgs)+1 {
		return 20
	}
	for index, want := range wantArgs {
		if os.Args[index+1] != want {
			return 21
		}
	}
	transport := NewTransport(os.Stdin, os.Stdout)
	initialize, err := transport.Read()
	if err != nil || initialize.Kind != Request || initialize.Method != "initialize" || initialize.ID.Key() != IntID(1).Key() {
		return 22
	}
	var initializeParams struct {
		ClientInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	var initializeFields map[string]json.RawMessage
	if json.Unmarshal(initialize.Params, &initializeParams) != nil || json.Unmarshal(initialize.Params, &initializeFields) != nil || initializeParams.ClientInfo.Name != "stepan" || initializeParams.ClientInfo.Version == "" {
		return 23
	}
	if _, hasCapabilities := initializeFields["capabilities"]; hasCapabilities {
		return 23
	}
	if err := transport.SendResult(initialize.ID, map[string]string{
		"codexHome": filepath.Join(os.TempDir(), "fake-codex-home"), "platformFamily": "windows", "platformOs": "windows", "userAgent": "codex_cli_rs/0.147.0",
	}); err != nil {
		return 24
	}
	initialized, err := transport.Read()
	if err != nil || initialized.Kind != Notification || initialized.Method != "initialized" {
		return 25
	}
	requirements, err := transport.Read()
	if err != nil || requirements.Kind != Request || requirements.Method != "configRequirements/read" || requirements.ID.Key() != IntID(2).Key() || string(requirements.Params) != "null" {
		return 26
	}
	if scenario == "config-denied" {
		if err := transport.SendResult(requirements.ID, map[string]any{"requirements": map[string]any{"allowedApprovalPolicies": []string{"never"}}}); err != nil {
			return 27
		}
		return 0
	}
	if err := transport.SendResult(requirements.ID, map[string]any{"requirements": nil}); err != nil {
		return 28
	}
	threadRequest, err := transport.Read()
	if err != nil || threadRequest.Kind != Request || threadRequest.ID.Key() != IntID(3).Key() {
		return 29
	}
	threadID := "thread-1"
	if scenario == "resume" {
		if threadRequest.Method != "thread/resume" {
			return 30
		}
		var params struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(threadRequest.Params, &params) != nil || params.ThreadID != "thread-resume" {
			return 31
		}
		threadID = params.ThreadID
	} else if threadRequest.Method != "thread/start" {
		return 32
	}
	thread := map[string]any{
		"id": threadID, "sessionId": "session-1", "source": "appServer", "modelProvider": "fake", "createdAt": 1, "updatedAt": 1,
		"status": map[string]string{"type": "idle"}, "path": nil, "cwd": filepath.Dir(os.Args[0]), "cliVersion": "0.147.0", "preview": "", "turns": []any{}, "ephemeral": false,
	}
	if err := transport.SendResult(threadRequest.ID, map[string]any{
		"thread": thread, "model": "fake", "modelProvider": "fake", "cwd": filepath.Dir(os.Args[0]), "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": "read-only",
	}); err != nil {
		return 33
	}
	turnRequest, err := transport.Read()
	if err != nil || turnRequest.Kind != Request || turnRequest.Method != "turn/start" || turnRequest.ID.Key() != IntID(4).Key() {
		return 34
	}
	var turnParams struct {
		ThreadID      string           `json:"threadId"`
		Input         []map[string]any `json:"input"`
		CWD           string           `json:"cwd"`
		Approval      string           `json:"approvalPolicy"`
		SandboxPolicy struct {
			Type          string `json:"type"`
			NetworkAccess bool   `json:"networkAccess"`
		} `json:"sandboxPolicy"`
		OutputSchema map[string]any `json:"outputSchema"`
	}
	if json.Unmarshal(turnRequest.Params, &turnParams) != nil || turnParams.ThreadID != threadID || turnParams.CWD == "" || turnParams.Approval != "on-request" || turnParams.SandboxPolicy.Type != "readOnly" || turnParams.SandboxPolicy.NetworkAccess || len(turnParams.Input) != 1 || turnParams.OutputSchema["type"] != "object" {
		return 35
	}
	if err := transport.SendResult(turnRequest.ID, map[string]any{"turn": map[string]any{"id": "turn-1", "items": []any{}, "status": "inProgress"}}); err != nil {
		return 36
	}
	if scenario == "approvals" {
		if code := runApprovalFake(transport, threadID, turnParams.CWD); code != 0 {
			return code
		}
	}
	_ = transport.SendNotification("future/notification", map[string]bool{"preserved": true})
	text := `{"result":"ok","nonce":"nonce"}`
	if scenario == "structured-extra" {
		text = `{"result":"ok","nonce":"nonce","extra":true}`
	} else if scenario == "structured-missing" {
		text = `{"result":"ok"}`
	} else if scenario == "structured-wrapped" {
		text = `answer: {"result":"ok","nonce":"nonce"}`
	}
	phase := "final_answer"
	terminal := map[string]any{
		"threadId": threadID,
		"turn":     map[string]any{"id": "turn-1", "status": "completed", "items": []any{map[string]any{"id": "item-1", "type": "agentMessage", "phase": phase, "text": text}}},
	}
	if err := transport.SendNotification("turn/completed", terminal); err != nil {
		return 37
	}
	fmt.Fprintln(os.Stderr, "warning")
	return 0
}

func runApprovalFake(transport *Transport, threadID, cwd string) int {
	base := map[string]any{"threadId": threadID, "turnId": "turn-1", "itemId": "command-item", "startedAtMs": 1, "command": "go test ./...", "cwd": cwd}
	if err := transport.SendRequest(StringID("opaque-command"), "item/commandExecution/requestApproval", base); err != nil {
		return 40
	}
	response, err := transport.Read()
	if err != nil || response.Kind != Response || response.ID.Key() != StringID("opaque-command").Key() || !resultHasDecision(response.Result, DecisionAccept) {
		return 41
	}

	if err := transport.SendNotification("item/started", map[string]any{
		"threadId": threadID, "turnId": "turn-1", "startedAtMs": 2,
		"item": map[string]any{"id": "file-item", "type": "fileChange", "status": "inProgress", "changes": []map[string]string{{"path": filepath.Join("public", "allowed.txt"), "kind": "add", "diff": "SECRET-FILE-CONTENT"}}},
	}); err != nil {
		return 42
	}
	file := map[string]any{"threadId": threadID, "turnId": "turn-1", "itemId": "file-item", "startedAtMs": 2, "reason": "do not persist SECRET-REASON"}
	if err := transport.SendRequest(StringID("opaque-file"), "item/fileChange/requestApproval", file); err != nil {
		return 43
	}
	if err := transport.SendNotification("future/while-awaiting-operator", map[string]string{"padding": strings.Repeat("x", 1<<20)}); err != nil {
		return 44
	}
	response, err = transport.Read()
	if err != nil || response.ID.Key() != StringID("opaque-file").Key() || !resultHasDecision(response.Result, DecisionAccept) {
		return 45
	}

	permissions := map[string]any{
		"threadId": threadID, "turnId": "turn-1", "itemId": "permission-item", "startedAtMs": 3, "cwd": cwd,
		"permissions": map[string]any{"fileSystem": map[string]any{"write": []string{filepath.Join(cwd, "public", "new.txt")}}},
	}
	if err := transport.SendRequest(IntID(99), "item/permissions/requestApproval", permissions); err != nil {
		return 46
	}
	response, err = transport.Read()
	if err != nil || response.ID.Key() != IntID(99).Key() || !bytes.Contains(response.Result, []byte(`"scope":"turn"`)) || !bytes.Contains(response.Result, []byte(`new.txt`)) {
		return 47
	}

	permissions["itemId"] = "protected-item"
	permissions["permissions"] = map[string]any{"fileSystem": map[string]any{"read": []string{filepath.Join(cwd, ".git", "config")}}, "network": map[string]bool{"enabled": true}}
	if err := transport.SendRequest(StringID("opaque-denied"), "item/permissions/requestApproval", permissions); err != nil {
		return 48
	}
	response, err = transport.Read()
	if err != nil || response.ID.Key() != StringID("opaque-denied").Key() || bytes.Contains(response.Result, []byte(`config`)) || !bytes.Contains(response.Result, []byte(`"permissions":{}`)) {
		return 49
	}
	return 0
}

func resultHasDecision(raw json.RawMessage, decision ApprovalDecision) bool {
	var result struct {
		Decision ApprovalDecision `json:"decision"`
	}
	return json.Unmarshal(raw, &result) == nil && result.Decision == decision
}
