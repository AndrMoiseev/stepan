//go:build git_integration

package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
	"github.com/AndrMoiseev/stepan/internal/git"
)

type contractControl interface {
	workspace.Control
	workspace.Committer
	workspace.CommitObserver
}

// The same observable expectations apply to both adapters. Object IDs, index
// encodings and patch formatting are deliberately opaque to the contract.
func TestWorkspaceConformance(t *testing.T) {
	for _, adapter := range []struct {
		name   string
		create func(*testing.T) (contractControl, string)
	}{
		{"git", func(t *testing.T) (contractControl, string) { return Control{}, newGitWorkspace(t) }},
		{"filesystem", func(t *testing.T) (contractControl, string) {
			root := t.TempDir()
			writeGitWorkspaceFile(t, filepath.Join(root, "tracked.txt"), "initial\n")
			return testfs.New(), root
		}},
	} {
		t.Run(adapter.name, func(t *testing.T) {
			t.Parallel()
			for _, scenario := range []struct {
				name string
				run  func(*testing.T, contractControl, string)
			}{
				{"targeted restore", contractTargetedRestore},
				{"stale restore", contractStaleRestore},
				{"unsafe restore", contractUnsafeRestore},
				{"commit and diff", contractCommitAndDiff},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					control, root := adapter.create(t)
					scenario.run(t, control, root)
				})
			}
		})
	}
}

func contractCapture(t *testing.T, control workspace.Control, root string) git.Snapshot {
	t.Helper()
	snapshot, err := control.Capture(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func contractTargetedRestore(t *testing.T, control contractControl, root string) {
	ctx := context.Background()
	writeGitWorkspaceFile(t, filepath.Join(root, "prior.txt"), "earlier uncommitted work\r\n")
	writeGitWorkspaceFile(t, filepath.Join(root, "deleted.txt"), "restore deleted bytes\x00\xff")
	before := contractCapture(t, control, root)
	if err := control.EnsureUnchanged(ctx, root, before); err != nil {
		t.Fatal(err)
	}
	writeGitWorkspaceFile(t, filepath.Join(root, "tracked.txt"), "changed\n")
	writeGitWorkspaceFile(t, filepath.Join(root, "new dir", "empty.txt"), "")
	writeGitWorkspaceFile(t, filepath.Join(root, "allowed.txt"), "keep\n")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	after := contractCapture(t, control, root)
	diff, err := control.Diff(ctx, root, before, after)
	want := []string{"allowed.txt", "deleted.txt", "new dir/empty.txt", "tracked.txt"}
	if err != nil || !reflect.DeepEqual(diff.Paths, want) || diff.HeadChanged || diff.HeadRefChanged || diff.IndexChanged || diff.SubmodulesChanged {
		t.Fatalf("file changes = %#v, %v", diff, err)
	}
	if err := control.EnsureUnchanged(ctx, root, before); !errors.Is(err, git.ErrRepositoryDiverged) {
		t.Fatalf("divergence = %v", err)
	}
	restored, err := control.RestorePaths(ctx, root, before, after, []string{"tracked.txt", "deleted.txt", "new dir/empty.txt"})
	if err != nil {
		t.Fatal(err)
	}
	diff, err = control.Diff(ctx, root, before, restored)
	if err != nil || !reflect.DeepEqual(diff.Paths, []string{"allowed.txt"}) {
		t.Fatalf("remaining changes = %#v, %v", diff, err)
	}
	for path, want := range map[string]string{"tracked.txt": "initial\n", "deleted.txt": "restore deleted bytes\x00\xff", "prior.txt": "earlier uncommitted work\r\n", "allowed.txt": "keep\n"} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "new dir", "empty.txt")); !os.IsNotExist(err) {
		t.Fatalf("new file survived restore: %v", err)
	}
	// Durable snapshots lose private rollback bytes but must still verify state.
	encoded, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	var decoded git.Snapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := control.EnsureUnchanged(ctx, root, decoded); err != nil {
		t.Fatalf("durable snapshot: %v", err)
	}
}

func contractStaleRestore(t *testing.T, control contractControl, root string) {
	ctx := context.Background()
	before := contractCapture(t, control, root)
	writeGitWorkspaceFile(t, filepath.Join(root, "tracked.txt"), "first edit\n")
	after := contractCapture(t, control, root)
	writeGitWorkspaceFile(t, filepath.Join(root, "tracked.txt"), "later edit\n")
	if _, err := control.RestorePaths(ctx, root, before, after, []string{"tracked.txt"}); !errors.Is(err, git.ErrRepositoryDiverged) {
		t.Fatalf("stale restore = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil || string(contents) != "later edit\n" {
		t.Fatalf("lost later work: %q, %v", contents, err)
	}
}

func contractUnsafeRestore(t *testing.T, control contractControl, root string) {
	ctx := context.Background()
	before := contractCapture(t, control, root)
	if err := os.Remove(filepath.Join(root, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tracked.txt"), 0o700); err != nil {
		t.Fatal(err)
	}
	after := contractCapture(t, control, root)
	if _, err := control.RestorePaths(ctx, root, before, after, []string{"tracked.txt"}); !errors.Is(err, git.ErrRestoreUnsafe) {
		t.Fatalf("unsafe restore = %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "tracked.txt")); err != nil || !info.IsDir() {
		t.Fatalf("unsafe target was changed: %v, %v", info, err)
	}
}

func contractCommitAndDiff(t *testing.T, control contractControl, root string) {
	ctx := context.Background()
	before := contractCapture(t, control, root)
	patch, err := control.AssignmentDiff(ctx, root, before.HeadOID)
	if err != nil || patch != "(no working-tree changes)" {
		t.Fatalf("clean diff = %q, %v", patch, err)
	}
	writeGitWorkspaceFile(t, filepath.Join(root, "new.txt"), "new content\n")
	patch, err = control.AssignmentDiff(ctx, root, before.HeadOID)
	if err != nil || !strings.Contains(patch, "new.txt") || !strings.Contains(patch, "+new content") {
		t.Fatalf("new file diff = %q, %v", patch, err)
	}
	accepted := contractCapture(t, control, root)
	commit, err := control.Commit(ctx, root, "Accepted change\n\nStepan-Operation: test")
	if err != nil {
		t.Fatal(err)
	}
	if commit.CommitID == "" || commit.CommitID == before.HeadOID || commit.ParentCommit != before.HeadOID || commit.Tree != accepted.TreeOID || commit.Worktree.TreeOID != commit.Tree || commit.Worktree.HeadOID != commit.CommitID {
		t.Fatalf("commit = %#v", commit)
	}
	observed, err := control.Observe(ctx, root)
	if err != nil || observed.CommitID != commit.CommitID || observed.ParentCommit != commit.ParentCommit || observed.Message != commit.Message || observed.Tree != commit.Tree {
		t.Fatalf("observation = %#v, %v", observed, err)
	}
	if err := control.EnsureUnchanged(ctx, root, accepted); !errors.Is(err, git.ErrRepositoryDiverged) {
		t.Fatalf("commit must invalidate earlier snapshot: %v", err)
	}
	patch, err = control.AssignmentDiff(ctx, root, commit.CommitID)
	if err != nil || patch != "(no working-tree changes)" {
		t.Fatalf("post-commit diff = %q, %v", patch, err)
	}
}
