package impl_loop

import (
	"context"
	"errors"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestReconcilePendingCommitAllowsNormalRetryBeforeGitCommit(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	intent := persistPendingCommit(t, run, stateStore)
	observer := &commitObserverFake{observation: CommitObservation{
		CommitID: intent.ParentCommit,
		Worktree: gitsnapshot.Snapshot{HeadOID: intent.ParentCommit, TreeOID: intent.Tree},
	}}

	reconciled, err := ReconcilePendingCommit(context.Background(), ReconcilePendingCommitInput{
		Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reconciled.Retry || reconciled.Adopted || observer.calls != 1 {
		t.Fatalf("pre-commit reconciliation = %#v, observations=%d", reconciled, observer.calls)
	}
	if run.Status != implementationstate.RunActive || run.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit || run.LeafStatus["A"] != implementationstate.TaskAcceptedAwaitingCommit {
		t.Fatalf("safe retry changed accepted state: %#v", run)
	}
}

func TestReconcilePendingCommitAdoptsMatchingCommitOnlyOnce(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	intent := persistPendingCommit(t, run, stateStore)
	observer := &commitObserverFake{observation: CommitObservation{
		CommitID: "created-commit", ParentCommit: intent.ParentCommit, Tree: intent.Tree, Message: intent.Message,
		Worktree: gitsnapshot.Snapshot{HeadOID: "created-commit", TreeOID: intent.Tree},
	}}

	first, err := ReconcilePendingCommit(context.Background(), ReconcilePendingCommitInput{
		Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Adopted || first.Retry || first.Commit.CommitID != "created-commit" {
		t.Fatalf("matching commit was not adopted: %#v", first)
	}
	parent, _ := run.TaskStatus("parent")
	if run.Assignments[0].Status != implementationstate.AssignmentCommitted || run.LeafStatus["A"] != implementationstate.TaskComplete || parent != implementationstate.TaskComplete {
		t.Fatalf("matching commit did not complete machine accounting: %#v", run)
	}
	second, err := ReconcilePendingCommit(context.Background(), ReconcilePendingCommitInput{
		Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", Observer: observer,
	})
	if err != nil || second.Adopted || second.Retry || observer.calls != 1 {
		t.Fatalf("completed commit was reconciled twice: result=%#v error=%v observations=%d", second, err, observer.calls)
	}
}

func TestReconcilePendingCommitPausesWhenGitFactsAreAmbiguous(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	intent := persistPendingCommit(t, run, stateStore)
	observer := &commitObserverFake{observation: CommitObservation{
		CommitID: "unknown-commit", ParentCommit: intent.ParentCommit, Tree: "unexpected-tree", Message: intent.Message,
		Worktree: gitsnapshot.Snapshot{HeadOID: "unknown-commit", TreeOID: "unexpected-tree"},
	}}

	_, err := ReconcilePendingCommit(context.Background(), ReconcilePendingCommitInput{
		Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", Observer: observer,
	})
	if !errors.Is(err, ErrPendingCommitAmbiguous) {
		t.Fatalf("ambiguous Git facts error = %v", err)
	}
	if run.Status != implementationstate.RunPaused || run.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit || run.LeafStatus["A"] != implementationstate.TaskAcceptedAwaitingCommit {
		t.Fatalf("ambiguous reconciliation changed commit accounting: %#v", run)
	}
}

func TestReconcilePendingCommitPausesWhenExactGitFactsLackRequiredTrailer(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	intent := persistPendingCommit(t, run, stateStore)
	intent.Message = "Implement accepted task"
	if err := run.SetPendingCommitIntent("assignment", intent); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	observer := &commitObserverFake{observation: CommitObservation{
		CommitID: "created-commit", ParentCommit: intent.ParentCommit, Tree: intent.Tree, Message: intent.Message,
		Worktree: gitsnapshot.Snapshot{HeadOID: "created-commit", TreeOID: intent.Tree},
	}}

	_, err := ReconcilePendingCommit(context.Background(), ReconcilePendingCommitInput{
		Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", Observer: observer,
	})
	if !errors.Is(err, ErrPendingCommitAmbiguous) {
		t.Fatalf("wrong service trailer error = %v", err)
	}
	if run.Status != implementationstate.RunPaused || run.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit {
		t.Fatalf("wrong trailer changed accounting instead of pausing: %#v", run)
	}
}

func TestReconcilePendingCommitRestoresCallerStateWhenAccountingCannotBePersisted(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	intent := persistPendingCommit(t, run, stateStore)
	if err := stateStore.Close(); err != nil {
		t.Fatal(err)
	}
	observer := &commitObserverFake{observation: CommitObservation{
		CommitID: "created-commit", ParentCommit: intent.ParentCommit, Tree: intent.Tree, Message: intent.Message,
		Worktree: gitsnapshot.Snapshot{HeadOID: "created-commit", TreeOID: intent.Tree},
	}}

	_, err := ReconcilePendingCommit(context.Background(), ReconcilePendingCommitInput{
		Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", Observer: observer,
	})
	if !errors.Is(err, ErrAssignmentCommit) {
		t.Fatalf("reconcile with closed store error = %v", err)
	}
	if run.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit || run.LeafStatus["A"] != implementationstate.TaskAcceptedAwaitingCommit {
		t.Fatalf("undurable accounting advanced caller state: %#v", run)
	}
}

func persistPendingCommit(t *testing.T, run *implementationstate.Run, stateStore *runstore.StateStore) implementationstate.CommitIntent {
	t.Helper()
	acceptCommitFixture(t, stateStore, run)
	intent := implementationstate.CommitIntent{
		OperationID: "commit-1", ParentCommit: "parent", Tree: "accepted-tree",
		Message: "Implement accepted task\n\nStepan-Run: " + string(run.Identity.ID) + "\nStepan-Assignment: assignment\nStepan-Operation: commit-1",
	}
	if err := run.SetPendingCommitIntent("assignment", intent); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return intent
}

type commitObserverFake struct {
	observation CommitObservation
	err         error
	calls       int
}

func (fake *commitObserverFake) Observe(context.Context, string) (CommitObservation, error) {
	fake.calls++
	return fake.observation, fake.err
}
