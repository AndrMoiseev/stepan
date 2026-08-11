package codexapp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplayApprovalRequestsPreservesOpaqueCorrelation(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "approval-requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	wantKinds := []ApprovalKind{CommandApproval, FileChangeApproval, PermissionsApproval}
	wantIDs := []string{StringID("opaque-command").Key(), IntID(42).Key(), StringID("opaque-permissions").Key()}
	scanner := bufio.NewScanner(file)
	index := 0
	for scanner.Scan() {
		message, err := ParseMessage(scanner.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		kind, ids, _, err := decodeApproval(message)
		if err != nil || kind != wantKinds[index] || message.ID.Key() != wantIDs[index] || ids.ThreadID != "thread-1" || ids.TurnID != "turn-1" {
			t.Fatalf("replay[%d] = %q %q %+v, %v", index, kind, message.ID.Key(), ids, err)
		}
		index++
	}
	if err := scanner.Err(); err != nil || index != len(wantKinds) {
		t.Fatalf("replay count = %d, error = %v", index, err)
	}
}

func TestApprovalResponsesAreOneShotAndTurnScoped(t *testing.T) {
	for _, kind := range []ApprovalKind{CommandApproval, FileChangeApproval, PermissionsApproval} {
		for _, decision := range []ApprovalDecision{DecisionAccept, DecisionDecline, DecisionCancel} {
			request := &approvalRequest{pending: PendingApproval{Kind: kind}, permissions: permissionProfile{Network: &networkPermissions{Enabled: ptrBool(true)}}}
			data, err := json.Marshal(approvalResponse(request, decision))
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if strings.Contains(text, "acceptForSession") || strings.Contains(strings.ToLower(text), "amendment") || strings.Contains(text, `"scope":"session"`) {
				t.Fatalf("%s/%s response widens scope: %s", kind, decision, text)
			}
			if kind == PermissionsApproval && !strings.Contains(text, `"scope":"turn"`) {
				t.Fatalf("permission response is not turn-scoped: %s", text)
			}
		}
	}
}

func TestApprovalPolicyIsExactAndFailClosed(t *testing.T) {
	workspace := t.TempDir()
	writable := filepath.Join(workspace, "public")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := normalizePolicy(AccessPolicy{
		ReadableRoots: []string{workspace}, WritableRoots: []string{writable},
		ProtectedPatterns: []string{filepath.Join(workspace, "public", "*.key")},
		AllowedCommands:   []CommandForm{{Command: "go test ./...", CWD: workspace}},
	}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	manager := &approvalManager{policy: policy, fileChanges: map[string][]string{
		fileChangeKey("thread", "turn", "safe-file"):      {filepath.Join(writable, "safe.txt")},
		fileChangeKey("thread", "turn", "protected-file"): {filepath.Join(workspace, ".git", "config")},
	}}
	command, cwd := "go test ./...", workspace
	network := true
	tests := []struct {
		name        string
		kind        ApprovalKind
		ids         approvalIDs
		permissions permissionProfile
		want        ApprovalDecision
	}{
		{"exact command", CommandApproval, approvalIDs{Command: &command, CWD: &cwd}, permissionProfile{}, DecisionAccept},
		{"cwd mismatch", CommandApproval, approvalIDs{Command: &command, CWD: ptr(filepath.Dir(workspace))}, permissionProfile{}, DecisionDecline},
		{"shell nesting", CommandApproval, approvalIDs{Command: ptr(command + " ; whoami"), CWD: &cwd}, permissionProfile{}, DecisionDecline},
		{"execpolicy amendment", CommandApproval, approvalIDs{Command: &command, CWD: &cwd, ProposedExecpolicyAmendment: []string{"prefix_rule(*)"}}, permissionProfile{}, DecisionDecline},
		{"network denied", PermissionsApproval, approvalIDs{CWD: &cwd}, permissionProfile{Network: &networkPermissions{Enabled: &network}}, DecisionDecline},
		{"allowed write", PermissionsApproval, approvalIDs{CWD: &cwd}, permissionProfile{FileSystem: &fileSystemPermissions{Write: []string{filepath.Join(writable, "new.txt")}}}, DecisionAccept},
		{"outside write", PermissionsApproval, approvalIDs{CWD: &cwd}, permissionProfile{FileSystem: &fileSystemPermissions{Write: []string{filepath.Join(workspace, "outside.txt")}}}, DecisionDecline},
		{"protected read", PermissionsApproval, approvalIDs{CWD: &cwd}, permissionProfile{FileSystem: &fileSystemPermissions{Read: []string{filepath.Join(workspace, ".codex", "auth.json")}}}, DecisionDecline},
		{"protected pattern", PermissionsApproval, approvalIDs{CWD: &cwd}, permissionProfile{FileSystem: &fileSystemPermissions{Read: []string{filepath.Join(writable, "secret.key")}}}, DecisionDecline},
		{"glob rejected", PermissionsApproval, approvalIDs{CWD: &cwd}, permissionProfile{FileSystem: &fileSystemPermissions{Entries: []permissionEntry{{Access: "read", Path: permissionPath{Type: "glob_pattern", Pattern: "**"}}}}}, DecisionDecline},
		{"safe file change", FileChangeApproval, approvalIDs{ThreadID: "thread", TurnID: "turn", ItemID: "safe-file"}, permissionProfile{}, DecisionAccept},
		{"protected file change", FileChangeApproval, approvalIDs{ThreadID: "thread", TurnID: "turn", ItemID: "protected-file"}, permissionProfile{}, DecisionDecline},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := manager.evaluate(test.kind, test.ids, test.permissions); got != test.want {
				t.Fatalf("decision = %q, want %q", got, test.want)
			}
		})
	}
	if _, err := normalizePolicy(AccessPolicy{AllowedCommands: []CommandForm{{Command: "Remove-Item -Recurse target", CWD: workspace}}}, workspace); err == nil {
		t.Fatal("destructive command entered allowlist")
	}
	if _, err := normalizePolicy(AccessPolicy{AllowedCommands: []CommandForm{{Command: `powershell -Command "Get-Content secret"`, CWD: workspace}}}, workspace); err == nil {
		t.Fatal("nested shell entered allowlist")
	}
}

func TestProtectedAncestorDoesNotBlockExplicitRoot(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, ".stepan", "role-workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := normalizePolicy(AccessPolicy{ReadableRoots: []string{workspace}, WritableRoots: []string{workspace}}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	manager := &approvalManager{policy: policy}
	for _, allowed := range []string{workspace, filepath.Join(workspace, "safe.txt")} {
		if !manager.allowedRequestedPath(allowed, manager.policy.WritableRoots) {
			t.Errorf("explicit root path rejected: %s", allowed)
		}
	}
	for _, protected := range []string{filepath.Join(workspace, ".stepan", "state.json"), filepath.Join(workspace, ".git", "config")} {
		if manager.allowedRequestedPath(protected, manager.policy.WritableRoots) {
			t.Errorf("protected path allowed: %s", protected)
		}
	}
}

func TestApprovalSnapshotChangeDeclinesBeforeSend(t *testing.T) {
	repository := newApprovalRepository(t)
	manager, cleanup := testApprovalManager(t, repository, AccessPolicy{
		ReadableRoots: []string{repository}, AllowedCommands: []CommandForm{{Command: "go test ./...", CWD: repository}},
	}, nil)
	defer cleanup()
	transport, output, message := approvalTransport(t, StringID("opaque"), "item/commandExecution/requestApproval", map[string]any{
		"threadId": "thread", "turnId": "turn", "itemId": "item", "startedAtMs": 1, "command": "go test ./...", "cwd": repository,
	})
	request, decision, err := manager.register(message)
	if err != nil || decision != DecisionAccept {
		t.Fatalf("register = %q, %v", decision, err)
	}
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.resolve(transport, request, decision, DecisionByPolicy); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"decision":"decline"`)) {
		t.Fatalf("response = %s", output.Bytes())
	}
	journal := readJournal(t, manager.journal.Name())
	if !strings.Contains(journal, `"decision":"decline"`) || strings.Contains(journal, "tracked.txt") {
		t.Fatalf("journal = %s", journal)
	}
}

func TestOperatorContinuationIsSentOnce(t *testing.T) {
	workspace := t.TempDir()
	release := make(chan struct{})
	manager, cleanup := testApprovalManager(t, workspace, AccessPolicy{WritableRoots: []string{workspace}, OperatorDecisions: []ApprovalKind{FileChangeApproval}}, func(PendingApproval) (ApprovalDecision, error) {
		<-release
		return DecisionAccept, nil
	})
	defer cleanup()
	manager.fileChanges[fileChangeKey("thread", "turn", "item")] = []string{filepath.Join(workspace, "safe.txt")}
	transport, output, message := approvalTransport(t, IntID(7), "item/fileChange/requestApproval", map[string]any{
		"threadId": "thread", "turnId": "turn", "itemId": "item", "startedAtMs": 1, "reason": "SECRET",
	})
	_, decision, err := manager.register(message)
	if err != nil || decision != DecisionAwaitOperator {
		t.Fatalf("register = %q, %v", decision, err)
	}
	close(release)
	result := <-manager.operatorOut
	manager.fileChanges[fileChangeKey("thread", "turn", "item")] = []string{filepath.Join(workspace, ".git", "config")}
	if err := manager.continueOperator(result, transport); err != nil {
		t.Fatal(err)
	}
	if err := manager.continueOperator(result, transport); !errors.Is(err, ErrDuplicateResponse) {
		t.Fatalf("second continuation = %v", err)
	}
	if strings.Count(output.String(), "\n") != 1 || !strings.Contains(output.String(), `"decision":"decline"`) || strings.Contains(readJournal(t, manager.journal.Name()), "SECRET") {
		t.Fatalf("response = %q, journal = %s", output.String(), readJournal(t, manager.journal.Name()))
	}
}

func TestConnectionLossKeepsFailClosedPending(t *testing.T) {
	workspace := t.TempDir()
	manager, cleanup := testApprovalManager(t, workspace, AccessPolicy{WritableRoots: []string{workspace}, OperatorDecisions: []ApprovalKind{FileChangeApproval}}, func(PendingApproval) (ApprovalDecision, error) {
		select {}
	})
	defer cleanup()
	manager.fileChanges[fileChangeKey("thread", "turn", "item")] = []string{filepath.Join(workspace, "safe.txt")}
	_, _, message := approvalTransport(t, StringID("lost"), "item/fileChange/requestApproval", map[string]any{
		"threadId": "thread", "turnId": "turn", "itemId": "item", "startedAtMs": 1,
	})
	if _, _, err := manager.register(message); err != nil {
		t.Fatal(err)
	}
	if err := manager.failClosed(); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(manager.statePath)
	if err != nil || !bytes.Contains(state, []byte(`"status":"failed_closed"`)) || !bytes.Contains(state, []byte(`"decision":"cancel"`)) {
		t.Fatalf("state = %s, %v", state, err)
	}
}

func TestRestartClosesOldPendingWithoutResending(t *testing.T) {
	workspace := t.TempDir()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	old := PendingApproval{
		SchemaVersion: 1, RequestID: json.RawMessage(`"old-request"`), ThreadID: "thread", TurnID: "turn", ItemID: "item",
		Kind: CommandApproval, Status: "pending", PolicySnapshotID: "old-policy",
	}
	if err := writeJSONAtomic(statePath, durableProbeState{SchemaVersion: 1, Status: StatusAwaitingOperator, Pending: []PendingApproval{old}}); err != nil {
		t.Fatal(err)
	}
	journal, err := os.OpenFile(filepath.Join(dir, "approvals.jsonl"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newApprovalManager(statePath, journal, workspace, AccessPolicy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if manager.hasPending() {
		t.Fatal("restarted manager retained a live stdio request")
	}
	state, err := os.ReadFile(statePath)
	if err != nil || !bytes.Contains(state, []byte(`"closed_approvals"`)) || !bytes.Contains(state, []byte(`"status":"failed_closed"`)) {
		t.Fatalf("state = %s, %v", state, err)
	}
	if journalText := readJournal(t, journal.Name()); !strings.Contains(journalText, "failed_closed_after_restart") {
		t.Fatalf("journal = %s", journalText)
	}
}

func approvalTransport(t *testing.T, id ID, method string, params any) (*Transport, *bytes.Buffer, Message) {
	t.Helper()
	line, err := json.Marshal(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
		ID     ID     `json:"id"`
	}{method, params, id})
	if err != nil {
		t.Fatal(err)
	}
	input := append(line, '\n')
	var output bytes.Buffer
	transport := NewTransport(bytes.NewReader(input), &output)
	message, err := transport.Read()
	if err != nil {
		t.Fatal(err)
	}
	return transport, &output, message
}

func testApprovalManager(t *testing.T, workspace string, policy AccessPolicy, operator OperatorFunc) (*approvalManager, func()) {
	t.Helper()
	dir := t.TempDir()
	journal, err := os.OpenFile(filepath.Join(dir, "approvals.jsonl"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newApprovalManager(filepath.Join(dir, "state.json"), journal, workspace, policy, operator)
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	return manager, func() { _ = journal.Close() }
}

func readJournal(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func newApprovalRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runApprovalGit(t, repository, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runApprovalGit(t, repository, "add", "tracked.txt")
	runApprovalGit(t, repository, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "initial")
	return repository
}

func runApprovalGit(t *testing.T, repository string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func ptr(value string) *string { return &value }

func ptrBool(value bool) *bool { return &value }
