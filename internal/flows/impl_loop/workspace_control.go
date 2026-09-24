package impl_loop

import (
	"context"
	"errors"
	"fmt"

	workcopy "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	gitworkspace "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/git"
)

func effectiveWorkspaceControl(control workcopy.Control) workcopy.Control {
	if control == nil {
		return gitworkspace.Control{}
	}
	return control
}

// workspaceAssignmentDiff keeps review routing errors in the orchestration layer.
func workspaceAssignmentDiff(ctx context.Context, control workcopy.Control, repository, base string) (string, error) {
	diff, err := effectiveWorkspaceControl(control).AssignmentDiff(ctx, repository, base)
	if errors.Is(err, workcopy.ErrAssignmentDiff) {
		return "", fmt.Errorf("%w: %w", ErrInvalidTaskReviewRoute, err)
	}
	return diff, err
}
