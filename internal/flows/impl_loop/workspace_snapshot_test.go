package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
	"github.com/AndrMoiseev/stepan/internal/git"
)

func TestCheckWorkspaceBeforeOperationPausesOnUnexpectedChange(t *testing.T) {
	t.Parallel()
	repository := newFilesystemWorkspace(t)
	control := testfs.New()
	expected, err := control.Capture(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	run := &implstate.Run{Status: implstate.RunActive}
	if err := CheckWorkspaceBeforeOperationWithControl(context.Background(), control, repository, expected, run); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "unexpected.txt"), []byte("manual edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckWorkspaceBeforeOperationWithControl(context.Background(), control, repository, expected, run); !errors.Is(err, git.ErrRepositoryDiverged) {
		t.Fatalf("divergence = %v", err)
	}
	if run.Status != implstate.RunPaused || run.PauseReason != unexpectedWorkspaceChangePauseReason {
		t.Fatalf("run was not paused: %#v", run)
	}
}

func TestCheckWorkspaceBeforeOperationPausesOnObservationFailure(t *testing.T) {
	run := &implstate.Run{Status: implstate.RunActive}
	failure := errors.New("cannot inspect workspace")
	control := ensureErrorWorkspaceControl{Control: testfs.New(), err: failure}
	if err := CheckWorkspaceBeforeOperationWithControl(context.Background(), control, t.TempDir(), git.Snapshot{}, run); !errors.Is(err, failure) {
		t.Fatalf("observation failure = %v", err)
	}
	if run.Status != implstate.RunPaused || run.PauseReason != unexpectedWorkspaceChangePauseReason {
		t.Fatalf("run was not paused: %#v", run)
	}
}

func TestCheckWorkspaceBeforeOperationPausesOnSubmoduleCycle(t *testing.T) {
	t.Parallel()
	run := &implstate.Run{Status: implstate.RunActive}
	workspace := ensureErrorWorkspaceControl{
		Control: &unchangedWorkspaceControl{},
		err:     &git.SubmoduleCycleError{Root: "cycle"},
	}
	err := CheckWorkspaceBeforeOperationWithControl(context.Background(), workspace, t.TempDir(), git.Snapshot{}, run)
	if !errors.Is(err, git.ErrSubmoduleCycle) {
		t.Fatalf("error = %v", err)
	}
	if run.Status != implstate.RunPaused || run.PauseReason != unexpectedWorkspaceChangePauseReason {
		t.Fatalf("run was not paused for submodule cycle: %#v", run)
	}
}
