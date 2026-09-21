package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
)

func TestFinalFindingsAppendDurableOrdinaryTasksWithoutReopeningCompletedWork(t *testing.T) {
	fixture := newCompletedFinalFixture(t)
	defer fixture.state.Close()
	if _, err := RunFinalRequiredChecks(context.Background(), FinalRequiredChecks{
		Run: fixture.run, Workspace: &unchangedWorkspaceControl{}, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		Selection: fixture.selection, Runner: fixture.runner, MaxCycles: 3, Operation: "final-checks", Result: "final-checks-result",
	}); err != nil {
		t.Fatal(err)
	}
	reviewRuntime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseChangesRequested)}}}
	review, err := runFinalReviewerTurn(context.Background(), FinalReviewInput{
		Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		CheckResult: "final-checks-result", OperationID: "final-review", ResultID: "final-review-result", CallID: "final-review-call", RoundID: "round-1", Limits: controlledCallLimits(),
	}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: reviewRuntime, thread: "final-reviewer"}, "base")
	if err != nil {
		t.Fatal(err)
	}
	tasksPath := filepath.Join("openspec", "changes", "change", "tasks.md")
	absTasksPath := filepath.Join(fixture.repository, tasksPath)
	if err := os.MkdirAll(filepath.Dir(absTasksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absTasksPath, []byte("- [x] source task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	forgedReview := review
	forgedReview.Response.FindingIDs = []string{"F-forged"}
	if _, err := validateFinalFindingTasksInput(FinalFindingTasksInput{
		Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		Session: &AgentSession{Role: ResponseRoleOrchestrator}, Review: forgedReview, TasksPath: filepath.ToSlash(tasksPath),
		OperationID: "forged-add-final-tasks", ResultID: "forged-add-final-tasks-result", CallID: "forged-add-final-tasks-call", Limits: controlledCallLimits(),
	}); !errors.Is(err, ErrFinalFindingTasks) {
		t.Fatalf("forged final-review links were accepted: %v", err)
	}
	payload := responsePayloadMap(ResponseTasksAdded)
	payload["task_ids"] = []string{"final-fix"}
	payload["task_payloads"] = []string{`{"id":"final-fix","parent_id":"","title":"repair final finding","finding_ids":["F-1"]}`}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator := &controlledCallRuntime{turns: []controlledTurn{{raw: raw, before: func() {
		if err := os.WriteFile(absTasksPath, []byte("- [x] source task\n- [ ] repair final finding\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}}}}
	added, err := AddFinalFindingTasks(context.Background(), FinalFindingTasksInput{
		Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{},
		Session: &AgentSession{Role: ResponseRoleOrchestrator, runtime: orchestrator, thread: "orchestrator"}, Review: review, TasksPath: filepath.ToSlash(tasksPath),
		OperationID: "add-final-tasks", ResultID: "add-final-tasks-result", CallID: "add-final-tasks-call", Limits: controlledCallLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added.Tasks) != 1 || !strings.Contains(orchestrator.messages[0], "F-1") || fixture.run.LeafStatus["task"] != implstate.TaskComplete || fixture.run.LeafStatus["final-fix"] != implstate.TaskPending || fixture.run.FinalReviewRounds != 1 {
		t.Fatalf("final finding did not become an ordinary pending task: added=%#v run=%#v message=%q", added, fixture.run, orchestrator.messages)
	}
	if finding := added.Tasks[0].FinalFindings; len(finding) != 1 || finding[0].ReviewResultID != review.ResultID || finding[0].FindingID != "F-1" {
		t.Fatalf("task provenance = %#v", finding)
	}
	if err := fixture.run.StartAssignment("final-fix-assignment", []implstate.TaskID{"final-fix"}); err != nil {
		t.Fatalf("appended finding bypassed the ordinary assignment loop: %v", err)
	}
	completeFinalFindingAssignment(t, fixture.run, fixture.journal)
	if _, err := RunFinalRequiredChecks(context.Background(), FinalRequiredChecks{
		Run: fixture.run, Workspace: &unchangedWorkspaceControl{}, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		Selection: fixture.selection, Runner: fixture.runner, MaxCycles: 3, Operation: "repeat-final-checks", Result: "repeat-final-checks-result",
	}); err != nil {
		t.Fatalf("corrective task did not return to the final acceptance route: %v", err)
	}
	finalRuntime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseReviewPassed)}}}
	finalReview, err := runFinalReviewerTurn(context.Background(), FinalReviewInput{
		Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		CheckResult: "repeat-final-checks-result", OperationID: "repeat-final-review", ResultID: "repeat-final-review-result", CallID: "repeat-final-review-call", RoundID: "round-2", Limits: controlledCallLimits(),
	}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: finalRuntime, thread: "final-reviewer"}, "base")
	if err != nil {
		t.Fatalf("final acceptance did not repeat after corrective commit: %v", err)
	}
	if err := CompleteFinalAcceptance(context.Background(), fixture.run, fixture.state, fixture.journal, &unchangedWorkspaceControl{}, fixture.repository, "repeat-final-checks-result", finalReview); err != nil {
		t.Fatalf("repeated final acceptance did not complete: %v", err)
	}
	restarted, _, err := fixture.state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if restarted.LeafStatus["task"] != implstate.TaskComplete || restarted.LeafStatus["final-fix"] != implstate.TaskComplete || len(restarted.Tasks[len(restarted.Tasks)-1].FinalFindings) != 1 || restarted.Status != implstate.RunSucceeded {
		t.Fatalf("durable appended tasks = %#v", restarted)
	}
}

func TestFinalFindingTasksMarkdownMustBeAppendOnly(t *testing.T) {
	for name, value := range map[string][]byte{
		"unchanged": {1},
		"rewritten": []byte("- [ ] source\n- [ ] corrective\n"),
		"appended":  []byte("- [x] source\n- [ ] corrective\n"),
	} {
		t.Run(name, func(t *testing.T) {
			err := validateFinalFindingTasksMarkdownAppend([]byte("- [x] source\n"), value)
			if name == "appended" && err != nil {
				t.Fatalf("append rejected: %v", err)
			}
			if name != "appended" && !errors.Is(err, ErrFinalFindingTasks) {
				t.Fatalf("non-append error = %v, want ErrFinalFindingTasks", err)
			}
		})
	}
}

func TestFinalFindingTasksExecutionBlockedPausesAndReloadsAfterOneCall(t *testing.T) {
	fixture, review, tasksPath := newFailedFinalFindingTasksFixture(t)
	defer fixture.state.Close()
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseExecutionBlocked)}}}
	result, err := AddFinalFindingTasks(context.Background(), FinalFindingTasksInput{
		Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{},
		Session: &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "orchestrator"}, Review: review, TasksPath: tasksPath,
		OperationID: "blocked-final-tasks", ResultID: "blocked-final-tasks-result", CallID: "blocked-final-tasks-call", Limits: controlledCallLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Call.Response.Kind != ResponseExecutionBlocked || len(runtime.messages) != 1 || fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || fixture.run.ExecutionBlock.BlockedAction != "run required checks" || len(fixture.run.Tasks) != 1 {
		t.Fatalf("execution_blocked did not durably pause final-task routing: result=%#v run=%#v", result, fixture.run)
	}
	restarted, _, err := fixture.state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Status != implstate.RunPaused || restarted.ExecutionBlock == nil || restarted.ExecutionBlock.Diagnostic != "tool is not installed" || len(restarted.Tasks) != 1 {
		t.Fatalf("execution-blocked final-task route did not reload: %#v", restarted)
	}
}

func TestFinalFindingTasksClarificationClosesAndReloadsAfterOneCall(t *testing.T) {
	fixture, review, tasksPath := newFailedFinalFindingTasksFixture(t)
	defer fixture.state.Close()
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseClarificationNeeded)}}}
	result, err := AddFinalFindingTasks(context.Background(), FinalFindingTasksInput{
		Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{},
		Session: &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "orchestrator"}, Review: review, TasksPath: tasksPath,
		OperationID: "clarify-final-tasks", ResultID: "clarify-final-tasks-result", CallID: "clarify-final-tasks-call", Limits: controlledCallLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Call.Response.Kind != ResponseClarificationNeeded || len(runtime.messages) != 1 || fixture.run.Status != implstate.RunClosed || !strings.Contains(fixture.run.CloseReason, "which behavior is required?") {
		t.Fatalf("clarification did not close final-task routing: result=%#v run=%#v", result, fixture.run)
	}
	restarted, _, err := fixture.state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	durable := finalRunResult(restarted, "clarify-final-tasks-result")
	if restarted.Status != implstate.RunClosed || !strings.Contains(restarted.CloseReason, "boundaries:") || durable == nil || durable.Status != implstate.ResultSucceeded || len(durable.Evidence) != 1 {
		t.Fatalf("clarification final-task route did not durably close: run=%#v result=%#v", restarted, durable)
	}
	receipt, err := fixture.journal.Read(durable.Evidence[0])
	if err != nil || !strings.Contains(string(receipt), "which behavior is required?") {
		t.Fatalf("clarification question was not durable: %q, %v", receipt, err)
	}
}

func newFailedFinalFindingTasksFixture(t *testing.T) (implementerTransitionFixture, FinalReviewResult, string) {
	t.Helper()
	fixture := newCompletedFinalFixture(t)
	if _, err := RunFinalRequiredChecks(context.Background(), FinalRequiredChecks{
		Run: fixture.run, Workspace: &unchangedWorkspaceControl{}, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		Selection: fixture.selection, Runner: fixture.runner, MaxCycles: 3, Operation: "final-checks", Result: "final-checks-result",
	}); err != nil {
		fixture.state.Close()
		t.Fatal(err)
	}
	reviewRuntime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseChangesRequested)}}}
	review, err := runFinalReviewerTurn(context.Background(), FinalReviewInput{
		Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		CheckResult: "final-checks-result", OperationID: "final-review", ResultID: "final-review-result", CallID: "final-review-call", RoundID: "round-1", Limits: controlledCallLimits(),
	}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: reviewRuntime, thread: "final-reviewer"}, "base")
	if err != nil {
		fixture.state.Close()
		t.Fatal(err)
	}
	tasksPath := filepath.Join("openspec", "changes", "change", "tasks.md")
	absoluteTasksPath := filepath.Join(fixture.repository, tasksPath)
	if err := os.MkdirAll(filepath.Dir(absoluteTasksPath), 0o700); err != nil {
		fixture.state.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(absoluteTasksPath, []byte("- [x] source task\n"), 0o600); err != nil {
		fixture.state.Close()
		t.Fatal(err)
	}
	return fixture, review, filepath.ToSlash(tasksPath)
}

func completeFinalFindingAssignment(t *testing.T, run *implstate.Run, journal interface {
	Publish(implstate.EvidenceID, []byte) (implstate.EvidenceRef, error)
},
) {
	t.Helper()
	basis := implstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	brief, err := journal.Publish("final-fix-brief", []byte("correct the final finding"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("final-fix-assignment", implstate.BriefVersion{ID: "final-fix-brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		operation implstate.OperationID
		result    implstate.ResultID
		kind      implstate.OperationKind
	}{{"final-fix-check", "final-fix-check-result", implstate.OperationCheck}, {"final-fix-review", "final-fix-review-result", implstate.OperationReview}} {
		if err := run.AddOperation("final-fix-assignment", implstate.Operation{ID: item.operation, Kind: item.kind, BriefID: "final-fix-brief", Basis: basis}); err != nil {
			t.Fatal(err)
		}
		if _, err := run.StartAssignmentAttempt("final-fix-assignment", item.operation); err != nil {
			t.Fatal(err)
		}
		if err := run.AddResult("final-fix-assignment", implstate.OperationResult{ID: item.result, OperationID: item.operation, Status: implstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	intent := implstate.CommitIntent{OperationID: "final-fix-commit", ParentCommit: "commit", Tree: "tree-after-final-fix", Message: "correct final finding"}
	if err := run.AcceptAssignment("final-fix-assignment", implstate.AcceptanceEvidence{BriefID: "final-fix-brief", State: run.CurrentState, Basis: basis, CheckResultIDs: []implstate.ResultID{"final-fix-check-result"}, ReviewResultID: "final-fix-review-result", PendingCommit: intent}); err != nil {
		t.Fatal(err)
	}
	if err := run.CommitAssignment("final-fix-assignment", implstate.CommitEvidence{OperationID: intent.OperationID, CommitID: "final-fix-commit", ParentCommit: intent.ParentCommit, Tree: intent.Tree, Message: intent.Message, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
}

func TestFinalReviewPausesBeforeFourthRound(t *testing.T) {
	fixture := newCompletedFinalFixture(t)
	defer fixture.state.Close()
	if _, err := RunFinalRequiredChecks(context.Background(), FinalRequiredChecks{
		Run: fixture.run, Workspace: &unchangedWorkspaceControl{}, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		Selection: fixture.selection, Runner: fixture.runner, MaxCycles: 3, Operation: "final-checks", Result: "final-checks-result",
	}); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseChangesRequested)}}}
		_, err := runFinalReviewerTurn(context.Background(), FinalReviewInput{
			Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
			CheckResult: "final-checks-result", OperationID: implstate.OperationID("final-review-" + string(rune('0'+round))), ResultID: implstate.ResultID("final-review-result-" + string(rune('0'+round))), CallID: "final-review-call-" + string(rune('0'+round)), RoundID: "round", Limits: controlledCallLimits(),
		}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: runtime, thread: "final-reviewer"}, "base")
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
	fourth := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseReviewPassed)}}}
	_, err := runFinalReviewerTurn(context.Background(), FinalReviewInput{
		Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		CheckResult: "final-checks-result", OperationID: "final-review-4", ResultID: "final-review-result-4", CallID: "final-review-call-4", RoundID: "round", Limits: controlledCallLimits(),
	}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: fourth, thread: "final-reviewer"}, "base")
	if !errors.Is(err, implstate.ErrLimitExceeded) || fixture.run.Status != implstate.RunPaused || fixture.run.LimitPause == nil || fixture.run.FinalReviewRounds != 3 || len(fourth.messages) != 0 {
		t.Fatalf("fourth final review was not paused before dispatch: error=%v run=%#v messages=%#v", err, fixture.run, fourth.messages)
	}
}
