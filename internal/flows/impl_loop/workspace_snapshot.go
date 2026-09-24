package impl_loop

import (
	"context"
	"errors"
	"fmt"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	workcopy "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	gitworkspace "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/git"
	"github.com/AndrMoiseev/stepan/internal/git"
)

const unexpectedWorkspaceChangePauseReason = "unexpected working copy change between operations"

// CheckWorkspaceBeforeOperation pauses an active run when the working copy no
// longer matches the fingerprint accepted after the previous operation. The
// caller remains responsible for durably recording the mutated run before
// dispatching more work.
func CheckWorkspaceBeforeOperation(ctx context.Context, repository string, expected git.Snapshot, run *implstate.Run) error {
	return CheckWorkspaceBeforeOperationWithControl(ctx, gitworkspace.Control{}, repository, expected, run)
}

// CheckWorkspaceBeforeOperationWithControl verifies the accepted state through
// the workspace seam. It lets orchestration tests exercise pause behavior
// without starting Git for cases that do not test Git itself.
func CheckWorkspaceBeforeOperationWithControl(ctx context.Context, workspace workcopy.Control, repository string, expected git.Snapshot, run *implstate.Run) error {
	if run == nil {
		return errors.New("implementation run is required")
	}
	if err := effectiveWorkspaceControl(workspace).EnsureUnchanged(ctx, repository, expected); err != nil {
		if pauseErr := run.Pause(unexpectedWorkspaceChangePauseReason); pauseErr != nil {
			return fmt.Errorf("pause after unexpected working copy change: %w", pauseErr)
		}
		if !errors.Is(err, git.ErrRepositoryDiverged) {
			return fmt.Errorf("verify working copy: %w", err)
		}
		return git.ErrRepositoryDiverged
	}
	return nil
}
