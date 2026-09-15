package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// ErrPendingCommitAmbiguous means that Git cannot prove whether the pending
// controller operation made a commit. The run is paused rather than risking a
// second commit or a new implementation attempt.
var ErrPendingCommitAmbiguous = errors.New("pending Git commit is ambiguous")

// CommitObserver is the read-only Git seam used before retrying a durable
// pending commit operation.
type CommitObserver interface {
	Observe(context.Context, string) (CommitObservation, error)
}

// GitCommitObserver reads the current HEAD commit and its working copy. It
// performs no Git mutation and never disables normal hooks.
type GitCommitObserver struct{}

var _ CommitObserver = GitCommitObserver{}

func (GitCommitObserver) Observe(ctx context.Context, repository string) (CommitObservation, error) {
	commitID, err := runGitMutation(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		return CommitObservation{}, err
	}
	parents, err := runGitMutation(ctx, repository, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return CommitObservation{}, err
	}
	parentFields := strings.Fields(string(parents))
	if len(parentFields) != 1 && len(parentFields) != 2 {
		return CommitObservation{}, errors.New("HEAD has an unexpected parent list")
	}
	if parentFields[0] != strings.TrimSpace(string(commitID)) {
		return CommitObservation{}, errors.New("HEAD parent list does not start with HEAD")
	}
	parent := ""
	if len(parentFields) == 2 {
		parent = parentFields[1]
	}
	tree, err := runGitMutation(ctx, repository, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return CommitObservation{}, err
	}
	message, err := runGitMutation(ctx, repository, "show", "-s", "--format=%B", "HEAD")
	if err != nil {
		return CommitObservation{}, err
	}
	worktree, err := gitsnapshot.Capture(ctx, repository)
	if err != nil {
		return CommitObservation{}, fmt.Errorf("capture working copy: %w", err)
	}
	return CommitObservation{
		CommitID: strings.TrimSpace(string(commitID)), ParentCommit: parent,
		Tree: strings.TrimSpace(string(tree)), Message: strings.TrimRight(string(message), "\r\n"), Worktree: worktree,
	}, nil
}

// ReconcilePendingCommit checks the one durable pending commit intent against
// actual Git before any retry. A matching commit is adopted exactly once; an
// unchanged pre-commit working copy is safe to retry; every other observation
// pauses the run without changing Git or task completion.
type ReconcilePendingCommitInput struct {
	Run          *implementationstate.Run
	StateStore   *runstore.StateStore
	Repository   string
	AssignmentID implementationstate.AssignmentID
	Observer     CommitObserver
}

type PendingCommitReconciliation struct {
	Intent  implementationstate.CommitIntent
	Commit  implementationstate.CommitEvidence
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
	observer := input.Observer
	if observer == nil {
		observer = GitCommitObserver{}
	}
	observed, err := observer.Observe(ctx, input.Repository)
	if err != nil {
		return PendingCommitReconciliation{Intent: intent}, pausePendingCommitAmbiguity(ctx, input, fmt.Errorf("read Git state: %w", err))
	}
	if commitMatchesIntent(intent, observed) && commitHasExpectedTrailers(input.Run, input.AssignmentID, observed.Message) && worktreeMatchesCommit(observed) {
		commit := implementationstate.CommitEvidence{
			OperationID: intent.OperationID, CommitID: observed.CommitID, ParentCommit: observed.ParentCommit,
			Tree: observed.Tree, Message: observed.Message, State: input.Run.CurrentState,
			Basis: implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration},
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
	if input.Run.Status == implementationstate.RunActive {
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
