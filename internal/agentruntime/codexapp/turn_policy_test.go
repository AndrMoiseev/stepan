package codexapp

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTurnPolicyAcceptsOnlyObservedPathsInsideSingleRoot(t *testing.T) {
	workspace := t.TempDir()
	writable := filepath.Join(workspace, "spec")
	sibling := filepath.Join(workspace, "spec-sibling")
	outside := t.TempDir()
	for _, directory := range []string{writable, sibling} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	escape := filepath.Join(writable, "escape")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", escape, outside).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}

	writePolicy, err := SingleWriteRootTurnPolicy(writable)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		policy    TurnPolicy
		paths     []string
		grantRoot *string
		scope     string
		want      ApprovalDecision
	}{
		{"read-only", ReadOnlyTurnPolicy(), []string{filepath.Join(writable, "file.md")}, nil, "", DecisionDecline},
		{"inside", writePolicy, []string{filepath.Join(writable, "file.md")}, nil, "", DecisionAccept},
		{"sibling prefix", writePolicy, []string{filepath.Join(sibling, "file.md")}, nil, "", DecisionDecline},
		{"outside", writePolicy, []string{filepath.Join(outside, "file.md")}, nil, "", DecisionDecline},
		{"reparse escape", writePolicy, []string{filepath.Join(escape, "file.md")}, nil, "", DecisionDecline},
		{"grant root", writePolicy, []string{filepath.Join(writable, "file.md")}, stringPointer(writable), "", DecisionDecline},
		{"session grant", writePolicy, []string{filepath.Join(writable, "file.md")}, nil, "session", DecisionDecline},
		{"not observed", writePolicy, nil, nil, "", DecisionDecline},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := testTurnRun(t, workspace, test.policy)
			if test.paths != nil {
				changes := make([]map[string]string, 0, len(test.paths))
				for _, path := range test.paths {
					changes = append(changes, map[string]string{"path": path})
				}
				message := Message{Method: "item/started", Params: mustJSON(t, map[string]any{
					"threadId": "thread", "turnId": "turn",
					"item": map[string]any{"id": "item", "type": "fileChange", "changes": changes},
				})}
				if err := run.observeFileChanges(message); err != nil {
					t.Fatal(err)
				}
			}
			ids := approvalIDs{ThreadID: "thread", TurnID: "turn", ItemID: "item", GrantRoot: test.grantRoot, Scope: test.scope}
			decision := run.evaluateApproval(ApprovalRequest{Kind: FileChangeApproval, ThreadID: ids.ThreadID, TurnID: ids.TurnID, ItemID: ids.ItemID, ids: ids})
			if decision != test.want {
				t.Fatalf("decision = %q, want %q", decision, test.want)
			}
		})
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == ".stepan" || entry.Name() == "state.json" || entry.Name() == "approvals.jsonl" {
			t.Fatalf("policy persisted artifact %q", entry.Name())
		}
	}
}

func TestTurnPolicyAccumulatesObservedPaths(t *testing.T) {
	workspace := t.TempDir()
	writable := filepath.Join(workspace, "spec")
	outside := t.TempDir()
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := SingleWriteRootTurnPolicy(writable)
	if err != nil {
		t.Fatal(err)
	}
	run := testTurnRun(t, workspace, policy)
	for method, path := range map[string]string{
		"item/started":                 filepath.Join(outside, "outside.md"),
		"item/fileChange/patchUpdated": filepath.Join(writable, "inside.md"),
	} {
		params := map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item", "changes": []map[string]string{{"path": path}}}
		if method == "item/started" {
			params["item"] = map[string]any{"id": "item", "type": "fileChange", "changes": params["changes"]}
		}
		if err := run.observeFileChanges(Message{Method: method, Params: mustJSON(t, params)}); err != nil {
			t.Fatal(err)
		}
	}
	ids := approvalIDs{ThreadID: "thread", TurnID: "turn", ItemID: "item"}
	if decision := run.evaluateApproval(ApprovalRequest{Kind: FileChangeApproval, ThreadID: ids.ThreadID, TurnID: ids.TurnID, ItemID: ids.ItemID, ids: ids}); decision != DecisionDecline {
		t.Fatalf("decision after outside path = %q", decision)
	}
}

func TestTurnPolicyDeclinesCommandPermissionsAndNetwork(t *testing.T) {
	workspace := t.TempDir()
	run := testTurnRun(t, workspace, ReadOnlyTurnPolicy())
	for _, kind := range []ApprovalKind{CommandApproval, PermissionsApproval} {
		ids := approvalIDs{ThreadID: "thread", TurnID: "turn", ItemID: "item"}
		permissions := permissionProfile{Network: &networkPermissions{Enabled: boolPointer(true)}}
		if decision := run.evaluateApproval(ApprovalRequest{Kind: kind, ThreadID: ids.ThreadID, TurnID: ids.TurnID, ItemID: ids.ItemID, ids: ids, permissions: permissions}); decision != DecisionDecline {
			t.Fatalf("%s decision = %q", kind, decision)
		}
	}
}

func TestTurnApprovalIsOneShotAndTurnScoped(t *testing.T) {
	workspace := t.TempDir()
	writable := filepath.Join(workspace, "spec")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := SingleWriteRootTurnPolicy(writable)
	if err != nil {
		t.Fatal(err)
	}
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	connection := newConnection(NewTransport(clientSide, clientSide), Handler{})
	run := testTurnRun(t, workspace, policy)
	connection.mu.Lock()
	connection.turn = run
	connection.mu.Unlock()
	server := NewTransport(serverSide, serverSide)
	if err := server.SendNotification("item/started", map[string]any{
		"threadId": "thread", "turnId": "turn",
		"item": map[string]any{"id": "item", "type": "fileChange", "changes": []map[string]string{{"path": filepath.Join(writable, "file.md")}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SendRequest(StringID("approval"), "item/fileChange/requestApproval", map[string]string{
		"threadId": "thread", "turnId": "turn", "itemId": "item",
	}); err != nil {
		t.Fatal(err)
	}
	response, err := server.Read()
	if err != nil || response.Error != nil || !resultHasDecision(response.Result, DecisionAccept) {
		t.Fatalf("approval response = %+v, %v", response, err)
	}
	connection.mu.Lock()
	connection.turn = nil
	connection.mu.Unlock()
	if err := server.SendRequest(StringID("after-turn"), "item/fileChange/requestApproval", map[string]string{
		"threadId": "thread", "turnId": "turn", "itemId": "item",
	}); err != nil {
		t.Fatal(err)
	}
	<-connection.Done()
	if connection.Err() == nil {
		t.Fatal("approval after turn did not fail closed")
	}
}

func testTurnRun(t *testing.T, workspace string, policy TurnPolicy) *turnRun {
	t.Helper()
	approvals, err := NewApprovalEvaluator(workspace, AccessPolicy{WritableRoots: writableRoots(policy)})
	if err != nil {
		t.Fatal(err)
	}
	return &turnRun{
		threadID: "thread", turnID: "turn", approvals: approvals,
		pending: make(map[string]bool), wake: make(chan struct{}, 1),
	}
}

func stringPointer(value string) *string { return &value }
func boolPointer(value bool) *bool       { return &value }

func resultHasDecision(raw json.RawMessage, decision ApprovalDecision) bool {
	var result struct {
		Decision ApprovalDecision `json:"decision"`
	}
	return json.Unmarshal(raw, &result) == nil && result.Decision == decision
}

func TestTurnPolicyResponseNeverGrantsSession(t *testing.T) {
	request := ApprovalRequest{Kind: FileChangeApproval}
	data, err := json.Marshal(request.Response(DecisionAccept))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"decision":"accept"}` {
		t.Fatalf("approval response = %s", data)
	}
}
