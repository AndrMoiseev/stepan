package qwenapp

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanonicalTargetResolvesExistingAndNewTargetsWithoutLinkEscape(t *testing.T) {
	cwd := t.TempDir()
	root := filepath.Join(t.TempDir(), "artifact")
	outside := filepath.Join(t.TempDir(), "outside")
	for _, directory := range []string{root, outside, filepath.Join(root, "nested")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	existing := filepath.Join(root, "nested", "existing.md")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, supplied := range []string{
		existing,
		filepath.Join(root, "nested", "new", "document.md"),
	} {
		target, err := canonicalTargetWithin(cwd, root, supplied)
		if err != nil || !pathWithin(root, target) {
			t.Fatalf("canonical target %q = %q, %v", supplied, target, err)
		}
	}

	link := filepath.Join(root, "linked")
	if err := makeDirectoryLink(link, outside); err != nil {
		t.Skipf("directory link fixture unavailable: %v", err)
	}
	for _, supplied := range []string{
		filepath.Join(link, "new.md"),
		filepath.Join(link, "missing", "new.md"),
	} {
		if _, err := canonicalTargetWithin(cwd, root, supplied); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("link escape %q error = %v", supplied, err)
		}
	}
}

func TestPermissionContextClonesLogicalRoots(t *testing.T) {
	policy := newFilePolicy(`C:\workspace`, `C:\artifact`)
	context := policy.context("session", "turn")
	context.readRoots[0] = "changed"
	if policy.readRoots[0] != `C:\workspace` {
		t.Fatalf("permission context aliased policy roots: %#v", policy.readRoots)
	}
	if context.sessionID != "session" || context.turnID != "turn" || context.writableRoot != `C:\artifact` {
		t.Fatalf("permission context = %#v", context)
	}
	fresh := policy.context("session", "next-turn")
	if len(fresh.readRoots) != 2 || fresh.readRoots[0] != `C:\workspace` || fresh.readRoots[1] != `C:\artifact` {
		t.Fatalf("logical roots = %#v", fresh.readRoots)
	}
	readOnly := newFilePolicy(`C:\workspace`, "").context("session", "read-only")
	if readOnly.writableRoot != "" || len(readOnly.readRoots) != 1 || readOnly.readRoots[0] != `C:\workspace` {
		t.Fatalf("read-only context = %#v", readOnly)
	}
}

func TestPermissionMediationAllowsDistinctWritesAndRejectsInvalidGrants(t *testing.T) {
	workspace := t.TempDir()
	artifact := filepath.Join(t.TempDir(), "artifact")
	sibling := filepath.Join(t.TempDir(), "sibling")
	outside := filepath.Join(t.TempDir(), "outside")
	for _, directory := range []string{artifact, sibling, outside} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	existing := filepath.Join(artifact, "existing.md")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(artifact, "escape")
	if err := makeDirectoryLink(link, outside); err != nil {
		t.Skipf("directory link fixture unavailable: %v", err)
	}

	connection, server, raw := establishPolicyConnection(t, workspace, artifact, connectionHandler{})
	defer raw.Close()
	defer connection.Close()
	promptErr := startPrompt(t, connection, server)

	first := filepath.Join(artifact, "new", "first.md")
	second := existing
	announceTool(t, server, "tool-write", "write_file", map[string]any{"file_path": first}, first)
	announceTool(t, server, "tool-edit", "edit", map[string]any{"file_path": second}, second)
	if outcome := requestPermission(t, server, "permission-write", "tool-write", "write_file", map[string]any{"file_path": first}, first, standardPermissionOptions()); outcome != "selected:allow-once" {
		t.Fatalf("first write outcome = %q", outcome)
	}
	if outcome := requestPermission(t, server, "permission-edit", "tool-edit", "edit", map[string]any{"file_path": second}, second, standardPermissionOptions()); outcome != "selected:allow-once" {
		t.Fatalf("second write outcome = %q", outcome)
	}
	relativeTarget := filepath.Join(artifact, "relative.md")
	relativePath, err := filepath.Rel(workspace, relativeTarget)
	if err != nil {
		t.Fatal(err)
	}
	announceTool(t, server, "tool-relative", "write_file", map[string]any{"file_path": relativePath}, relativeTarget)
	if outcome := requestPermission(t, server, "permission-relative", "tool-relative", "write_file", map[string]any{"file_path": relativePath}, relativeTarget, standardPermissionOptions()); outcome != "selected:allow-once" {
		t.Fatalf("relative write outcome = %q", outcome)
	}
	if outcome := requestPermission(t, server, "permission-duplicate", "tool-write", "write_file", map[string]any{"file_path": first}, first, standardPermissionOptions()); outcome != "cancelled" {
		t.Fatalf("duplicate write outcome = %q", outcome)
	}

	tests := []struct {
		name          string
		toolID        string
		announcedName string
		requestName   string
		announced     map[string]any
		requested     map[string]any
		location      string
		options       []map[string]string
		requestKind   string
		requestStatus string
	}{
		{name: "workspace", toolID: "workspace", announcedName: "write_file", requestName: "write_file", announced: map[string]any{"file_path": filepath.Join(workspace, "source.go")}, requested: map[string]any{"file_path": filepath.Join(workspace, "source.go")}, location: filepath.Join(workspace, "source.go")},
		{name: "sibling", toolID: "sibling", announcedName: "write_file", requestName: "write_file", announced: map[string]any{"file_path": filepath.Join(sibling, "other.md")}, requested: map[string]any{"file_path": filepath.Join(sibling, "other.md")}, location: filepath.Join(sibling, "other.md")},
		{name: "outside", toolID: "outside", announcedName: "edit", requestName: "edit", announced: map[string]any{"file_path": filepath.Join(outside, "other.md")}, requested: map[string]any{"file_path": filepath.Join(outside, "other.md")}, location: filepath.Join(outside, "other.md")},
		{name: "link escape", toolID: "link", announcedName: "write_file", requestName: "write_file", announced: map[string]any{"file_path": filepath.Join(link, "new.md")}, requested: map[string]any{"file_path": filepath.Join(link, "new.md")}, location: filepath.Join(link, "new.md")},
		{name: "ambiguous path", toolID: "ambiguous", announcedName: "write_file", requestName: "write_file", announced: map[string]any{"file_path": filepath.Join(artifact, "a.md"), "path": filepath.Join(artifact, "b.md")}, requested: map[string]any{"file_path": filepath.Join(artifact, "a.md"), "path": filepath.Join(artifact, "b.md")}, location: filepath.Join(artifact, "a.md")},
		{name: "path mismatch", toolID: "mismatch", announcedName: "write_file", requestName: "write_file", announced: map[string]any{"file_path": filepath.Join(artifact, "a.md")}, requested: map[string]any{"file_path": filepath.Join(artifact, "b.md")}, location: filepath.Join(artifact, "b.md")},
		{name: "tool mismatch", toolID: "tool-mismatch", announcedName: "write_file", requestName: "edit", announced: map[string]any{"file_path": filepath.Join(artifact, "a.md")}, requested: map[string]any{"file_path": filepath.Join(artifact, "a.md")}, location: filepath.Join(artifact, "a.md")},
		{name: "persistent only", toolID: "persistent", announcedName: "write_file", requestName: "write_file", announced: map[string]any{"file_path": filepath.Join(artifact, "persistent.md")}, requested: map[string]any{"file_path": filepath.Join(artifact, "persistent.md")}, location: filepath.Join(artifact, "persistent.md"), options: []map[string]string{{"optionId": "always", "kind": "allow_always", "name": "Always"}}},
		{name: "duplicate allow once", toolID: "grant-ambiguous", announcedName: "edit", requestName: "edit", announced: map[string]any{"file_path": filepath.Join(artifact, "grant.md")}, requested: map[string]any{"file_path": filepath.Join(artifact, "grant.md")}, location: filepath.Join(artifact, "grant.md"), options: []map[string]string{{"optionId": "one", "kind": "allow_once", "name": "One"}, {"optionId": "two", "kind": "allow_once", "name": "Two"}}},
		{name: "wrong kind", toolID: "kind", announcedName: "write_file", requestName: "write_file", announced: map[string]any{"file_path": filepath.Join(artifact, "kind.md")}, requested: map[string]any{"file_path": filepath.Join(artifact, "kind.md")}, location: filepath.Join(artifact, "kind.md"), requestKind: "execute"},
		{name: "wrong status", toolID: "status", announcedName: "write_file", requestName: "write_file", announced: map[string]any{"file_path": filepath.Join(artifact, "status.md")}, requested: map[string]any{"file_path": filepath.Join(artifact, "status.md")}, location: filepath.Join(artifact, "status.md"), requestStatus: "completed"},
		{name: "empty path", toolID: "empty", announcedName: "write_file", requestName: "write_file", announced: map[string]any{"file_path": ""}, requested: map[string]any{"file_path": ""}},
		{name: "non-string path", toolID: "non-string", announcedName: "edit", requestName: "edit", announced: map[string]any{"file_path": 7}, requested: map[string]any{"file_path": 7}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			announceTool(t, server, test.toolID, test.announcedName, test.announced, test.location)
			options := test.options
			if options == nil {
				options = standardPermissionOptions()
			}
			outcome := requestPermissionWithState(t, server, "permission-"+test.toolID, test.toolID, test.requestName, test.requested, test.location, options, test.requestKind, test.requestStatus)
			if outcome != "cancelled" {
				t.Fatalf("invalid request outcome = %q", outcome)
			}
		})
	}
	if outcome := requestPermission(t, server, "permission-persistent-retry", "persistent", "write_file", map[string]any{"file_path": filepath.Join(artifact, "persistent.md")}, filepath.Join(artifact, "persistent.md"), standardPermissionOptions()); outcome != "cancelled" {
		t.Fatalf("denied grant was inherited by retry: %q", outcome)
	}

	if outcome := requestPermission(t, server, "permission-unannounced", "unannounced", "write_file", map[string]any{"file_path": filepath.Join(artifact, "unannounced.md")}, filepath.Join(artifact, "unannounced.md"), standardPermissionOptions()); outcome != "cancelled" {
		t.Fatalf("unannounced tool outcome = %q", outcome)
	}
	finishPrompt(t, server, promptErr, "end_turn")
}

func TestReadOnlyTurnAndForeignOrStalePermissionFailClosed(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(t.TempDir(), "artifact", "file.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("read only", func(t *testing.T) {
		connection, server, raw := establishPolicyConnection(t, workspace, "", connectionHandler{})
		defer raw.Close()
		defer connection.Close()
		promptErr := startPrompt(t, connection, server)
		announceTool(t, server, "readonly", "write_file", map[string]any{"file_path": target}, target)
		if outcome := requestPermission(t, server, "readonly-permission", "readonly", "write_file", map[string]any{"file_path": target}, target, standardPermissionOptions()); outcome != "cancelled" {
			t.Fatalf("read-only outcome = %q", outcome)
		}
		finishPrompt(t, server, promptErr, "end_turn")
	})

	for _, test := range []struct {
		name      string
		sessionID string
		stale     bool
	}{
		{name: "foreign session", sessionID: "foreign"},
		{name: "stale turn", sessionID: "s", stale: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := filepath.Dir(target)
			connection, server, raw := establishPolicyConnection(t, workspace, artifact, connectionHandler{})
			defer raw.Close()
			promptErr := startPrompt(t, connection, server)
			if test.stale {
				finishPrompt(t, server, promptErr, "end_turn")
			}
			if err := server.sendRequest(stringID("invalid-correlation"), "session/request_permission", permissionPayload(test.sessionID, "tool", "write_file", map[string]any{"file_path": target}, target, standardPermissionOptions(), "", "")); err != nil {
				t.Fatal(err)
			}
			waitForDone(t, connection.Done())
			if !errors.Is(connection.Err(), ErrProtocol) || !strings.Contains(connection.Err().Error(), "foreign or stale") {
				t.Fatalf("connection error = %v", connection.Err())
			}
		})
	}
}

func TestDelegatedReadAllowsOnlyLogicalRootsAndNativeReadIsAdvisory(t *testing.T) {
	workspace := t.TempDir()
	artifact := filepath.Join(t.TempDir(), "artifact")
	outside := filepath.Join(t.TempDir(), "outside")
	for _, directory := range []string{artifact, outside} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workspaceFile := filepath.Join(workspace, "source.txt")
	artifactFile := filepath.Join(artifact, "artifact.txt")
	outsideFile := filepath.Join(outside, "secret.txt")
	for path, content := range map[string]string{workspaceFile: "one\ntwo\nthree\n", artifactFile: "artifact", outsideFile: "forbidden-marker"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(workspace, "escape")
	if err := makeDirectoryLink(link, outside); err != nil {
		t.Skipf("directory link fixture unavailable: %v", err)
	}

	connection, server, raw := establishPolicyConnection(t, workspace, artifact, connectionHandler{})
	defer raw.Close()
	defer connection.Close()
	promptErr := startPrompt(t, connection, server)

	if content, rpcErr := requestRead(t, server, "read-workspace", "s", "source.txt", uint32Pointer(2), uint32Pointer(1)); rpcErr != nil || content != "two\n" {
		t.Fatalf("workspace read = %q, %v", content, rpcErr)
	}
	if content, rpcErr := requestRead(t, server, "read-artifact", "s", artifactFile, nil, nil); rpcErr != nil || content != "artifact" {
		t.Fatalf("artifact read = %q, %v", content, rpcErr)
	}
	for _, path := range []string{outsideFile, filepath.Join(link, "secret.txt")} {
		content, rpcErr := requestRead(t, server, "denied-"+filepath.Base(filepath.Dir(path)), "s", path, nil, nil)
		if rpcErr == nil || rpcErr.Code != invalidParamsCode || content != "" {
			t.Fatalf("denied read = %q, %+v", content, rpcErr)
		}
		encoded, _ := json.Marshal(rpcErr)
		if strings.Contains(string(encoded), path) || strings.Contains(string(encoded), "forbidden-marker") {
			t.Fatalf("read denial leaked path/content: %s", encoded)
		}
	}

	// This event is observed but deliberately does not produce a claim that
	// native Qwen reads are technically confined by Stepan.
	if err := server.sendNotification("session/update", map[string]any{
		"sessionId": "s",
		"update": map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": "native-read", "kind": "read", "status": "in_progress",
			"rawInput": map[string]any{"file_path": outsideFile}, "_meta": map[string]any{"toolName": "read_file"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	finishPrompt(t, server, promptErr, "end_turn")
	if connection.Err() != nil {
		t.Fatalf("native read status corrupted connection: %v", connection.Err())
	}
}

func TestDelegatedReadRejectsForeignAndStaleSessions(t *testing.T) {
	for _, test := range []struct {
		name      string
		sessionID string
		stale     bool
	}{
		{name: "foreign", sessionID: "foreign"},
		{name: "stale", sessionID: "s", stale: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			file := filepath.Join(workspace, "file.txt")
			if err := os.WriteFile(file, []byte("safe"), 0o600); err != nil {
				t.Fatal(err)
			}
			connection, server, raw := establishPolicyConnection(t, workspace, "", connectionHandler{})
			defer raw.Close()
			promptErr := startPrompt(t, connection, server)
			if test.stale {
				finishPrompt(t, server, promptErr, "end_turn")
			}
			if err := server.sendRequest(stringID("invalid-read"), "fs/read_text_file", map[string]any{"sessionId": test.sessionID, "path": file}); err != nil {
				t.Fatal(err)
			}
			waitForDone(t, connection.Done())
			if !errors.Is(connection.Err(), ErrProtocol) || !strings.Contains(connection.Err().Error(), "foreign or stale fs/read_text_file") {
				t.Fatalf("connection error = %v", connection.Err())
			}
		})
	}
}

func TestCancelClosesPendingPermissionAndRejectsLateRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := connectionHandler{permission: func(connection *Connection, received message) error {
		close(started)
		<-release
		return connection.respond(received.id, selectedPermissionResult("allow-once"))
	}}
	workspace := t.TempDir()
	artifact := t.TempDir()
	connection, server, raw := establishPolicyConnection(t, workspace, artifact, handler)
	defer raw.Close()
	promptErr := startPrompt(t, connection, server)
	if err := server.sendRequest(stringID("pending"), "session/request_permission", map[string]any{
		"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tool"}, "options": standardPermissionOptions(),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("permission handler did not start")
	}
	cancelErr := make(chan error, 1)
	go func() { cancelErr <- connection.sendNotification("session/cancel", sessionParams{SessionID: "s"}) }()
	cancel, err := server.read()
	if err != nil || cancel.kind != notificationMessage || cancel.method != "session/cancel" {
		t.Fatalf("cancel notification = %+v, %v", cancel, err)
	}
	response, err := server.read()
	if err != nil || permissionOutcome(t, response) != "cancelled" {
		t.Fatalf("cancelled permission = %+v, %v", response, err)
	}
	if err := <-cancelErr; err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := server.sendRequest(stringID("late"), "session/request_permission", map[string]any{
		"sessionId": "s", "toolCall": map[string]any{"toolCallId": "late-tool"}, "options": standardPermissionOptions(),
	}); err != nil {
		t.Fatal(err)
	}
	waitForDone(t, connection.Done())
	if !errors.Is(connection.Err(), ErrProtocol) || !strings.Contains(connection.Err().Error(), "foreign or stale") {
		t.Fatalf("late request error = %v", connection.Err())
	}
	if err := <-promptErr; err == nil {
		t.Fatal("cancelled prompt unexpectedly succeeded")
	}
}

func TestCloseInvalidatesPendingPermissionWithoutApproval(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handlerResult := make(chan error, 1)
	handler := connectionHandler{permission: func(connection *Connection, received message) error {
		close(started)
		<-release
		err := connection.respond(received.id, selectedPermissionResult("allow-once"))
		handlerResult <- err
		return err
	}}
	connection, server, raw := establishPolicyConnection(t, t.TempDir(), t.TempDir(), handler)
	promptErr := startPrompt(t, connection, server)
	if err := server.sendRequest(stringID("pending-close"), "session/request_permission", map[string]any{
		"sessionId": "s", "toolCall": map[string]any{"toolCallId": "tool"}, "options": standardPermissionOptions(),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("permission handler did not start")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-handlerResult:
		if !errors.Is(err, ErrConnectionClosed) {
			t.Fatalf("late approval error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late approval remained blocked")
	}
	if err := <-promptErr; !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("prompt error = %v", err)
	}
	if response, err := server.read(); err == nil {
		t.Fatalf("approval reached wire after close: %+v", response)
	}
	_ = raw.Close()
}

func establishPolicyConnection(t *testing.T, workspace, writableRoot string, handler connectionHandler) (*Connection, *transport, net.Conn) {
	t.Helper()
	connection, _, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, handler)
	if err != nil {
		t.Fatal(err)
	}
	connection.configureFilePolicy(filepath.Clean(workspace), filepath.Clean(writableRoot))
	if writableRoot == "" {
		connection.configureFilePolicy(filepath.Clean(workspace), "")
	}
	return connection, server, raw
}

func announceTool(t *testing.T, server *transport, toolID, name string, input map[string]any, location string) {
	t.Helper()
	if err := server.sendNotification("session/update", map[string]any{
		"sessionId": "s",
		"update": map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": toolID, "kind": "edit", "status": "pending",
			"rawInput": input, "locations": []any{map[string]any{"path": location}}, "_meta": map[string]any{"toolName": name},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func standardPermissionOptions() []map[string]string {
	return []map[string]string{
		{"optionId": "always", "kind": "allow_always", "name": "Always"},
		{"optionId": "allow-once", "kind": "allow_once", "name": "Allow"},
		{"optionId": "reject", "kind": "reject_once", "name": "Reject"},
	}
}

func permissionPayload(sessionID, toolID, name string, input map[string]any, location string, options []map[string]string, kind, status string) map[string]any {
	if kind == "" {
		kind = "edit"
	}
	if status == "" {
		status = "pending"
	}
	toolCall := map[string]any{
		"toolCallId": toolID, "kind": kind, "status": status, "rawInput": input, "_meta": map[string]any{"toolName": name},
	}
	if location != "" {
		toolCall["locations"] = []any{map[string]any{"path": location}}
	}
	return map[string]any{"sessionId": sessionID, "toolCall": toolCall, "options": options}
}

func requestPermission(t *testing.T, server *transport, requestID, toolID, name string, input map[string]any, location string, options []map[string]string) string {
	t.Helper()
	return requestPermissionWithState(t, server, requestID, toolID, name, input, location, options, "", "")
}

func requestPermissionWithState(t *testing.T, server *transport, requestID, toolID, name string, input map[string]any, location string, options []map[string]string, kind, status string) string {
	t.Helper()
	if err := server.sendRequest(stringID(requestID), "session/request_permission", permissionPayload("s", toolID, name, input, location, options, kind, status)); err != nil {
		t.Fatal(err)
	}
	response, err := server.read()
	if err != nil {
		t.Fatal(err)
	}
	return permissionOutcome(t, response)
}

func permissionOutcome(t *testing.T, response message) string {
	t.Helper()
	if response.kind != responseMessage || response.err != nil {
		t.Fatalf("permission response = %+v", response)
	}
	var result struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(response.result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome.Outcome == "selected" {
		return "selected:" + result.Outcome.OptionID
	}
	return result.Outcome.Outcome
}

func requestRead(t *testing.T, server *transport, requestID, sessionID, path string, line, limit *uint32) (string, *rpcError) {
	t.Helper()
	params := map[string]any{"sessionId": sessionID, "path": path}
	if line != nil {
		params["line"] = *line
	}
	if limit != nil {
		params["limit"] = *limit
	}
	if err := server.sendRequest(stringID(requestID), "fs/read_text_file", params); err != nil {
		t.Fatal(err)
	}
	response, err := server.read()
	if err != nil {
		t.Fatal(err)
	}
	if response.err != nil {
		return "", response.err
	}
	var result struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(response.result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Content, nil
}

func finishPrompt(t *testing.T, server *transport, promptErr <-chan error, stopReason string) {
	t.Helper()
	if err := server.sendResult(integerID(3), map[string]string{"stopReason": stopReason}); err != nil {
		t.Fatal(err)
	}
	if err := <-promptErr; err != nil {
		t.Fatal(err)
	}
}

func uint32Pointer(value uint32) *uint32 { return &value }
