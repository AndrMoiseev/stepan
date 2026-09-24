package impl_loop

import (
	"context"
	"errors"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	workcopy "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
	"github.com/AndrMoiseev/stepan/internal/git"
)

func TestCommitAcceptedAssignmentRetriesPendingCommitOnceAfterExplicitResume(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	disk := testfs.New()
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	acceptCommitFixture(t, stateStore, run)

	snapshot, err := disk.Capture(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := CommitPreparationFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	message := "Implement the accepted task"
	response := AgentResponse{Kind: ResponseImplementationReady, Message: &message, Binding: ResponseBinding{
		CallID: "implement", RunID: run.Identity.ID, AssignmentID: "assignment", BriefID: "brief",
		Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList,
	}}
	commitMessage, err := messageForImplementationCommit(run, "assignment", "commit-1", response)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.SetPendingCommitIntent("assignment", implstate.CommitIntent{OperationID: "commit-1", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: commitMessage}); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := run.Pause("waiting for user"); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := run.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	control := &commitControlFake{observation: &workcopy.CommitObservation{
		CommitID: "commit", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: commitMessage,
		Worktree: git.Snapshot{HeadOID: "commit", TreeOID: preparation.Tree},
	}}

	result, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{
		Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1",
		Response: response, Preparation: preparation, Control: control, Observer: disk,
	})
	if err != nil || control.commitCalls != 1 || result.Commit.CommitID != "commit" || run.Assignments[0].Status != implstate.AssignmentCommitted {
		t.Fatalf("resumed pending commit result=%#v error=%v calls=%d run=%#v", result, err, control.commitCalls, run)
	}
}

func TestCommitAcceptedAssignmentDoesNotRetryPendingCommitUntilRunIsExplicitlyResumed(t *testing.T) {
	for _, status := range []implstate.RunStatus{implstate.RunPaused, implstate.RunClosed} {
		t.Run(string(status), func(t *testing.T) {
			repository := newFilesystemWorkspace(t)
			disk := testfs.New()
			run, stateStore, _ := acceptanceReflectionFixture(t, repository)
			defer stateStore.Close()
			acceptCommitFixture(t, stateStore, run)
			snapshot, err := disk.Capture(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			preparation, err := CommitPreparationFromSnapshot(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			intent := implstate.CommitIntent{OperationID: "commit-1", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: "Implement accepted task\n\nStepan-Run: " + string(run.Identity.ID) + "\nStepan-Assignment: assignment\nStepan-Operation: commit-1"}
			if err := run.SetPendingCommitIntent("assignment", intent); err != nil {
				t.Fatal(err)
			}
			if _, err := stateStore.Record(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if status == implstate.RunPaused {
				err = run.Pause("waiting for user")
			} else {
				err = run.Close("user stopped run")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stateStore.Record(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			control := &commitControlFake{observation: &workcopy.CommitObservation{
				CommitID: "commit", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: intent.Message,
				Worktree: git.Snapshot{HeadOID: "commit", TreeOID: preparation.Tree},
			}}

			_, err = CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{
				Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1",
				Preparation: preparation, Control: control, Observer: disk,
			})
			if !errors.Is(err, ErrPendingCommitInactive) || control.commitCalls != 0 {
				t.Fatalf("inactive pending commit error=%v calls=%d", err, control.commitCalls)
			}
		})
	}
}
