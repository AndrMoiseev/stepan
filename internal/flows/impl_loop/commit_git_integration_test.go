//go:build git_integration

package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/git"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/openspec"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestGitResumeRetriesPendingCommitAfterHookRefusalWithStagedIndex(t *testing.T) {
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	writeResumeGitSpecification(t, repository)
	writeGitWorkspaceFile(t, filepath.Join(repository, "rules", "rules.md"), "# Rules\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "implementation.txt"), "accepted code\n")
	configuration := resumeTestConfiguration(t, "test-model", "rules/rules.md")
	store := mustControllerStore(t, t.TempDir())
	journal, err := store.Create("resume-hook-refusal")
	if err != nil {
		t.Fatal(err)
	}
	workspace := GitWorkspaceControl{}
	baseline, err := workspace.Capture(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	baselineData, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	baselineRef, err := journal.Publish("baseline", baselineData)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := openspec.Load(repository, "change")
	if err != nil {
		t.Fatal(err)
	}
	specification, err := resumeSpecification(pkg)
	if err != nil {
		t.Fatal(err)
	}
	specRef, err := journal.Publish("specification", specification)
	if err != nil {
		t.Fatal(err)
	}
	configData, err := canonicalResumeConfiguration(configuration)
	if err != nil {
		t.Fatal(err)
	}
	configRef, err := journal.Publish("configuration", configData)
	if err != nil {
		t.Fatal(err)
	}
	tasksRef, err := journal.Publish("tasks", []byte("source tasks"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := implementationstate.NewRun(implementationstate.RunIdentity{ID: journal.ID(), Change: "change", Repository: repository, WorkCopy: repository, Branch: "implementation", BaselineCommit: baseline.HeadOID, BaselineState: baselineRef, Specification: specRef, TaskList: tasksRef, Configuration: configRef}, []implementationstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	stateStore, err := runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	prepareResumeCommitAcceptance(t, run, stateStore, journal)
	writeGitWorkspaceFile(t, filepath.Join(repository, "openspec", "changes", "change", "tasks.md"), "- [x] task\n")
	reflection, err := workspace.Capture(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	recordResumeReflectionEvidence(t, run, stateStore, journal, reflection)
	preparation, err := CommitPreparationFromSnapshot(reflection)
	if err != nil {
		t.Fatal(err)
	}
	message := "Commit accepted task"
	response := commitResponse(run, "commit-1", message)
	writeGitHook(t, repository, "pre-commit", "#!/bin/sh\nexit 1\n")
	if _, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Journal: journal, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: response, Preparation: preparation, Control: GitCommitControl{}}); !errors.Is(err, ErrAssignmentCommit) {
		t.Fatalf("hook refusal = %v", err)
	}
	if run.Status != implementationstate.RunPaused || run.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit {
		t.Fatalf("hook refusal lost pending acceptance: %#v", run)
	}
	writeGitWorkspaceFile(t, filepath.Join(repository, "rules", "rules.md"), "# Updated rules\n")
	if err := os.Remove(filepath.Join(repository, ".git", "hooks", "pre-commit")); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(context.Background(), ResumeInput{Run: run, StateStore: stateStore, Journal: journal, Repository: repository, Workspace: workspace, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }), ConfigurationLoader: func(string) (implementationconfig.Configuration, error) { return configuration, nil }}); err != nil {
		t.Fatal(err)
	}
	if run.Status != implementationstate.RunActive || run.Assignments[0].Status != implementationstate.AssignmentAcceptedAwaitingCommit {
		t.Fatalf("resume invalidated accepted pending commit: %#v", run)
	}
	if intent := run.Assignments[0].Acceptance.PendingCommit; intent.Tree == preparation.Tree || intent.Tree == "" {
		t.Fatalf("resume did not durably refresh pending tree after rules-only edit: %#v", intent)
	}
	result, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Journal: journal, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: response, Preparation: preparation, Control: GitCommitControl{}})
	if err != nil || result.Commit.CommitID == "" {
		t.Fatalf("retry commit result=%#v err=%v", result, err)
	}
	if count := strings.TrimSpace(gitFixture(t, repository, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("commit count=%s, want exactly one retry commit", count)
	}
}

func recordResumeReflectionEvidence(t *testing.T, run *implementationstate.Run, stateStore *runstore.StateStore, journal *runstore.Run, reflection git.Snapshot) {
	t.Helper()
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	if err := run.AddRunOperation(implementationstate.Operation{ID: "reflect-progress", Kind: implementationstate.OperationAgent, Basis: basis, Description: "reflect accepted task progress in tasks.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("reflect-progress"); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordRunAttemptOutcome("reflect-progress", implementationstate.AttemptSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(reflection)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := journal.Publish("reflect-progress-workspace", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(implementationstate.OperationResult{ID: "reflect-progress-result", OperationID: "reflect-progress", Status: implementationstate.ResultSucceeded, State: run.CurrentState, Basis: basis, Evidence: []implementationstate.EvidenceRef{evidence}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

func writeResumeGitSpecification(t *testing.T, repository string) {
	t.Helper()
	writeGitWorkspaceFile(t, filepath.Join(repository, "openspec", "changes", "change", "proposal.md"), "proposal\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "openspec", "changes", "change", "design.md"), "design\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "openspec", "changes", "change", "tasks.md"), "- [ ] task\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "openspec", "changes", "change", "specs", "feature", "spec.md"), "requirement\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "openspec", "specs", "base", "spec.md"), "base\n")
}

func prepareResumeCommitAcceptance(t *testing.T, run *implementationstate.Run, stateStore *runstore.StateStore, journal *runstore.Run) {
	t.Helper()
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	if err := run.AddRunOperation(implementationstate.Operation{ID: "baseline", Kind: implementationstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(implementationstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implementationstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("assignment", []implementationstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief, err := journal.Publish("brief", []byte("brief"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", implementationstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []implementationstate.Operation{{ID: "check", Kind: implementationstate.OperationCheck, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterMandatoryChecks}, {ID: "review", Kind: implementationstate.OperationReview, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterAssignmentReview}} {
		if err := run.AddOperation("assignment", operation); err != nil {
			t.Fatal(err)
		}
		if _, err := run.StartAssignmentAttempt("assignment", operation.ID); err != nil {
			t.Fatal(err)
		}
		if err := run.AddResult("assignment", implementationstate.OperationResult{ID: implementationstate.ResultID(string(operation.ID) + "-result"), OperationID: operation.ID, Status: implementationstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.AcceptAssignment("assignment", implementationstate.AcceptanceEvidence{BriefID: "brief", State: run.CurrentState, Basis: basis, CheckResultIDs: []implementationstate.ResultID{"check-result"}, ReviewResultID: "review-result"}); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

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
	if commits := strings.Fields(gitFixture(t, repository, "rev-list", "--count", "HEAD")); len(commits) != 1 || commits[0] != "2" {
		t.Fatalf("commit count = %q, want one new commit", commits)
	}
	for _, path := range []string{"implementation.txt", "openspec/changes/change/tasks.md"} {
		if got := gitFixture(t, repository, "show", "--format=", "--name-only", "HEAD", "--", path); strings.TrimSpace(got) != path {
			t.Fatalf("commit does not include %q: %q", path, got)
		}
	}
	contents, err := os.ReadFile(tasks)
	if err != nil || !strings.Contains(string(contents), "[x]") {
		t.Fatalf("informational mark = %q, error = %v", contents, err)
	}
	if result.Commit.CommitID != strings.TrimSpace(gitFixture(t, repository, "rev-parse", "HEAD")) {
		t.Fatalf("committed state did not retain actual HEAD: %#v", result.Commit)
	}
}

func TestGitCommitAcceptedAssignmentRetriesPendingCommitOnceAfterExplicitResume(t *testing.T) {
	repository := newGitWorkspace(t)
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	acceptCommitFixture(t, stateStore, run)

	snapshot, err := (GitWorkspaceControl{}).Capture(context.Background(), repository)
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
	if err := run.SetPendingCommitIntent("assignment", implementationstate.CommitIntent{OperationID: "commit-1", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: commitMessage}); err != nil {
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
	control := &commitControlFake{observation: &CommitObservation{
		CommitID: "commit", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: commitMessage,
		Worktree: git.Snapshot{HeadOID: "commit", TreeOID: preparation.Tree},
	}}

	result, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{
		Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1",
		Response: response, Preparation: preparation, Control: control,
	})
	if err != nil || control.commitCalls != 1 || result.Commit.CommitID != "commit" || run.Assignments[0].Status != implementationstate.AssignmentCommitted {
		t.Fatalf("resumed pending commit result=%#v error=%v calls=%d run=%#v", result, err, control.commitCalls, run)
	}
}

func TestGitCommitAcceptedAssignmentDoesNotRetryPendingCommitUntilRunIsExplicitlyResumed(t *testing.T) {
	for _, status := range []implementationstate.RunStatus{implementationstate.RunPaused, implementationstate.RunClosed} {
		t.Run(string(status), func(t *testing.T) {
			repository := newGitWorkspace(t)
			run, stateStore, _ := acceptanceReflectionFixture(t, repository)
			defer stateStore.Close()
			acceptCommitFixture(t, stateStore, run)
			snapshot, err := (GitWorkspaceControl{}).Capture(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			preparation, err := CommitPreparationFromSnapshot(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			intent := implementationstate.CommitIntent{OperationID: "commit-1", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: "Implement accepted task\n\nStepan-Run: " + string(run.Identity.ID) + "\nStepan-Assignment: assignment\nStepan-Operation: commit-1"}
			if err := run.SetPendingCommitIntent("assignment", intent); err != nil {
				t.Fatal(err)
			}
			if _, err := stateStore.Record(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if status == implementationstate.RunPaused {
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
			control := &commitControlFake{observation: &CommitObservation{
				CommitID: "commit", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: intent.Message,
				Worktree: git.Snapshot{HeadOID: "commit", TreeOID: preparation.Tree},
			}}

			_, err = CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{
				Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1",
				Preparation: preparation, Control: control,
			})
			if !errors.Is(err, ErrPendingCommitInactive) || control.commitCalls != 0 {
				t.Fatalf("inactive pending commit error=%v calls=%d", err, control.commitCalls)
			}
		})
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
	if count := strings.TrimSpace(gitFixture(t, repository, "rev-list", "--count", "HEAD")); count != "1" {
		t.Fatalf("hook refusal created or rewrote commits: count=%s", count)
	}
	if status := gitFixture(t, repository, "status", "--porcelain=v1"); !strings.Contains(status, "implementation.txt") {
		t.Fatalf("hook refusal reset accepted work: %q", status)
	}
}

func TestGitCommitReconciliationAdoptsCommitCreatedBeforeResultWasRecorded(t *testing.T) {
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	run, stateStore, journal := acceptanceReflectionFixture(t, repository)
	acceptCommitFixture(t, stateStore, run)

	writeGitWorkspaceFile(t, filepath.Join(repository, "implementation.txt"), "accepted code\n")
	preparation := captureCommitPreparation(t, repository)
	message, err := messageForImplementationCommit(run, "assignment", "commit-1", commitResponse(run, "commit-1", "Implement source task"))
	if err != nil {
		t.Fatal(err)
	}
	intent := implementationstate.CommitIntent{OperationID: "commit-1", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: message}
	if err := run.SetPendingCommitIntent("assignment", intent); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	// This models the crash window: Git succeeds after the durable intent, but
	// before CommitAssignment can write the completed machine state.
	gitFixture(t, repository, "add", "--all")
	gitFixture(t, repository, "commit", "--quiet", "-m", message)
	created := strings.TrimSpace(gitFixture(t, repository, "rev-parse", "HEAD"))
	if err := stateStore.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil {
		t.Fatal(err)
	}
	stateStore, err = runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	result, err := ReconcilePendingCommit(context.Background(), ReconcilePendingCommitInput{
		Run: recovered, StateStore: stateStore, Repository: repository, AssignmentID: "assignment",
	})
	if err != nil || !result.Adopted || result.Commit.CommitID != created {
		t.Fatalf("reconcile created commit result=%#v error=%v", result, err)
	}
	if recovered.Assignments[0].Status != implementationstate.AssignmentCommitted || recovered.LeafStatus["A"] != implementationstate.TaskComplete {
		t.Fatalf("recovered run did not complete exactly the committed assignment: %#v", recovered)
	}
	if count := strings.TrimSpace(gitFixture(t, repository, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("reconciliation created another commit: count=%s", count)
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
	firstCommit := strings.TrimSpace(gitFixture(t, repository, "rev-parse", "HEAD"))
	if run.Status != implementationstate.RunActive || run.Assignments[0].Status != implementationstate.AssignmentActive || run.LeafStatus["A"] != implementationstate.TaskPending || run.Assignments[0].Commit != nil || len(run.Assignments[0].AcceptanceHistory) != 1 {
		t.Fatalf("hook-modified commit was incorrectly completed or paused: %#v", run)
	}
	if got := gitFixture(t, repository, "show", "HEAD:hook.txt"); got != "hook content\n" {
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
	if second.Commit.ParentCommit != firstCommit || strings.TrimSpace(gitFixture(t, repository, "rev-parse", "HEAD^")) != firstCommit {
		t.Fatalf("corrective commit rewrote hook-created commit: first=%s second=%#v", firstCommit, second.Commit)
	}
	if count := strings.TrimSpace(gitFixture(t, repository, "rev-list", "--count", "HEAD")); count != "3" {
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
	firstCommit := strings.TrimSpace(gitFixture(t, repository, "rev-parse", "HEAD"))
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

	// No post-hook edit is made. A fresh check may publish the same snapshot
	// under a new evidence ID, so completion must compare verified digest rather
	// than EvidenceRef equality and must not invoke Git or create an empty child.
	originalState := run.Assignments[0].ReconciledCommits[0].State
	snapshotData, err := journal.Read(originalState)
	if err != nil {
		t.Fatal(err)
	}
	freshState, err := journal.Publish("fresh-check-state-after-reload", snapshotData)
	if err != nil {
		t.Fatal(err)
	}
	if freshState.ID == originalState.ID || freshState.Digest != originalState.Digest {
		t.Fatalf("fresh equivalent state = %#v, original = %#v", freshState, originalState)
	}
	if err := run.ObserveCodeState(freshState); err != nil {
		t.Fatal(err)
	}
	reacceptAfterHook(t, run, stateStore)
	control := &failOnCallCommitControl{}
	completed, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Journal: journal, Repository: repository, AssignmentID: "assignment", OperationID: "commit-2", Response: commitResponse(run, "commit-2", "Reaccept hook result"), Preparation: captureCommitPreparation(t, repository), Control: control})
	if err != nil || completed.Commit.CommitID != firstCommit || completed.Intent.OperationID != "commit-1" {
		t.Fatalf("unchanged reacceptance did not reuse hook commit: error=%v result=%#v", err, completed)
	}
	if control.calls != 0 {
		t.Fatalf("reused hook commit invoked a second Git commit %d times", control.calls)
	}
	if count := strings.TrimSpace(gitFixture(t, repository, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("unchanged reacceptance created a corrective commit: count=%s", count)
	}
	if run.Assignments[0].Status != implementationstate.AssignmentCommitted || run.LeafStatus["A"] != implementationstate.TaskComplete {
		t.Fatalf("reused hook commit did not complete machine status: %#v", run)
	}
}

type failOnCallCommitControl struct{ calls int }

func (control *failOnCallCommitControl) Commit(context.Context, string, string) (CommitObservation, error) {
	control.calls++
	return CommitObservation{}, errors.New("reused hook commit must not invoke Git")
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
