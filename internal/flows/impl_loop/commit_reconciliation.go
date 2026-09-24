package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	workcopy "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	gitworkspace "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/git"
)

// ErrPendingCommitAmbiguous means that Git cannot prove whether the pending
// controller operation made a commit. The run is paused rather than risking a
// second commit or a new implementation attempt.
var ErrPendingCommitAmbiguous = errors.New("pending Git commit is ambiguous")

// ReconcilePendingCommit checks the one durable pending commit intent against
// actual Git before any retry. A matching commit is adopted exactly once; an
// unchanged pre-commit working copy is safe to retry; every other observation
// pauses the run without changing Git or task completion.
type ReconcilePendingCommitInput struct {
	Run          *implstate.Run
	StateStore   *runstore.StateStore
	Repository   string
	AssignmentID implstate.AssignmentID
	Observer     workcopy.CommitObserver
}

type PendingCommitReconciliation struct {
	Intent  implstate.CommitIntent
	Commit  implstate.CommitEvidence
	Adopted bool
	Retry   bool
}

func ReconcilePendingCommit(ctx context.Context, input ReconcilePendingCommitInput) (PendingCommitReconciliation, error) {
	if input.Run == nil || input.StateStore == nil || strings.TrimSpace(input.Repository) == "" || input.AssignmentID == "" {
		return PendingCommitReconciliation{}, fmt.Errorf("%w: run, state store, repository, and assignment are required", ErrAssignmentCommit)
	}
	intent, pending := pendingCommitIntent(input.Run, input.AssignmentID)
	if !pending {
		return PendingCommitReconciliation{}, nil
	}
	if err := requireActiveCommitRun(input.Run); err != nil {
		return PendingCommitReconciliation{Intent: intent}, err
	}
	observer := input.Observer
	if observer == nil {
		observer = gitworkspace.Control{}
	}
	observed, err := observer.Observe(ctx, input.Repository)
	if err != nil {
		return PendingCommitReconciliation{Intent: intent}, pausePendingCommitAmbiguity(ctx, input, fmt.Errorf("read Git state: %w", err))
	}
	if commitMatchesIntent(intent, observed) && commitHasExpectedTrailers(input.Run, input.AssignmentID, observed.Message) && worktreeMatchesCommit(observed) {
		commit := implstate.CommitEvidence{
			OperationID: intent.OperationID, CommitID: observed.CommitID, ParentCommit: observed.ParentCommit,
			Tree: observed.Tree, Message: observed.Message, State: input.Run.CurrentState,
			Basis: implstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration},
		}
		beforeCompletion, err := cloneCommitRun(input.Run)
		if err != nil {
			return PendingCommitReconciliation{Intent: intent}, fmt.Errorf("%w: checkpoint run before adopting Git commit: %v", ErrAssignmentCommit, err)
		}
		if err := input.Run.CommitAssignment(input.AssignmentID, commit); err != nil {
			return PendingCommitReconciliation{Intent: intent}, fmt.Errorf("%w: adopt reconciled Git commit: %v", ErrAssignmentCommit, err)
		}
		if event, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
			restoreUndurableCommitRun(input.Run, beforeCompletion, event)
			return PendingCommitReconciliation{Intent: intent, Commit: commit}, fmt.Errorf("%w: persist reconciled Git commit: %v", ErrAssignmentCommit, err)
		}
		return PendingCommitReconciliation{Intent: intent, Commit: commit, Adopted: true}, nil
	}
	if observed.CommitID == intent.ParentCommit && observed.Worktree.HeadOID == intent.ParentCommit && observed.Worktree.TreeOID == intent.Tree {
		return PendingCommitReconciliation{Intent: intent, Retry: true}, nil
	}
	return PendingCommitReconciliation{Intent: intent}, pausePendingCommitAmbiguity(ctx, input, fmt.Errorf("HEAD=%q parent=%q tree=%q does not prove pending operation %q", observed.CommitID, observed.ParentCommit, observed.Tree, intent.OperationID))
}

func pausePendingCommitAmbiguity(ctx context.Context, input ReconcilePendingCommitInput, cause error) error {
	beforePause, checkpointErr := cloneCommitRun(input.Run)
	if checkpointErr != nil {
		return fmt.Errorf("%w: checkpoint run before pause: %v", ErrAssignmentCommit, checkpointErr)
	}
	if input.Run.Status == implstate.RunActive {
		if err := input.Run.Pause(ErrPendingCommitAmbiguous.Error() + ": " + cause.Error()); err != nil {
			return errors.Join(ErrPendingCommitAmbiguous, cause, fmt.Errorf("pause run: %w", err))
		}
	}
	if event, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		restoreUndurableCommitRun(input.Run, beforePause, event)
		return fmt.Errorf("%w: %w", ErrAssignmentCommit, errors.Join(ErrPendingCommitAmbiguous, cause, fmt.Errorf("persist paused run: %w", err)))
	}
	return fmt.Errorf("%w: %v", ErrPendingCommitAmbiguous, cause)
}
