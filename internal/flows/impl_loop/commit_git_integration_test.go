//go:build git_integration

package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestGitCommitControlCommitsCodeAndInformationalMarkTogether(t *testing.T) {
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	acceptCommitFixture(t, stateStore, run)

	tasks := filepath.Join(repository, "openspec", "changes", "change", "tasks.md")
	writeGitWorkspaceFile(t, tasks, "- [x] source task\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "implementation.txt"), "accepted code\n")
	// In the real flow this is the already-captured post-orchestrator snapshot
	// returned by the controlled call. The commit step only derives facts from
	// it and does no Git work until after StateStore.Record.
	snapshot, err := (GitWorkspaceControl{}).Capture(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := CommitPreparationFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	message := "Implement source task"
	response := AgentResponse{Kind: ResponseImplementationReady, Message: &message, Binding: ResponseBinding{CallID: "implement", RunID: run.Identity.ID, AssignmentID: "assignment", BriefID: "brief", Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}}

	result, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: response, Preparation: preparation, Control: GitCommitControl{}})
	if err != nil {
		t.Fatal(err)
	}
	if commits := strings.Fields(git(t, repository, "rev-list", "--count", "HEAD")); len(commits) != 1 || commits[0] != "2" {
		t.Fatalf("commit count = %q, want one new commit", commits)
	}
	for _, path := range []string{"implementation.txt", "openspec/changes/change/tasks.md"} {
		if got := git(t, repository, "show", "--format=", "--name-only", "HEAD", "--", path); strings.TrimSpace(got) != path {
			t.Fatalf("commit does not include %q: %q", path, got)
		}
	}
	contents, err := os.ReadFile(tasks)
	if err != nil || !strings.Contains(string(contents), "[x]") {
		t.Fatalf("informational mark = %q, error = %v", contents, err)
	}
	if result.Commit.CommitID != strings.TrimSpace(git(t, repository, "rev-parse", "HEAD")) {
		t.Fatalf("committed state did not retain actual HEAD: %#v", result.Commit)
	}
}

func TestGitCommitControlHookRefusalPausesAwaitingCommitWithoutReset(t *testing.T) {
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	run, stateStore, journal := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	acceptCommitFixture(t, stateStore, run)

	writeGitHook(t, repository, "pre-commit", "#!/bin/sh\necho hook rejected >&2\nexit 1\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "implementation.txt"), "accepted code\n")
	preparation := captureCommitPreparation(t, repository)
	message := "Implement source task"
	response := commitResponse(run, "commit-1", message)

	_, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Journal: journal, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: response, Preparation: preparation, Control: GitCommitControl{}})
	if !errors.Is(err, ErrAssignmentCommit) || !strings.Contains(run.PauseReason, "hook rejected") {
		t.Fatalf("hook refusal error=%v pause=%q", err, run.PauseReason)
	}
	parentStatus, _ := run.TaskStatus("parent")
	if run.Status != implementationstate.RunPaused || run.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit || run.LeafStatus["A"] != implementationstate.TaskAcceptedAwaitingCommit || parentStatus != implementationstate.TaskPending {
		t.Fatalf("hook refusal did not preserve awaiting commit state: %#v", run)
	}
	persisted, _, persistErr := runstore.ReadJournalCurrent(journal)
	if persistErr != nil || persisted.Status != implementationstate.RunPaused || persisted.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit || !strings.Contains(persisted.PauseReason, "hook rejected") {
		t.Fatalf("hook refusal pause was not durable: run=%#v error=%v", persisted, persistErr)
	}
	if count := strings.TrimSpace(git(t, repository, "rev-list", "--count", "HEAD")); count != "1" {
		t.Fatalf("hook refusal created or rewrote commits: count=%s", count)
	}
	if status := git(t, repository, "status", "--porcelain=v1"); !strings.Contains(status, "implementation.txt") {
		t.Fatalf("hook refusal reset accepted work: %q", status)
	}
}

func TestGitCommitControlHookChangesRequireNewAcceptanceAndNewCommit(t *testing.T) {
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	run, stateStore, journal := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	acceptCommitFixture(t, stateStore, run)

	writeGitHook(t, repository, "pre-commit", "#!/bin/sh\nprintf 'hook content\\n' > hook.txt\ngit add -- hook.txt\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "implementation.txt"), "accepted code\n")
	message := "Implement source task"
	first, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Journal: journal, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: commitResponse(run, "commit-1", message), Preparation: captureCommitPreparation(t, repository), Control: GitCommitControl{}})
	if !errors.Is(err, ErrCommitReacceptanceRequired) || !first.ReacceptanceRequired {
		t.Fatalf("hook content change error=%v result=%#v", err, first)
	}
	firstCommit := strings.TrimSpace(git(t, repository, "rev-parse", "HEAD"))
	if run.Status != implementationstate.RunActive || run.Assignments[0].Status != implementationstate.AssignmentActive || run.LeafStatus["A"] != implementationstate.TaskPending || run.Assignments[0].Commit != nil || len(run.Assignments[0].AcceptanceHistory) != 1 {
		t.Fatalf("hook-modified commit was incorrectly completed or paused: %#v", run)
	}
	if got := git(t, repository, "show", "HEAD:hook.txt"); got != "hook content\n" {
		t.Fatalf("hook-created commit content = %q", got)
	}

	// The original hook-created commit remains in history. The changed state is
	// accepted again and the correction is recorded by a distinct commit.
	writeGitWorkspaceFile(t, filepath.Join(repository, "implementation.txt"), "accepted and corrected code\n")
	reacceptAfterHook(t, run, stateStore)
	secondMessage := "Correct hook-modified result"
	second, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Journal: journal, Repository: repository, AssignmentID: "assignment", OperationID: "commit-2", Response: commitResponse(run, "commit-2", secondMessage), Preparation: captureCommitPreparation(t, repository), Control: GitCommitControl{}})
	if err != nil || second.Commit.CommitID == "" {
		t.Fatalf("corrective commit error=%v result=%#v", err, second)
	}
	if second.Commit.ParentCommit != firstCommit || strings.TrimSpace(git(t, repository, "rev-parse", "HEAD^")) != firstCommit {
		t.Fatalf("corrective commit rewrote hook-created commit: first=%s second=%#v", firstCommit, second.Commit)
	}
	if count := strings.TrimSpace(git(t, repository, "rev-list", "--count", "HEAD")); count != "3" {
		t.Fatalf("commit count=%s, want initial plus hook and corrective commits", count)
	}
}

func TestGitCommitControlReusesHookCreatedCommitAfterReloadWhenReacceptedUnchanged(t *testing.T) {
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	run, stateStore, journal := acceptanceReflectionFixture(t, repository)
	acceptCommitFixture(t, stateStore, run)

	writeGitHook(t, repository, "pre-commit", "#!/bin/sh\nprintf 'hook content\\n' > hook.txt\ngit add -- hook.txt\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "implementation.txt"), "accepted code\n")
	first, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Journal: journal, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: commitResponse(run, "commit-1", "Implement source task"), Preparation: captureCommitPreparation(t, repository), Control: GitCommitControl{}})
	if !errors.Is(err, ErrCommitReacceptanceRequired) || !first.ReacceptanceRequired {
		t.Fatalf("hook content change error=%v result=%#v", err, first)
	}
	firstCommit := strings.TrimSpace(git(t, repository, "rev-parse", "HEAD"))
	if err := stateStore.Close(); err != nil {
		t.Fatal(err)
	}
	run, _, err = runstore.ReadJournalCurrent(journal)
	if err != nil {
		t.Fatal(err)
	}
	stateStore, err = runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	if len(run.Assignments[0].ReconciledCommits) != 1 || run.Assignments[0].ReconciledCommits[0].CommitID != firstCommit {
		t.Fatalf("reconciled commit did not survive journal reload: %#v", run.Assignments[0])
	}

	// No post-hook edit is made. Fresh acceptance therefore proves the exact
	// hook-created commit and must not invoke Git or create an empty child.
	reacceptAfterHook(t, run, stateStore)
	completed, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Journal: journal, Repository: repository, AssignmentID: "assignment", OperationID: "commit-2", Response: commitResponse(run, "commit-2", "Reaccept hook result"), Preparation: captureCommitPreparation(t, repository), Control: GitCommitControl{}})
	if err != nil || completed.Commit.CommitID != firstCommit || completed.Intent.OperationID != "commit-1" {
		t.Fatalf("unchanged reacceptance did not reuse hook commit: error=%v result=%#v", err, completed)
	}
	if count := strings.TrimSpace(git(t, repository, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("unchanged reacceptance created a corrective commit: count=%s", count)
	}
	if run.Assignments[0].Status != implementationstate.AssignmentCommitted || run.LeafStatus["A"] != implementationstate.TaskComplete {
		t.Fatalf("reused hook commit did not complete machine status: %#v", run)
	}
}

func captureCommitPreparation(t *testing.T, repository string) CommitPreparation {
	t.Helper()
	snapshot, err := (GitWorkspaceControl{}).Capture(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := CommitPreparationFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return preparation
}

func commitResponse(run *implementationstate.Run, operationID implementationstate.OperationID, message string) AgentResponse {
	return AgentResponse{Kind: ResponseImplementationReady, Message: &message, Binding: ResponseBinding{CallID: string(operationID) + "-call", RunID: run.Identity.ID, AssignmentID: "assignment", BriefID: "brief", Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}}
}

func writeGitHook(t *testing.T, repository, name, body string) {
	t.Helper()
	path := filepath.Join(repository, ".git", "hooks", name)
	writeGitWorkspaceFile(t, path, body)
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func reacceptAfterHook(t *testing.T, run *implementationstate.Run, stateStore *runstore.StateStore) {
	t.Helper()
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	for _, operation := range []implementationstate.Operation{
		{ID: "check-after-hook", Kind: implementationstate.OperationCheck, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterMandatoryChecks},
		{ID: "review-after-hook", Kind: implementationstate.OperationReview, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterAssignmentReview},
	} {
		if err := run.AddOperation("assignment", operation); err != nil {
			t.Fatal(err)
		}
		if _, err := run.StartAssignmentAttempt("assignment", operation.ID); err != nil {
			t.Fatal(err)
		}
		if err := run.AddResult("assignment", implementationstate.OperationResult{ID: implementationstate.ResultID(operation.ID + "-result"), OperationID: operation.ID, Status: implementationstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.AcceptAssignment("assignment", implementationstate.AcceptanceEvidence{BriefID: "brief", State: run.CurrentState, Basis: basis, CheckResultIDs: []implementationstate.ResultID{"check-after-hook-result"}, ReviewResultID: "review-after-hook-result"}); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}
