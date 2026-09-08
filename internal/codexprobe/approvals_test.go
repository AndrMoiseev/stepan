package codexprobe

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	observeApprovalPath(t, manager, "thread", "turn", "item", filepath.Join(workspace, "safe.txt"))
	transport, output, message := approvalTransport(t, IntID(7), "item/fileChange/requestApproval", map[string]any{
		"threadId": "thread", "turnId": "turn", "itemId": "item", "startedAtMs": 1, "reason": "SECRET",
	})
	_, decision, err := manager.register(message)
	if err != nil || decision != DecisionAwaitOperator {
		t.Fatalf("register = %q, %v", decision, err)
	}
	close(release)
	result := <-manager.operatorOut
	observeApprovalPath(t, manager, "thread", "turn", "item", filepath.Join(workspace, ".git", "config"))
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
	observeApprovalPath(t, manager, "thread", "turn", "item", filepath.Join(workspace, "safe.txt"))
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

func runApprovalGit(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func ptr(value string) *string { return &value }

func ptrBool(value bool) *bool { return &value }

func observeApprovalPath(t *testing.T, manager *approvalManager, threadID, turnID, itemID, path string) {
	t.Helper()
	message := Message{Method: "item/started", Params: mustJSON(t, map[string]any{
		"threadId": threadID, "turnId": turnID,
		"item": map[string]any{"id": itemID, "type": "fileChange", "changes": []map[string]string{{"path": path}}},
	})}
	if err := manager.observeFileChanges(message, threadID, turnID); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
