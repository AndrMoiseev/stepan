package impl_loop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestCommitAcceptedAssignmentPersistsFullIntentBeforeFirstGitMutation(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	run, stateStore, journal := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	acceptCommitFixture(t, stateStore, run)

	message := "Implement the accepted task"
	response := AgentResponse{
		Kind: ResponseImplementationReady, Message: &message,
		Binding: ResponseBinding{CallID: "implement", RunID: run.Identity.ID, AssignmentID: "assignment", BriefID: "brief", Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList},
	}
	preparation := CommitPreparation{ParentCommit: "parent", Tree: "code-and-progress-tree"}
	control := &commitControlFake{}
	// Commit is this fake's first and only Git mutation. Its callback proves
	// the durable record is complete before the mutation starts, rather than
	// merely before a later `git commit` after a staging/preparation call.
	control.onCommit = func(actualMessage string) {
		current, _, err := runstore.ReadJournalCurrent(journal)
		if err != nil {
			control.commitErr = err
			return
		}
		intent := current.Assignments[0].Acceptance.PendingCommit
		if current.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit || intent.ParentCommit != "parent" || intent.Tree != "code-and-progress-tree" || intent.Message != actualMessage {
			control.commitErr = errors.New("commit intent was not durable before Git")
		}
	}

	result, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{
		Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: response, Preparation: preparation, Control: control,
	})
	if err != nil {
		t.Fatal(err)
	}
	if control.commitCalls != 1 {
		t.Fatalf("Git mutation calls = %d, want one commit", control.commitCalls)
	}
	for _, line := range []string{"Implement the accepted task", "Stepan-Run: " + string(run.Identity.ID), "Stepan-Assignment: assignment", "Stepan-Operation: commit-1"} {
		if !strings.Contains(result.Intent.Message, line) {
			t.Fatalf("commit message %q lacks %q", result.Intent.Message, line)
		}
	}
	parentStatus, _ := run.TaskStatus("parent")
	if run.Assignments[0].Status != implementationstate.AssignmentCommitted || run.LeafStatus["A"] != implementationstate.TaskComplete || parentStatus != implementationstate.TaskComplete {
		t.Fatalf("only the actual commit may complete selected tasks and parent: %#v", run)
	}
}

func TestCommitAcceptedAssignmentLeavesAcceptedTasksPendingWhenGitFails(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	acceptCommitFixture(t, stateStore, run)
	message := "Implement the accepted task"
	response := AgentResponse{Kind: ResponseImplementationReady, Message: &message, Binding: ResponseBinding{CallID: "implement", RunID: run.Identity.ID, AssignmentID: "assignment", BriefID: "brief", Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}}
	control := &commitControlFake{commitErr: errors.New("hook rejected commit")}

	_, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: response, Preparation: CommitPreparation{ParentCommit: "parent", Tree: "tree"}, Control: control})
	parentStatus, _ := run.TaskStatus("parent")
	if !errors.Is(err, ErrAssignmentCommit) || run.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit || run.LeafStatus["A"] != implementationstate.TaskAcceptedAwaitingCommit || parentStatus != implementationstate.TaskPending {
		t.Fatalf("failed Git commit changed task completion: error=%v run=%#v", err, run)
	}
}

func TestCommitAcceptedAssignmentRejectsBlankOrUnboundImplementationMessage(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	for _, response := range []AgentResponse{
		{Kind: ResponseImplementationReady, Binding: ResponseBinding{CallID: "implement", RunID: run.Identity.ID, AssignmentID: "assignment", BriefID: "brief", Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}},
		{Kind: ResponseImplementationReady, Message: commitStringPointer("message"), Binding: ResponseBinding{CallID: "implement", RunID: run.Identity.ID, AssignmentID: "other", BriefID: "brief", Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}},
	} {
		_, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: response, Preparation: CommitPreparation{ParentCommit: "parent", Tree: "tree"}, Control: &commitControlFake{}})
		if !errors.Is(err, ErrAssignmentCommit) {
			t.Fatalf("response %#v error = %v", response, err)
		}
	}
}

type commitControlFake struct {
	commitErr   error
	commitCalls int
	onCommit    func(string)
}

func (fake *commitControlFake) Commit(_ context.Context, _ string, message string) (CommitObservation, error) {
	fake.commitCalls++
	if fake.onCommit != nil {
		fake.onCommit(message)
	}
	if fake.commitErr != nil {
		return CommitObservation{}, fake.commitErr
	}
	return CommitObservation{CommitID: "commit", ParentCommit: "parent", Tree: "code-and-progress-tree", Message: message}, nil
}

func commitStringPointer(value string) *string { return &value }

func acceptCommitFixture(t *testing.T, stateStore *runstore.StateStore, run *implementationstate.Run) {
	t.Helper()
	evidence := acceptanceReflectionEvidence(run)
	// The final tree cannot be known until the informational task mark has
	// been written. CommitAcceptedAssignment fills this durable intent later,
	// immediately before it asks Git to stage and commit.
	evidence.PendingCommit = implementationstate.CommitIntent{}
	if err := run.AcceptAssignment("assignment", evidence); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}
