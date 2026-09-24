package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	workcopy "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	"github.com/AndrMoiseev/stepan/internal/git"
)

func TestWorkspaceAssignmentDiffPreservesReviewErrorClassification(t *testing.T) {
	_, err := workspaceAssignmentDiff(context.Background(), nil, t.TempDir(), "")
	if !errors.Is(err, ErrInvalidTaskReviewRoute) || !errors.Is(err, workcopy.ErrAssignmentDiff) {
		t.Fatalf("missing review or workspace error classification: %v", err)
	}
}

type ensureErrorWorkspaceControl struct {
	workcopy.Control
	err error
}

func (w ensureErrorWorkspaceControl) EnsureUnchanged(context.Context, string, git.Snapshot) error {
	return w.err
}

func newFilesystemWorkspace(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repository
}
