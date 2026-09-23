//go:build git_integration

package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/git"
)

func TestControlContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := newGitWorkspace(t)
	workspace := Control{}
	before, err := workspace.Capture(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(repository, "generated.go")
	if err := os.WriteFile(generated, []byte("package generated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := workspace.Capture(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	difference, err := workspace.Diff(ctx, repository, before, after)
	if err != nil || len(difference.Paths) != 1 || difference.Paths[0] != "generated.go" {
		t.Fatalf("difference = %#v, error %v", difference, err)
	}
	if err := workspace.EnsureUnchanged(ctx, repository, before); !errors.Is(err, git.ErrRepositoryDiverged) {
		t.Fatalf("changed workspace verification = %v", err)
	}
	restored, err := workspace.RestorePaths(ctx, repository, before, after, []string{"generated.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.EnsureUnchanged(ctx, repository, restored); err != nil {
		t.Fatalf("restored workspace verification = %v", err)
	}
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("untracked file survived targeted restore: %v", err)
	}
	if err := os.WriteFile(generated, []byte("package generated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, err := workspace.AssignmentDiff(ctx, repository, before.HeadOID)
	if err != nil || !strings.Contains(patch, "generated.go") || !strings.Contains(patch, "package generated") {
		t.Fatalf("assignment patch omitted generated file: error=%v\n%s", err, patch)
	}
}
