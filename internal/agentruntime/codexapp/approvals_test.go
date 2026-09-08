package codexapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovalResponsesAreOneShotAndTurnScoped(t *testing.T) {
	for _, kind := range []ApprovalKind{CommandApproval, FileChangeApproval, PermissionsApproval} {
		for _, decision := range []ApprovalDecision{DecisionAccept, DecisionDecline, DecisionCancel} {
			request := ApprovalRequest{Kind: kind, permissions: permissionProfile{Network: &networkPermissions{Enabled: ptrBool(true)}}}
			data, err := json.Marshal(request.Response(decision))
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
	fileChanges := map[string][]string{
		fileChangeKey("thread", "turn", "safe-file"):      {filepath.Join(writable, "safe.txt")},
		fileChangeKey("thread", "turn", "protected-file"): {filepath.Join(workspace, ".git", "config")},
	}
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
			if got := evaluateApproval(policy, fileChanges, test.kind, test.ids, test.permissions); got != test.want {
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
	for _, allowed := range []string{workspace, filepath.Join(workspace, "safe.txt")} {
		if !allowedRequestedPath(policy, allowed, policy.WritableRoots) {
			t.Errorf("explicit root path rejected: %s", allowed)
		}
	}
	for _, protected := range []string{filepath.Join(workspace, ".stepan", "state.json"), filepath.Join(workspace, ".git", "config")} {
		if allowedRequestedPath(policy, protected, policy.WritableRoots) {
			t.Errorf("protected path allowed: %s", protected)
		}
	}
}

func ptr(value string) *string { return &value }

func ptrBool(value bool) *bool { return &value }
