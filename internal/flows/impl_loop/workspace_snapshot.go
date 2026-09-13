package impl_loop

import (
	"context"
	"errors"
	"fmt"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

const unexpectedWorkspaceChangePauseReason = "unexpected working copy change between operations"

// CheckWorkspaceBeforeOperation pauses an active run when the working copy no
// longer matches the fingerprint accepted after the previous operation. The
// caller remains responsible for durably recording the mutated run before
// dispatching more work.
func CheckWorkspaceBeforeOperation(ctx context.Context, repository string, expected gitsnapshot.Snapshot, run *implementationstate.Run) error {
	if run == nil {
		return errors.New("implementation run is required")
	}
	if err := gitsnapshot.EnsureUnchanged(ctx, repository, expected); err != nil {
		if !errors.Is(err, gitsnapshot.ErrRepositoryDiverged) {
			return fmt.Errorf("verify working copy: %w", err)
		}
		if pauseErr := run.Pause(unexpectedWorkspaceChangePauseReason); pauseErr != nil {
			return fmt.Errorf("pause after unexpected working copy change: %w", pauseErr)
		}
		return gitsnapshot.ErrRepositoryDiverged
	}
	return nil
}
