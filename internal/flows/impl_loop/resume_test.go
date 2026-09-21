package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/checkexec"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/git"
	"github.com/AndrMoiseev/stepan/internal/openspec"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

func TestResumeReconcilesPausedRunAndPreservesManualWorkingCopyChanges(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.workspace.actual.TreeOID = "manual-code-tree"
	fixture.workspace.actual.StatusHash = "manual-code-status"
	fixture.workspace.paths = []string{"internal/example.go"}

	result, err := Resume(context.Background(), fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	if fixture.run.Status != implstate.RunActive || !result.WorkspaceChanged {
		t.Fatalf("resume did not retain classified manual change: run=%#v result=%#v", fixture.run, result)
	}
	if fixture.run.CurrentState == fixture.baseline {
		t.Fatal("manual working-copy fingerprint was not retained as current state")
	}
	if fixture.workspace.restores != 0 {
		t.Fatalf("resume restored manual worktree changes %d times", fixture.workspace.restores)
	}
}

func TestDispatchRestartContinuationRestoresFreshOrchestratorFromDurableRun(t *testing.T) {
	fixture := newResumeFixture(t, "")
	factory := &sessionRuntimeFactory{}
	configuration := resumeTestConfiguration(t, "initial-model", "")
	var owner *SessionOwner
	resumeInput := fixture.input()
	resumeInput.Factories = map[string]RuntimeFactory{"test": factory}
	resumeInput.SessionOwner = &owner
	resumeInput.SessionBase = agentruntime.ThreadConfig{Workspace: fixture.repository}
	result, err := Resume(context.Background(), resumeInput)
	if err != nil || owner == nil {
		t.Fatalf("resume = %#v, owner=%p, err=%v", result, owner, err)
	}
	defer func() { _ = owner.Close() }()
	before := len(fixture.run.RunOperations)
	if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
		Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
		Workspace: fixture.workspace, Runner: resumeInput.Runner, Configuration: configuration, Checks: result.Checks, Rules: result.Rules,
		CommitControl: restartCommitControl{parent: "head", tree: "expected-tree"},
	}); err != nil {
		t.Fatalf("dispatch: %v; run=%#v", err, fixture.run)
	}
	if len(factory.configurations()) < 5 {
		t.Fatalf("recovered role sessions = %d, want full orchestrator/assignment/final continuation", len(factory.configurations()))
	}
	if fixture.run.InitialBaselineStatus != implstate.InitialBaselinePassed || len(fixture.run.RunOperations) <= before || fixture.run.Status != implstate.RunSucceeded || fixture.run.FinalAcceptance == nil {
		t.Fatalf("restart did not execute the durable pipeline through final acceptance: %#v", fixture.run)
	}
}

func TestDispatchRestartContinuationPausesDurablyWhenSessionRestoreFails(t *testing.T) {
	fixture := newResumeFixture(t, "")
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	owner, err := NewSessionOwner(PreparedRuntimes{}, agentruntime.ThreadConfig{Workspace: fixture.repository})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	configuration := resumeTestConfiguration(t, "initial-model", "")
	checks, err := configuration.SelectHostChecks()
	if err != nil {
		t.Fatal(err)
	}
	err = DispatchRestartContinuation(context.Background(), RestartContinuationInput{
		Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
		Workspace: fixture.workspace, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		Configuration: configuration, Checks: checks,
	})
	if !errors.Is(err, ErrRestartContinuation) || fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || fixture.run.ExecutionBlock.BlockedAction != "restore orchestrator session" {
		t.Fatalf("restore failure did not durably pause: run=%#v err=%v", fixture.run, err)
	}
	reopened, _, err := runstore.ReadJournalCurrent(fixture.journal)
	if err != nil || reopened.Status != implstate.RunPaused || reopened.ExecutionBlock == nil {
		t.Fatalf("durable restore pause = %#v, %v", reopened, err)
	}
}

func TestDispatchRestartContinuationRestoresAssignmentRolesAndRunsImplementerRoute(t *testing.T) {
	fixture := newResumeFixture(t, "")
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if err := fixture.run.AddRunOperation(implstate.Operation{ID: "baseline", Kind: implstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddRunResult(implstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.StartAssignment("assignment", []implstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := PersistBriefVersion(context.Background(), fixture.journal, fixture.state, fixture.run, "assignment", []implstate.TaskID{"task"}, "Implement the durable task."); err != nil {
		t.Fatal(err)
	}
	factory := &sessionRuntimeFactory{}
	configuration := resumeTestConfiguration(t, "initial-model", "")
	prepared, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{"test": factory})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewSessionOwner(prepared, agentruntime.ThreadConfig{Workspace: fixture.repository})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	checks, err := configuration.SelectHostChecks()
	if err != nil {
		t.Fatal(err)
	}
	var committedMessage string
	if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
		Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
		Workspace: &unchangedWorkspaceControl{}, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		Configuration: configuration, Checks: checks, CommitControl: restartCommitControl{parent: "unchanged", tree: "unchanged", message: &committedMessage},
	}); err != nil {
		t.Fatalf("dispatch: %v; run=%#v", err, fixture.run)
	}
	roles := map[ResponseRole]int{}
	for _, config := range factory.configurations() {
		roles[roleFromBootstrap(config.BootstrapInstructions)]++
	}
	if roles[ResponseRoleOrchestrator] != 1 || roles[ResponseRoleBriefer] != 1 || roles[ResponseRoleImplementer] != 1 || roles[ResponseRoleTaskReviewer] != 1 || roles[ResponseRoleFinalReviewer] != 1 {
		t.Fatalf("restored assignment roles = %#v", roles)
	}
	assignment := assignmentByID(fixture.run, "assignment")
	if assignment == nil || assignment.Status != implstate.AssignmentCommitted || len(assignment.Operations) < 3 || len(assignment.Results) != 3 || fixture.run.Status != implstate.RunSucceeded {
		t.Fatalf("restart did not execute/persist the complete assignment route: assignment=%#v run=%#v", assignment, fixture.run)
	}
	reopened, _, err := runstore.ReadJournalCurrent(fixture.journal)
	if err != nil || reopened.Status != implstate.RunSucceeded || reopened.FinalAcceptance == nil || reopened.Assignments[0].Status != implstate.AssignmentCommitted {
		t.Fatalf("durable assignment continuation = %#v, %v", reopened, err)
	}
}

func TestRestartAssignmentActionUsesLatestDurableOperationAndResult(t *testing.T) {
	state := implstate.EvidenceRef{ID: "state", Digest: "digest"}
	basis := implstate.AcceptanceBasis{Specification: implstate.EvidenceRef{ID: "spec", Digest: "spec"}, Configuration: implstate.EvidenceRef{ID: "config", Digest: "config"}}
	operation := func(id string, kind implstate.OperationKind, counter implstate.CycleCounter) implstate.Operation {
		return implstate.Operation{ID: implstate.OperationID(id), Kind: kind, BriefID: "brief", Basis: basis, Counter: counter}
	}
	result := func(operation string, status implstate.ResultStatus) implstate.OperationResult {
		return implstate.OperationResult{ID: implstate.ResultID(operation + "-result"), OperationID: implstate.OperationID(operation), Status: status, State: state, Basis: basis}
	}
	for _, test := range []struct {
		name        string
		operations  []implstate.Operation
		results     []implstate.OperationResult
		want        restartAssignmentAction
		interrupted bool
		wantError   bool
	}{
		{name: "fresh assignment", want: restartAssignmentImplement},
		{name: "successful mandatory checks", operations: []implstate.Operation{operation("checks", implstate.OperationCheck, implstate.CycleCounterMandatoryChecks)}, results: []implstate.OperationResult{result("checks", implstate.ResultSucceeded)}, want: restartAssignmentReview},
		{name: "failed review", operations: []implstate.Operation{operation("review", implstate.OperationReview, implstate.CycleCounterAssignmentReview)}, results: []implstate.OperationResult{result("review", implstate.ResultFailed)}, want: restartAssignmentImplement},
		{name: "passed review", operations: []implstate.Operation{operation("review", implstate.OperationReview, implstate.CycleCounterAssignmentReview)}, results: []implstate.OperationResult{result("review", implstate.ResultSucceeded)}, want: restartAssignmentAwaitingAcceptance},
		{name: "interrupted implementer", operations: []implstate.Operation{operation("implement", implstate.OperationAgent, implstate.CycleCounterNone)}, want: restartAssignmentImplement, interrupted: true},
		{name: "completed implementer awaiting receipt replay", operations: []implstate.Operation{func() implstate.Operation {
			value := operation("implement", implstate.OperationAgent, implstate.CycleCounterNone)
			value.Attempts = []implstate.OperationAttempt{{Number: 1, Outcome: implstate.AttemptSucceeded}}
			return value
		}()}, want: restartAssignmentImplement, interrupted: true},
		{name: "interrupted check", operations: []implstate.Operation{operation("checks", implstate.OperationCheck, implstate.CycleCounterMandatoryChecks)}, want: restartAssignmentChecks, interrupted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := &implstate.Run{Identity: implstate.RunIdentity{Specification: basis.Specification, Configuration: basis.Configuration}, CurrentState: state, Assignments: []implstate.Assignment{{ID: "assignment", Status: implstate.AssignmentActive, Briefs: []implstate.BriefVersion{{ID: "brief", Number: 1, Document: implstate.EvidenceRef{ID: "brief-document", Digest: "brief"}}}, Operations: test.operations, Results: test.results}}}
			action, interrupted, err := classifyRestartAssignmentAction(run, "assignment")
			if (err != nil) != test.wantError || (!test.wantError && action != test.want) || (interrupted != nil) != test.interrupted {
				t.Fatalf("action=%v interrupted=%#v err=%v", action, interrupted, err)
			}
		})
	}
}

func TestDispatchRestartContinuationResumesInterruptedAssignmentStages(t *testing.T) {
	for _, stage := range []string{"checks", "review", "refinement"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newResumeFixture(t, "")
			prepareRestartActiveAssignment(t, fixture)
			seedImplementationReadyReceipt(t, fixture, "interrupted-stage-implementation", "Preserve this interrupted-stage message")
			basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
			if stage == "review" {
				evidence, err := fixture.journal.Publish("interrupted-review-check-evidence", []byte("required checks passed"))
				if err != nil {
					t.Fatal(err)
				}
				operation := implstate.Operation{ID: "completed-checks", Kind: implstate.OperationCheck, BriefID: "brief-assignment-v1", Basis: basis, Description: "required acceptance checks", Counter: implstate.CycleCounterMandatoryChecks}
				if err := fixture.run.AddOperation("assignment", operation); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.run.StartAssignmentAttempt("assignment", operation.ID); err != nil {
					t.Fatal(err)
				}
				if err := fixture.run.RecordAssignmentAttemptOutcome("assignment", operation.ID, implstate.AttemptSucceeded, ""); err != nil {
					t.Fatal(err)
				}
				if err := fixture.run.AddResult("assignment", implstate.OperationResult{ID: "completed-checks-result", OperationID: operation.ID, Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis, Evidence: []implstate.EvidenceRef{evidence}}); err != nil {
					t.Fatal(err)
				}
			}
			kind, counter, description := implstate.OperationCheck, implstate.CycleCounterMandatoryChecks, "required acceptance checks"
			if stage == "review" {
				kind, counter, description = implstate.OperationReview, implstate.CycleCounterAssignmentReview, "task review"
			}
			if stage == "refinement" {
				kind, counter, description = implstate.OperationAgent, implstate.CycleCounterBriefRefinement, "refine assignment brief"
			}
			interrupted := implstate.Operation{ID: implstate.OperationID("interrupted-" + stage), Kind: kind, BriefID: "brief-assignment-v1", Basis: basis, Description: description, Counter: counter}
			if err := fixture.run.AddOperation("assignment", interrupted); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.run.StartAssignmentAttempt("assignment", interrupted.ID); err != nil {
				t.Fatal(err)
			}
			if err := fixture.run.RecordAssignmentAttemptOutcome("assignment", interrupted.ID, implstate.AttemptInterrupted, "process stopped"); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
				t.Fatal(err)
			}

			owner, configuration, checks := restartTestOwner(t, fixture)
			if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
				Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
				Workspace: &unchangedWorkspaceControl{}, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
				Configuration: configuration, Checks: checks, CommitControl: restartCommitControl{parent: "unchanged", tree: "unchanged"},
			}); err != nil {
				t.Fatalf("dispatch interrupted %s: %v; run=%#v", stage, err, fixture.run)
			}
			operation := assignmentOperation(fixture.run, "assignment", interrupted.ID)
			if fixture.run.Status != implstate.RunSucceeded || operation == nil || len(operation.Attempts) != 2 || operation.Attempts[1].Outcome != implstate.AttemptSucceeded {
				t.Fatalf("interrupted %s did not resume under its durable identity: operation=%#v run=%#v", stage, operation, fixture.run)
			}
		})
	}
}

func TestDispatchRestartContinuationRetriesSafePendingCommit(t *testing.T) {
	fixture := newResumeFixture(t, "")
	preparePendingCommitReflection(t, fixture)
	fixture.run.Assignments[0].Acceptance.PendingCommit.Message = "commit accepted work\n\nStepan-Run: resume-run\nStepan-Assignment: assignment\nStepan-Operation: commit"
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	owner, configuration, checks := restartTestOwner(t, fixture)
	observer := &restartCommitObserver{observation: CommitObservation{CommitID: "head", Tree: "old-head-tree", Worktree: git.Snapshot{HeadOID: "head", TreeOID: "informational-reflection-tree"}}}
	control := &commitControlFake{observation: &CommitObservation{CommitID: "restart-commit", ParentCommit: "head", Tree: "informational-reflection-tree", Worktree: git.Snapshot{HeadOID: "restart-commit", TreeOID: "informational-reflection-tree"}}}
	control.onCommit = func(message string) { control.observation.Message = message }
	if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
		Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
		Workspace: fixture.workspace, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		Configuration: configuration, Checks: checks, CommitControl: control, CommitObserver: observer,
	}); err != nil {
		t.Fatalf("dispatch pending commit: %v; run=%#v", err, fixture.run)
	}
	if fixture.run.Status != implstate.RunSucceeded || fixture.run.Assignments[0].Commit == nil || observer.calls != 1 || control.commitCalls != 1 {
		t.Fatalf("safe pending commit was not reconciled and finished: observer=%d commits=%d run=%#v", observer.calls, control.commitCalls, fixture.run)
	}
}

func TestDispatchRestartContinuationAcceptsDurablePassedReviewWithoutRepeatingImplementation(t *testing.T) {
	fixture := newResumeFixture(t, "")
	prepareRestartActiveAssignment(t, fixture)
	seedImplementationReadyReceipt(t, fixture, "passed-implementation", "Preserve this exact passed-review message")
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	checkEvidence, err := fixture.journal.Publish("passed-review-check-evidence", []byte("required checks passed"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		operation implstate.Operation
		result    implstate.OperationResult
	}{
		{operation: implstate.Operation{ID: "passed-checks", Kind: implstate.OperationCheck, BriefID: "brief-assignment-v1", Basis: basis, Description: "required acceptance checks", Counter: implstate.CycleCounterMandatoryChecks}, result: implstate.OperationResult{ID: "passed-checks-result", OperationID: "passed-checks", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis, Evidence: []implstate.EvidenceRef{checkEvidence}}},
		{operation: implstate.Operation{ID: "passed-review", Kind: implstate.OperationReview, BriefID: "brief-assignment-v1", Basis: basis, Description: "task review", Counter: implstate.CycleCounterAssignmentReview}, result: implstate.OperationResult{ID: "passed-review-result", OperationID: "passed-review", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}},
	} {
		if err := fixture.run.AddOperation("assignment", item.operation); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.run.StartAssignmentAttempt("assignment", item.operation.ID); err != nil {
			t.Fatal(err)
		}
		if err := fixture.run.RecordAssignmentAttemptOutcome("assignment", item.operation.ID, implstate.AttemptSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		if err := fixture.run.AddResult("assignment", item.result); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.run.RecordTaskReview("assignment", implstate.TaskReviewRecord{OperationID: "passed-review", ResultID: "passed-review-result", Round: 1, State: fixture.run.CurrentState, Discussion: "review passed before restart"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}

	factory := &sessionRuntimeFactory{}
	configuration := resumeTestConfiguration(t, "initial-model", "")
	prepared, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{"test": factory})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewSessionOwner(prepared, agentruntime.ThreadConfig{Workspace: fixture.repository})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	checks, err := configuration.SelectHostChecks()
	if err != nil {
		t.Fatal(err)
	}
	var committedMessage string
	if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
		Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
		Workspace: &unchangedWorkspaceControl{}, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		Configuration: configuration, Checks: checks, CommitControl: restartCommitControl{parent: "unchanged", tree: "unchanged", message: &committedMessage},
	}); err != nil {
		t.Fatalf("dispatch passed review: %v; run=%#v", err, fixture.run)
	}
	implementerTurns := 0
	for _, runtime := range factory.runtimes {
		for _, config := range runtime.configurations {
			if roleFromBootstrap(config.BootstrapInstructions) == ResponseRoleImplementer {
				implementerTurns += runtime.turnCount
			}
		}
	}
	if fixture.run.Status != implstate.RunSucceeded || fixture.run.FinalAcceptance == nil || implementerTurns != 0 || !strings.HasPrefix(committedMessage, "Preserve this exact passed-review message\n\n") {
		t.Fatalf("durable review was not accepted directly: implementer_turns=%d message=%q run=%#v", implementerTurns, committedMessage, fixture.run)
	}
}

func TestDispatchRestartContinuationRecoversPublishedImplementationReadyWithoutRepeatingTurn(t *testing.T) {
	fixture := newResumeFixture(t, "")
	prepareRestartActiveAssignment(t, fixture)
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	operation := implstate.Operation{ID: "completed-implementation", Kind: implstate.OperationAgent, BriefID: "brief-assignment-v1", Basis: basis, Description: "continue implementation after restart"}
	if err := fixture.run.AddOperation("assignment", operation); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartAssignmentAttempt("assignment", operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordAssignmentAttemptOutcome("assignment", operation.ID, implstate.AttemptSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	message := "Keep the exact crash-boundary commit subject"
	response := AgentResponse{Kind: ResponseImplementationReady, Message: &message, Binding: ResponseBinding{CallID: "completed-implementation-call-1", RunID: fixture.run.Identity.ID, AssignmentID: "assignment", BriefID: operation.BriefID, Specification: basis.Specification, Configuration: basis.Configuration, TaskList: fixture.run.Identity.TaskList}}
	if _, _, err := publishControlledAgentSuccessReceipt(fixture.journal, operation.ID, response, fixture.workspace.actual); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}

	factory := &sessionRuntimeFactory{}
	configuration := resumeTestConfiguration(t, "initial-model", "")
	prepared, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{"test": factory})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewSessionOwner(prepared, threadConfigForTest(fixture.repository))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	checks, err := configuration.SelectHostChecks()
	if err != nil {
		t.Fatal(err)
	}
	var committedMessage string
	if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
		Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
		Workspace: fixture.workspace, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		Configuration: configuration, Checks: checks, CommitControl: restartCommitControl{parent: "head", tree: "expected-tree", message: &committedMessage},
	}); err != nil {
		t.Fatalf("recover published implementation_ready: %v; run=%#v", err, fixture.run)
	}
	result := assignmentResultForOperation(fixture.run, "assignment", operation.ID)
	if fixture.run.Status != implstate.RunSucceeded || result == nil || countRoleTurns(factory, ResponseRoleImplementer) != 0 || !strings.HasPrefix(committedMessage, message+"\n\n") {
		t.Fatalf("published response was not recovered exactly: result=%#v turns=%d message=%q run=%#v", result, countRoleTurns(factory, ResponseRoleImplementer), committedMessage, fixture.run)
	}
}

func TestDispatchRestartContinuationRecoversControlledCallCrashBoundary(t *testing.T) {
	fixture := newResumeFixture(t, "")
	prepareRestartActiveAssignment(t, fixture)
	factory := &sessionRuntimeFactory{}
	configuration := resumeTestConfiguration(t, "initial-model", "")
	prepared, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{"test": factory})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewSessionOwner(prepared, threadConfigForTest(fixture.repository))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	checks, err := configuration.SelectHostChecks()
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("injected process loss after accepted-turn receipt")
	err = DispatchRestartContinuation(context.Background(), RestartContinuationInput{
		Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
		Workspace: fixture.workspace, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		Configuration: configuration, Checks: checks, AfterAgentSuccessReceipt: func() error { return crash },
	})
	if !errors.Is(err, crash) || fixture.run.Status != implstate.RunPaused || countRoleTurns(factory, ResponseRoleImplementer) != 1 {
		t.Fatalf("injected accepted-turn crash = err=%v turns=%d run=%#v", err, countRoleTurns(factory, ResponseRoleImplementer), fixture.run)
	}
	operation := fixture.run.Assignments[0].Operations[len(fixture.run.Assignments[0].Operations)-1]
	if len(operation.Attempts) != 1 || operation.Attempts[0].Outcome != "" {
		t.Fatalf("crashed controlled call was marked succeeded: %#v", operation)
	}
	response, _, _, _, found, err := readControlledAgentSuccessReceipt(fixture.journal, operation.ID)
	if err != nil || !found || response.Message == nil || *response.Message != "implement feature" {
		t.Fatalf("crash receipt = response %#v found=%t err=%v", response, found, err)
	}
	if _, err := Resume(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	var committedMessage string
	if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
		Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
		Workspace: fixture.workspace, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		Configuration: configuration, Checks: checks, CommitControl: restartCommitControl{parent: "head", tree: "expected-tree", message: &committedMessage},
	}); err != nil {
		t.Fatalf("restart after controlled-call crash: %v; run=%#v", err, fixture.run)
	}
	if fixture.run.Status != implstate.RunSucceeded || countRoleTurns(factory, ResponseRoleImplementer) != 1 || !strings.HasPrefix(committedMessage, "implement feature\n\n") {
		t.Fatalf("controlled-call receipt was not resumed idempotently: turns=%d message=%q run=%#v", countRoleTurns(factory, ResponseRoleImplementer), committedMessage, fixture.run)
	}
}

func TestDispatchRestartContinuationRoutesSucceededReceiptKindsWithoutRepeatingTurn(t *testing.T) {
	for _, test := range []struct {
		name     string
		response map[string]any
		assert   func(*testing.T, *resumeFixture)
	}{
		{
			name:     "checks requested",
			response: responsePayloadMap(ResponseChecksRequested),
			assert: func(t *testing.T, fixture *resumeFixture) {
				t.Helper()
				assignment := assignmentByID(fixture.run, "assignment")
				if fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || assignment == nil || len(assignment.Results) != 1 || assignment.Results[0].Status != implstate.ResultFailed {
					t.Fatalf("replayed checks_requested did not use normal check transition: assignment=%#v run=%#v", assignment, fixture.run)
				}
			},
		},
		{
			name:     "execution blocked",
			response: responsePayloadMap(ResponseExecutionBlocked),
			assert: func(t *testing.T, fixture *resumeFixture) {
				t.Helper()
				if fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || fixture.run.ExecutionBlock.BlockedAction != "run required checks" || fixture.run.ExecutionBlock.Diagnostic != "tool is not installed" {
					t.Fatalf("replayed execution_blocked lost exact durable effect: %#v", fixture.run)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResumeFixture(t, "")
			prepareRestartActiveAssignment(t, fixture)
			factory := &sessionRuntimeFactory{responses: map[ResponseRole]map[string]any{ResponseRoleImplementer: test.response}}
			configuration := resumeTestConfiguration(t, "initial-model", "")
			prepared, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{"test": factory})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := NewSessionOwner(prepared, threadConfigForTest(fixture.repository))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			checks, err := configuration.SelectHostChecks()
			if err != nil {
				t.Fatal(err)
			}
			runner := CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
				return checkexec.Result{ExitCode: -1, Failure: checkexec.FailureInfrastructure}, errors.New("injected check infrastructure failure")
			})
			crash := errors.New("injected process loss after durable attempt success")
			err = DispatchRestartContinuation(context.Background(), RestartContinuationInput{
				Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
				Workspace: fixture.workspace, Runner: runner, Configuration: configuration, Checks: checks,
				AfterAgentAttemptSucceeded: func() error { return crash },
			})
			if !errors.Is(err, crash) || fixture.run.Status != implstate.RunPaused || countRoleTurns(factory, ResponseRoleImplementer) != 1 {
				t.Fatalf("post-success crash = err=%v turns=%d run=%#v", err, countRoleTurns(factory, ResponseRoleImplementer), fixture.run)
			}
			operation := fixture.run.Assignments[0].Operations[len(fixture.run.Assignments[0].Operations)-1]
			if len(operation.Attempts) != 1 || operation.Attempts[0].Outcome != implstate.AttemptSucceeded || assignmentResultForOperation(fixture.run, "assignment", operation.ID) != nil {
				t.Fatalf("crash boundary did not retain succeeded receipt-only operation: %#v", operation)
			}
			if _, err := Resume(context.Background(), fixture.input()); err != nil {
				t.Fatal(err)
			}
			if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
				Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
				Workspace: fixture.workspace, Runner: runner, Configuration: configuration, Checks: checks,
			}); err != nil {
				t.Fatalf("receipt replay: %v; run=%#v", err, fixture.run)
			}
			if countRoleTurns(factory, ResponseRoleImplementer) != 1 {
				t.Fatalf("completed implementer turn repeated: %d", countRoleTurns(factory, ResponseRoleImplementer))
			}
			test.assert(t, fixture)
		})
	}
}

func TestDispatchRestartContinuationPausesSucceededImplementationWithoutReceipt(t *testing.T) {
	fixture := newResumeFixture(t, "")
	prepareRestartActiveAssignment(t, fixture)
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	operation := implstate.Operation{ID: "ambiguous-implementation", Kind: implstate.OperationAgent, BriefID: "brief-assignment-v1", Basis: basis, Description: "continue implementation after restart"}
	if err := fixture.run.AddOperation("assignment", operation); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartAssignmentAttempt("assignment", operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordAssignmentAttemptOutcome("assignment", operation.ID, implstate.AttemptSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	factory := &sessionRuntimeFactory{}
	configuration := resumeTestConfiguration(t, "initial-model", "")
	prepared, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{"test": factory})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewSessionOwner(prepared, threadConfigForTest(fixture.repository))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	checks, err := configuration.SelectHostChecks()
	if err != nil {
		t.Fatal(err)
	}
	err = DispatchRestartContinuation(context.Background(), RestartContinuationInput{Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository, Workspace: fixture.workspace, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }), Configuration: configuration, Checks: checks})
	if !errors.Is(err, ErrRestartContinuation) || fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || countRoleTurns(factory, ResponseRoleImplementer) != 0 {
		t.Fatalf("ambiguous completed turn was repeated or not paused: err=%v turns=%d run=%#v", err, countRoleTurns(factory, ResponseRoleImplementer), fixture.run)
	}
}

func TestDispatchRestartContinuationResumesInterruptedProgressReflection(t *testing.T) {
	fixture := newResumeFixture(t, "")
	prepareAcceptedRestartAssignment(t, fixture)
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	operation := implstate.Operation{ID: "interrupted-reflection", Kind: implstate.OperationAgent, Basis: basis, Description: "reflect accepted task progress in tasks.md"}
	if err := fixture.run.AddRunOperation(operation); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartRunAttempt(operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordRunAttemptOutcome(operation.ID, implstate.AttemptInterrupted, "process stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	owner, configuration, checks := restartTestOwner(t, fixture)
	if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
		Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
		Workspace: &unchangedWorkspaceControl{}, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		Configuration: configuration, Checks: checks, CommitControl: restartCommitControl{parent: "unchanged", tree: "unchanged"},
	}); err != nil {
		t.Fatalf("dispatch interrupted reflection: %v; run=%#v", err, fixture.run)
	}
	recovered := finalRunOperation(fixture.run, operation.ID)
	if fixture.run.Status != implstate.RunSucceeded || recovered == nil || len(recovered.Attempts) != 2 || recovered.Attempts[1].Outcome != implstate.AttemptSucceeded {
		t.Fatalf("reflection did not resume under durable identity: operation=%#v run=%#v", recovered, fixture.run)
	}
}

func TestDispatchRestartContinuationResumesInterruptedFinalStages(t *testing.T) {
	for _, stage := range []string{"checks", "review"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newResumeFixture(t, "")
			preparePendingCommitReflection(t, fixture)
			if err := fixture.run.Resume(); err != nil {
				t.Fatal(err)
			}
			assignment := &fixture.run.Assignments[0]
			intent := assignment.Acceptance.PendingCommit
			basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
			if err := fixture.run.CommitAssignment(assignment.ID, implstate.CommitEvidence{OperationID: intent.OperationID, CommitID: "committed", ParentCommit: intent.ParentCommit, Tree: intent.Tree, Message: intent.Message, State: fixture.run.CurrentState, Basis: basis}); err != nil {
				t.Fatal(err)
			}
			configuration := resumeTestConfiguration(t, "initial-model", "")
			checks, err := configuration.SelectHostChecks()
			if err != nil {
				t.Fatal(err)
			}
			if stage == "review" {
				if _, err := RunFinalRequiredChecks(context.Background(), FinalRequiredChecks{Run: fixture.run, Workspace: fixture.workspace, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Selection: checks, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }), MaxCycles: 3, Operation: "completed-final-checks", Result: "completed-final-checks-result"}); err != nil {
					t.Fatal(err)
				}
			}
			kind, counter, description := implstate.OperationCheck, implstate.CycleCounterNone, "final required checks"
			if stage == "review" {
				kind, counter, description = implstate.OperationReview, implstate.CycleCounterFinalReview, "independent final review"
			}
			interrupted := implstate.Operation{ID: implstate.OperationID("interrupted-final-" + stage), Kind: kind, Basis: basis, Description: description, Counter: counter}
			if err := fixture.run.AddRunOperation(interrupted); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.run.StartRunAttempt(interrupted.ID); err != nil {
				t.Fatal(err)
			}
			if err := fixture.run.RecordRunAttemptOutcome(interrupted.ID, implstate.AttemptInterrupted, "process stopped"); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
				t.Fatal(err)
			}
			owner, _, _ := restartTestOwner(t, fixture)
			if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
				Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
				Workspace: fixture.workspace, Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }), Configuration: configuration, Checks: checks,
			}); err != nil {
				t.Fatalf("dispatch interrupted final %s: %v; run=%#v", stage, err, fixture.run)
			}
			recovered := finalRunOperation(fixture.run, interrupted.ID)
			if fixture.run.Status != implstate.RunSucceeded || recovered == nil || len(recovered.Attempts) != 2 || recovered.Attempts[1].Outcome != implstate.AttemptSucceeded {
				t.Fatalf("final %s did not resume under durable identity: operation=%#v run=%#v", stage, recovered, fixture.run)
			}
		})
	}
}

func TestResumeRunsEntireRequiredSetWithoutConsumingAttempts(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.load = func(string) (setting.Configuration, error) {
		return resumeChecksConfiguration(t, `{
"lint":{"kind":"lint","command":{"program":"lint","args":[]}},
"test_all":{"kind":"tests","command":{"program":"test_all","args":[]}},
"build":{"kind":"build","command":{"program":"build","args":[]}}}`, `["lint","test_all","build"]`), nil
	}
	var calls []string
	input := fixture.input()
	input.Runner = CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		calls = append(calls, command.Program)
		return checkexec.Result{}, nil
	})

	result, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"lint", "test_all", "build"}; !slices.Equal(got, want) {
		t.Fatalf("resume required-check order = %#v, want %#v", got, want)
	}
	if fixture.run.Status != implstate.RunActive || !result.ResumeChecks.Succeeded() || result.ResumeCheckEvidence.ID == "" {
		t.Fatalf("resume did not return active only after fresh required evidence: result=%#v run=%#v", result, fixture.run)
	}
	operation := fixture.run.RunOperations[len(fixture.run.RunOperations)-1]
	if !operation.UncountedResumeCheck || operation.Counter != implstate.CycleCounterNone || len(operation.Attempts) != 0 {
		t.Fatalf("resume checks consumed attempts: %#v", operation)
	}
	if len(fixture.run.RunResults) != 1 || fixture.run.RunResults[0].Status != implstate.ResultSucceeded {
		t.Fatalf("resume checks did not retain successful result: %#v", fixture.run.RunResults)
	}
}

func TestResumeSkipsBaselineWhileInterruptedTaskExtractionRemainsPending(t *testing.T) {
	fixture := newPendingExtractionResumeFixture(t)
	result, err := Resume(context.Background(), fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	if fixture.run.Status != implstate.RunActive || !fixture.run.TaskExtractionPending || fixture.run.InitialBaseline != nil || !result.ResumeChecks.Succeeded() {
		t.Fatalf("resume consumed the task-extraction stage with a baseline: result=%#v run=%#v", result, fixture.run)
	}
	payload := responsePayloadMap(ResponseTasksExtracted)
	payload["task_ids"] = []string{"A", "A1"}
	payload["task_payloads"] = []string{`{"id":"A","parent_id":"","title":"A"}`, `{"id":"A1","parent_id":"A","title":"A1"}`}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: raw}}}
	session := &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "orchestrator", restart: func(context.Context) (*AgentSession, error) {
		return &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "orchestrator"}, nil
	}}
	if err := runRestartTaskExtraction(context.Background(), RestartContinuationInput{Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Workspace: fixture.workspace}, session, openspec.Package{}, controlledCallLimits()); err != nil {
		t.Fatal(err)
	}
	operation := finalRunOperation(fixture.run, "extract")
	if fixture.run.TaskExtractionPending || fixture.run.InitialBaseline != nil || !slices.Equal(fixture.run.PendingLeafTasks(), []implstate.TaskID{"A1"}) || operation == nil || len(operation.Attempts) != 2 || operation.Attempts[1].Outcome != implstate.AttemptSucceeded {
		t.Fatalf("restart did not reach and finish interrupted extraction: operation=%#v run=%#v", operation, fixture.run)
	}
}

func TestRestartSupersedesInterruptedTaskExtractionAfterCompatibleRefresh(t *testing.T) {
	fixture := newPendingExtractionResumeFixture(t)
	oldBasis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	oldResponse := AgentResponse{Kind: ResponseTasksExtracted, TaskIDs: []implstate.TaskID{"OLD"}, TaskPayloads: []string{`{"id":"OLD","title":"stale"}`}, Binding: ResponseBinding{CallID: restartOperationCallID(finalRunOperation(fixture.run, "extract")), RunID: fixture.run.Identity.ID, Specification: oldBasis.Specification, Configuration: oldBasis.Configuration, TaskList: fixture.run.Identity.TaskList}}
	if _, _, err := publishControlledAgentSuccessReceipt(fixture.journal, "extract", oldResponse, fixture.workspace.actual); err != nil {
		t.Fatal(err)
	}
	configureAcceptanceRefresh(t, fixture, "compatible specification")
	if _, err := Resume(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	payload := responsePayloadMap(ResponseTasksExtracted)
	payload["task_ids"] = []string{"A", "A1"}
	payload["task_payloads"] = []string{`{"id":"A","parent_id":"","title":"A"}`, `{"id":"A1","parent_id":"A","title":"A1"}`}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: raw}}}
	session := &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "orchestrator", restart: func(context.Context) (*AgentSession, error) {
		return &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "orchestrator"}, nil
	}}
	if err := runRestartTaskExtraction(context.Background(), RestartContinuationInput{Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Workspace: fixture.workspace}, session, openspec.Package{}, controlledCallLimits()); err != nil {
		t.Fatal(err)
	}
	old := finalRunOperation(fixture.run, "extract")
	replacement := &fixture.run.RunOperations[len(fixture.run.RunOperations)-1]
	currentBasis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if old == nil || replacement == nil || replacement.ID == old.ID || replacement.Supersedes != old.ID || old.Basis != oldBasis || replacement.Basis != currentBasis || len(old.Attempts) != 1 || len(replacement.Attempts) != 1 || replacement.Attempts[0].Outcome != implstate.AttemptSucceeded || len(runtime.messages) != 1 || !slices.Equal(fixture.run.PendingLeafTasks(), []implstate.TaskID{"A1"}) {
		t.Fatalf("stale extraction was not superseded exactly once: old=%#v replacement=%#v turns=%d", old, replacement, len(runtime.messages))
	}
}

func TestRestartSupersedesInterruptedBriefSelectionAfterCompatibleRefresh(t *testing.T) {
	fixture := newResumeFixture(t, "")
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if err := fixture.run.AddRunOperation(implstate.Operation{ID: "baseline", Kind: implstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddRunResult(implstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := PrepareBriefSelection(context.Background(), fixture.state, fixture.run, "select-old"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.state.RecordRunAttemptStart(context.Background(), fixture.run, "select-old"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.RecordRunAttemptOutcome(context.Background(), fixture.run, "select-old", implstate.AttemptInterrupted, "process stopped"); err != nil {
		t.Fatal(err)
	}
	staleBrief := "STALE BRIEF MUST NOT BE USED"
	oldSelection := AgentResponse{Kind: ResponseBriefReady, TaskIDs: []implstate.TaskID{"task"}, Brief: &staleBrief, Binding: ResponseBinding{CallID: restartOperationCallID(finalRunOperation(fixture.run, "select-old")), RunID: fixture.run.Identity.ID, Specification: basis.Specification, Configuration: basis.Configuration, TaskList: fixture.run.Identity.TaskList}}
	if _, _, err := publishControlledAgentSuccessReceipt(fixture.journal, "select-old", oldSelection, fixture.workspace.actual); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.Pause("process stopped during selection"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	configureAcceptanceRefresh(t, fixture, "compatible specification")
	resumed, err := Resume(context.Background(), fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	factory := &sessionRuntimeFactory{}
	prepared, err := PrepareRuntimes(resumed.Configuration, map[string]RuntimeFactory{"test": factory})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewSessionOwner(prepared, agentruntime.ThreadConfig{Workspace: fixture.repository})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	if err := runRestartBriefSelection(context.Background(), RestartContinuationInput{Owner: owner, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Workspace: fixture.workspace}, controlledCallLimits(), time.Second); err != nil {
		t.Fatal(err)
	}
	old := finalRunOperation(fixture.run, "select-old")
	replacement := latestRunOperation(fixture.run, "select next assignment")
	currentBasis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if old == nil || replacement == nil || replacement.ID == old.ID || replacement.Supersedes != old.ID || old.Basis != basis || replacement.Basis != currentBasis || len(old.Attempts) != 1 || len(replacement.Attempts) != 1 || countRoleTurns(factory, ResponseRoleBriefer) != 1 || len(fixture.run.Assignments) != 1 {
		t.Fatalf("stale selection was not superseded exactly once: old=%#v replacement=%#v assignments=%#v", old, replacement, fixture.run.Assignments)
	}
	brief, briefErr := currentAssignmentBrief(fixture.journal, fixture.run, fixture.run.Assignments[0].ID)
	if briefErr != nil || brief.Text == staleBrief {
		t.Fatalf("old-basis selection receipt was applied: brief=%#v err=%v", brief, briefErr)
	}
}

func TestResumeRefreshesInitialBaselineAfterSameBasisManualStateEdit(t *testing.T) {
	fixture := newResumeFixture(t, "")
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if err := fixture.run.AddRunOperation(implstate.Operation{ID: "original-baseline", Kind: implstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartRunAttempt("original-baseline"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddRunResult(implstate.OperationResult{ID: "original-baseline-result", OperationID: "original-baseline", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordInitialBaselinePass("original-baseline", "original-baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.Pause("manual edit before first assignment"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	fixture.workspace.actual = resumeSnapshot("manual-tree", "manual-status")
	if _, err := Resume(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	assertCurrentResumeBaseline(t, fixture.run)
	if fixture.run.InitialBaseline.OperationID == "original-baseline" || fixture.run.InitialBaseline.State != fixture.run.CurrentState || fixture.run.Identity.Specification != basis.Specification || fixture.run.Identity.Configuration != basis.Configuration {
		t.Fatalf("same-basis manual state did not refresh baseline: %#v", fixture.run)
	}
}

func TestResumeInterruptedRequiredCommandRestartsCompleteSetFromFirstCheck(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.load = func(string) (setting.Configuration, error) {
		return resumeChecksConfiguration(t, `{
"lint":{"kind":"lint","command":{"program":"lint","args":[]}},
"test_all":{"kind":"tests","command":{"program":"test_all","args":[]}}}`, `["lint","test_all"]`), nil
	}
	var calls []string
	interrupted := true
	firstContext, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	input := fixture.input()
	input.Runner = CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		calls = append(calls, command.Program)
		if interrupted {
			interrupted = false
			cancelFirst()
			return checkexec.Result{ExitCode: -1, Failure: checkexec.FailureCanceled, Stderr: []byte("process ended during lint")}, context.Canceled
		}
		return checkexec.Result{}, nil
	})

	first, err := Resume(firstContext, input)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.run.Status != implstate.RunPaused || first.ResumeChecks.Results[0].Status != CheckFailed || first.ResumeChecks.Results[1].Status != CheckNotRun || fixture.run.RunResults[0].Status != implstate.ResultInterrupted {
		t.Fatalf("interrupted resume check did not remain a diagnostic pause: result=%#v run=%#v", first, fixture.run)
	}
	second, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"lint", "lint", "test_all"}; !slices.Equal(got, want) {
		t.Fatalf("resume continued an interrupted set instead of restarting it: %#v, want %#v", got, want)
	}
	if fixture.run.Status != implstate.RunActive || !second.ResumeChecks.Succeeded() {
		t.Fatalf("second resume did not establish fresh full evidence: result=%#v run=%#v", second, fixture.run)
	}
	for _, operation := range fixture.run.RunOperations {
		if operation.UncountedResumeCheck && len(operation.Attempts) != 0 {
			t.Fatalf("resume check retained an attempt: %#v", operation)
		}
	}
}

func TestResumeRequiredCheckFailurePersistsPauseAndDoesNotCreateSessions(t *testing.T) {
	fixture := newResumeFixture(t, "")
	changed := resumeTestConfiguration(t, "changed-model", "")
	fixture.load = func(string) (setting.Configuration, error) { return changed, nil }
	owner, err := NewSessionOwner(PreparedRuntimes{}, threadConfigForTest(fixture.repository))
	if err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.SessionOwner = &owner
	input.SessionBase = threadConfigForTest(fixture.repository)
	var calls []string
	input.Runner = CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		calls = append(calls, command.Program)
		return checkexec.Result{ExitCode: 1, Stderr: []byte("unit failed")}, nil
	})

	result, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"unit"}; !slices.Equal(got, want) {
		t.Fatalf("failed resume ran unexpected commands: %#v, want %#v", got, want)
	}
	if fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || !strings.Contains(fixture.run.ExecutionBlock.Diagnostic, "unit failed") || result.ResumeChecks.Results[0].Status != CheckFailed {
		t.Fatalf("failed resume check did not persist diagnostic pause: result=%#v run=%#v", result, fixture.run)
	}
	if owner == nil || result.SessionsRecreated {
		t.Fatalf("resume check failure created or replaced sessions before the gate passed: result=%#v", result)
	}
	operation := fixture.run.RunOperations[len(fixture.run.RunOperations)-1]
	if !operation.UncountedResumeCheck || len(operation.Attempts) != 0 {
		t.Fatalf("failed resume check consumed attempts: %#v", operation)
	}
}

func TestResumeReloadsChangedConfigurationAndRecreatesSessionOwner(t *testing.T) {
	fixture := newResumeFixture(t, "")
	changed := resumeTestConfiguration(t, "changed-model", "")
	fixture.load = func(string) (setting.Configuration, error) { return changed, nil }
	base := fixture.repository
	old, err := NewSessionOwner(PreparedRuntimes{}, threadConfigForTest(base))
	if err != nil {
		t.Fatal(err)
	}
	owner := old
	input := fixture.input()
	input.SessionOwner = &owner
	input.SessionBase = threadConfigForTest(base)

	result, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConfigurationChanged || !result.SessionsRecreated || owner == old {
		t.Fatalf("configuration reload did not install a fresh session owner: %#v", result)
	}
	assertCurrentResumeBaseline(t, fixture.run)
	if _, err := old.Orchestrator(context.Background(), RoleStartContext{Role: ResponseRoleOrchestrator}); !errors.Is(err, ErrSessionOwnerClosed) {
		t.Fatalf("old owner remains usable after profile change: %v", err)
	}
}

func TestResumeAfterProcessRestartCreatesFreshOwnerWithoutConfigurationChange(t *testing.T) {
	fixture := newResumeFixture(t, "")
	var owner *SessionOwner // a new process has no live provider threads
	input := fixture.input()
	input.Factories = map[string]RuntimeFactory{"test": &resumeProfileRuntimeFactory{}}
	input.SessionOwner = &owner
	input.SessionBase = threadConfigForTest(fixture.repository)

	result, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfigurationChanged || !result.SessionsRecreated || owner == nil {
		t.Fatalf("restart did not install a fresh owner from durable state: %#v owner=%p", result, owner)
	}
	if _, err := owner.Restore(context.Background(), SessionRestore{Role: ResponseRoleOrchestrator, Start: sessionStartContext(t, ResponseRoleOrchestrator)}); err != nil {
		t.Fatalf("fresh owner could not restore orchestrator from its durable bootstrap: %v", err)
	}
	if _, err := owner.Restore(context.Background(), SessionRestore{Role: ResponseRoleImplementer, AssignmentID: "active-assignment", Start: sessionStartContext(t, ResponseRoleImplementer)}); err != nil {
		t.Fatalf("fresh owner could not restore active assignment session from its durable bootstrap: %v", err)
	}
}

func TestResumeRecreatesStaleProfileOwnerAfterPriorGateFailure(t *testing.T) {
	fixture := newResumeFixture(t, "")
	factory := &resumeProfileRuntimeFactory{}
	initial := resumeTestConfiguration(t, "initial-model", "")
	preparedInitial, err := PrepareRuntimes(initial, map[string]RuntimeFactory{"test": factory})
	if err != nil {
		t.Fatal(err)
	}
	old, err := NewSessionOwner(preparedInitial, threadConfigForTest(fixture.repository))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Orchestrator(context.Background(), sessionStartContext(t, ResponseRoleOrchestrator)); err != nil {
		t.Fatal(err)
	}
	owner := old
	changed := resumeTestConfiguration(t, "changed-model", "")
	fixture.load = func(string) (setting.Configuration, error) { return changed, nil }
	input := fixture.input()
	input.Factories = map[string]RuntimeFactory{"test": factory}
	input.SessionOwner = &owner
	input.SessionBase = threadConfigForTest(fixture.repository)
	input.Runner = CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
		return checkexec.Result{ExitCode: 1, Stderr: []byte("required check failed")}, nil
	})

	first, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ConfigurationChanged || first.SessionsRecreated || owner != old || fixture.run.Status != implstate.RunPaused {
		t.Fatalf("failed gate unexpectedly replaced owner: result=%#v owner=%p old=%p run=%#v", first, owner, old, fixture.run)
	}
	input.Runner = CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil })

	second, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if second.ConfigurationChanged || !second.SessionsRecreated || owner == old || fixture.run.Status != implstate.RunActive {
		t.Fatalf("successful second resume did not replace stale owner: result=%#v owner=%p old=%p run=%#v", second, owner, old, fixture.run)
	}
	if _, err := old.Orchestrator(context.Background(), sessionStartContext(t, ResponseRoleOrchestrator)); !errors.Is(err, ErrSessionOwnerClosed) {
		t.Fatalf("old profile-A owner remained usable: %v", err)
	}
	if _, err := owner.Orchestrator(context.Background(), sessionStartContext(t, ResponseRoleOrchestrator)); err != nil {
		t.Fatal(err)
	}
	if len(factory.created) < 2 || factory.created[0].Model != "initial-model" || factory.created[len(factory.created)-1].Model != "changed-model" {
		t.Fatalf("owner sessions used profiles %#v, want initial then changed model", factory.created)
	}
}

func TestResumeInvalidConfigurationLeavesDurableDiagnosticPause(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.load = func(string) (setting.Configuration, error) {
		return setting.Configuration{}, errors.New("invalid implementation JSON")
	}

	_, err := Resume(context.Background(), fixture.input())
	if !errors.Is(err, ErrResumeReconciliation) {
		t.Fatalf("resume error = %v", err)
	}
	if fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || fixture.run.ExecutionBlock.Diagnostic == "" {
		t.Fatalf("invalid configuration did not remain a diagnostic pause: %#v", fixture.run)
	}
	persisted, _, readErr := fixture.state.Current(context.Background())
	if readErr != nil || persisted.Status != implstate.RunPaused || persisted.ExecutionBlock == nil {
		t.Fatalf("diagnostic pause was not durable: run=%#v err=%v", persisted, readErr)
	}
}

func TestResumeClosesWhenCompleteSpecificationChanges(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.classify = func([]byte, []byte) (SpecificationChange, error) {
		return SpecificationChange{RequiresNewScope: true}, nil
	}
	writeResumeFile(t, filepath.Join(fixture.repository, "openspec", "changes", "change", "proposal.md"), "changed scope\n")

	_, err := Resume(context.Background(), fixture.input())
	if !errors.Is(err, ErrResumeScopeChanged) {
		t.Fatalf("resume error = %v", err)
	}
	if fixture.run.Status != implstate.RunClosed || fixture.run.CloseReason == "" {
		t.Fatalf("scope change did not close run: %#v", fixture.run)
	}
}

func TestResumeRefreshesCompatibleSpecificationChange(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.classify = func(previous, current []byte) (SpecificationChange, error) {
		if string(previous) == string(current) {
			t.Fatal("classifier did not receive distinct specification versions")
		}
		return SpecificationChange{}, nil
	}
	writeResumeFile(t, filepath.Join(fixture.repository, "openspec", "changes", "change", "proposal.md"), "compatible clarification\n")

	if _, err := Resume(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	if fixture.run.Status != implstate.RunActive || fixture.run.Identity.Specification == fixture.specification {
		t.Fatalf("compatible specification was not refreshed: %#v", fixture.run)
	}
	assertCurrentResumeBaseline(t, fixture.run)
}

func TestRestartAfterAcceptanceInputRefreshRerunsAssignmentEvidenceWithoutRepeatingImplementation(t *testing.T) {
	for _, refresh := range []string{"configuration", "compatible specification"} {
		t.Run(refresh, func(t *testing.T) {
			fixture := newResumeFixture(t, "")
			prepareAcceptedRestartAssignment(t, fixture)
			if err := fixture.run.Pause("refresh acceptance inputs"); err != nil {
				t.Fatal(err)
			}
			configureAcceptanceRefresh(t, fixture, refresh)
			var checkCalls int
			input := fixture.input()
			input.Runner = CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
				checkCalls++
				return checkexec.Result{}, nil
			})
			resumed, err := Resume(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			assertCurrentResumeBaseline(t, fixture.run)
			if err := CanStartTaskReview(fixture.run, "assignment"); !errors.Is(err, ErrTaskReviewNotReady) {
				t.Fatalf("stale assignment checks remained current: %v", err)
			}
			action, interrupted, err := classifyRestartAssignmentAction(fixture.run, "assignment")
			if err != nil || action != restartAssignmentChecks || interrupted != nil {
				t.Fatalf("stale assignment evidence route = %v, %#v, %v", action, interrupted, err)
			}

			factory := &sessionRuntimeFactory{}
			prepared, err := PrepareRuntimes(resumed.Configuration, map[string]RuntimeFactory{"test": factory})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := NewSessionOwner(prepared, threadConfigForTest(fixture.repository))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			var committedMessage string
			if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{
				Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository,
				Workspace: fixture.workspace, Runner: input.Runner, Configuration: resumed.Configuration, Checks: resumed.Checks,
				CommitControl: restartCommitControl{parent: "head", tree: "expected-tree", message: &committedMessage},
			}); err != nil {
				t.Fatalf("continue refreshed assignment: %v; run=%#v", err, fixture.run)
			}
			if fixture.run.Status != implstate.RunSucceeded || checkCalls != 3 || countRoleTurns(factory, ResponseRoleImplementer) != 0 || !strings.HasPrefix(committedMessage, "Use the implementer-authored commit message\n\n") {
				t.Fatalf("refreshed assignment reused stale evidence or response: checks=%d implementer_turns=%d message=%q run=%#v", checkCalls, countRoleTurns(factory, ResponseRoleImplementer), committedMessage, fixture.run)
			}
		})
	}
}

func TestRestartAfterAcceptanceInputRefreshRerunsFinalEvidence(t *testing.T) {
	for _, refresh := range []string{"configuration", "compatible specification"} {
		t.Run(refresh, func(t *testing.T) {
			fixture := newResumeFixture(t, "")
			preparePendingCommitReflection(t, fixture)
			if err := fixture.run.Resume(); err != nil {
				t.Fatal(err)
			}
			assignment := &fixture.run.Assignments[0]
			intent := assignment.Acceptance.PendingCommit
			oldBasis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
			if err := fixture.run.CommitAssignment(assignment.ID, implstate.CommitEvidence{OperationID: intent.OperationID, CommitID: "old-commit", ParentCommit: intent.ParentCommit, Tree: intent.Tree, Message: intent.Message, State: fixture.run.CurrentState, Basis: oldBasis}); err != nil {
				t.Fatal(err)
			}
			for _, operation := range []implstate.Operation{
				{ID: "stale-final-check", Kind: implstate.OperationCheck, Basis: oldBasis, Description: "final required checks"},
				{ID: "stale-final-review", Kind: implstate.OperationReview, Basis: oldBasis, Description: "independent final review", Counter: implstate.CycleCounterFinalReview},
			} {
				if err := fixture.run.AddRunOperation(operation); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.run.StartRunAttempt(operation.ID); err != nil {
					t.Fatal(err)
				}
				if err := fixture.run.RecordRunAttemptOutcome(operation.ID, implstate.AttemptSucceeded, ""); err != nil {
					t.Fatal(err)
				}
				if err := fixture.run.AddRunResult(implstate.OperationResult{ID: implstate.ResultID(string(operation.ID) + "-result"), OperationID: operation.ID, Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: oldBasis}); err != nil {
					t.Fatal(err)
				}
			}
			if err := fixture.run.Pause("refresh final acceptance inputs"); err != nil {
				t.Fatal(err)
			}
			configureAcceptanceRefresh(t, fixture, refresh)
			var checkCalls int
			input := fixture.input()
			input.Runner = CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
				checkCalls++
				return checkexec.Result{}, nil
			})
			resumed, err := Resume(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			assertCurrentResumeBaseline(t, fixture.run)
			if result, _ := latestCurrentFinalCheck(fixture.run); result != nil {
				t.Fatalf("stale final check remained current: %#v", result)
			}
			factory := &sessionRuntimeFactory{}
			prepared, err := PrepareRuntimes(resumed.Configuration, map[string]RuntimeFactory{"test": factory})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := NewSessionOwner(prepared, threadConfigForTest(fixture.repository))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			if err := DispatchRestartContinuation(context.Background(), RestartContinuationInput{Owner: owner, Journal: fixture.journal, StateStore: fixture.state, Run: fixture.run, Repository: fixture.repository, Workspace: fixture.workspace, Runner: input.Runner, Configuration: resumed.Configuration, Checks: resumed.Checks}); err != nil {
				t.Fatalf("continue refreshed final acceptance: %v; run=%#v", err, fixture.run)
			}
			if fixture.run.Status != implstate.RunSucceeded || checkCalls != 2 {
				t.Fatalf("refreshed final acceptance reused stale evidence: checks=%d run=%#v", checkCalls, fixture.run)
			}
		})
	}
}

func TestResumePausesWhenSpecificationChangeIsIndeterminate(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.classify = func([]byte, []byte) (SpecificationChange, error) {
		return SpecificationChange{}, errors.New("semantic design change is indeterminate")
	}
	writeResumeFile(t, filepath.Join(fixture.repository, "openspec", "changes", "change", "design.md"), "# Design\n\n## New structure\n")

	_, err := Resume(context.Background(), fixture.input())
	if !errors.Is(err, ErrResumeReconciliation) || fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || !strings.Contains(fixture.run.ExecutionBlock.Diagnostic, "indeterminate") {
		t.Fatalf("indeterminate specification classification = run=%#v err=%v", fixture.run, err)
	}
	if fixture.run.Status == implstate.RunClosed {
		t.Fatal("indeterminate specification change closed the run")
	}
}

func TestResumeRulesContentOnlyDoesNotInvalidateCurrentAcceptanceState(t *testing.T) {
	fixture := newResumeFixture(t, "rules/rules.md")
	fixture.workspace.actual.TreeOID = "rules-only-tree"
	fixture.workspace.actual.StatusHash = "rules-only-status"
	fixture.workspace.paths = []string{"rules/rules.md"}
	writeResumeFile(t, filepath.Join(fixture.repository, "rules", "rules.md"), "# Updated rules\n")

	result, err := Resume(context.Background(), fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceChanged || fixture.run.CurrentState != fixture.baseline || fixture.run.Status != implstate.RunActive {
		t.Fatalf("rules-only edit altered acceptance state: result=%#v run=%#v", result, fixture.run)
	}
}

func TestResumeRulesClassificationUsesExactValidatedMarkdownDocuments(t *testing.T) {
	for _, test := range []struct {
		name          string
		path          string
		staged        bool
		wantWorkspace bool
	}{
		{name: "root markdown rule", path: "rules/AGENTS.md"},
		{name: "staged markdown rule", path: "rules/AGENTS.md", staged: true},
		{name: "sibling code", path: "rules/helper.go", wantWorkspace: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResumeFixture(t, "rules/AGENTS.md")
			fixture.workspace.actual.TreeOID = "changed-" + test.name
			fixture.workspace.actual.StatusHash = "changed-status-" + test.name
			if test.staged {
				fixture.workspace.actual.IndexHash = "staged-rules-index"
			}
			fixture.workspace.paths = []string{test.path}
			writeResumeFile(t, filepath.Join(fixture.repository, filepath.FromSlash(test.path)), "changed\n")
			result, err := Resume(context.Background(), fixture.input())
			if err != nil {
				t.Fatal(err)
			}
			if result.WorkspaceChanged != test.wantWorkspace {
				t.Fatalf("WorkspaceChanged = %v, want %v", result.WorkspaceChanged, test.wantWorkspace)
			}
		})
	}
}

func TestResumePausesForChangedGitControlButPreservesUnstagedEdits(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*git.Snapshot)
		wantError bool
	}{
		{name: "changed branch", change: func(s *git.Snapshot) { s.HeadRef = "refs/heads/other" }, wantError: true},
		{name: "detached head", change: func(s *git.Snapshot) { s.HeadRef = "" }, wantError: true},
		{name: "unrelated commit", change: func(s *git.Snapshot) { s.HeadOID = "other-head" }, wantError: true},
		{name: "staged index", change: func(s *git.Snapshot) { s.IndexHash = "other-index" }, wantError: true},
		{name: "unstaged edit", change: func(s *git.Snapshot) { s.TreeOID, s.StatusHash = "manual-tree", "manual-status" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResumeFixture(t, "")
			test.change(&fixture.workspace.actual)
			fixture.workspace.paths = []string{"code.go"}
			result, err := Resume(context.Background(), fixture.input())
			if test.wantError {
				if !errors.Is(err, ErrResumeReconciliation) || fixture.run.Status != implstate.RunPaused {
					t.Fatalf("unsafe Git control change was accepted: err=%v run=%#v", err, fixture.run)
				}
				return
			}
			if err != nil || !result.WorkspaceChanged || fixture.run.Status != implstate.RunActive {
				t.Fatalf("unstaged edit was not retained: err=%v result=%#v run=%#v", err, result, fixture.run)
			}
		})
	}
}

func TestResumePreservesAcceptedPendingCommitAfterInformationalReflection(t *testing.T) {
	fixture := newResumeFixture(t, "")
	preparePendingCommitReflection(t, fixture)
	fixture.workspace.actual.TreeOID = "informational-reflection-tree"
	fixture.workspace.actual.StatusHash = "informational-reflection-status"
	fixture.workspace.actual.IndexHash = "staged-by-refused-hook"
	fixture.workspace.paths = []string{"openspec/changes/change/tasks.md"}

	if _, err := Resume(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	assignment := fixture.run.Assignments[0]
	if fixture.run.Status != implstate.RunActive || assignment.Status != implstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil || assignment.Acceptance.PendingCommit.Tree != "informational-reflection-tree" {
		t.Fatalf("resume invalidated accepted pending commit after informational reflection: %#v", fixture.run)
	}
}

func TestResumePreservesAcceptedReflectionBeforePendingCommitIntent(t *testing.T) {
	fixture := newResumeFixture(t, "rules/rules.md")
	prepareAcceptedReflectionEvidence(t, fixture)
	fixture.workspace.actual.TreeOID = "reflected-tasks-and-rules-tree"
	fixture.workspace.actual.StatusHash = "reflected-tasks-and-rules-status"
	fixture.workspace.compare = func(before, after git.Snapshot) []string {
		switch {
		case before.TreeOID == "expected-tree" && after.TreeOID == "reflected-tasks-tree":
			return []string{"openspec/changes/change/tasks.md"}
		case before.TreeOID == "reflected-tasks-tree" && after.TreeOID == "reflected-tasks-and-rules-tree":
			return []string{"rules/rules.md"}
		default:
			return nil
		}
	}

	if _, err := Resume(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	assignment := fixture.run.Assignments[0]
	if fixture.run.Status != implstate.RunActive || assignment.Status != implstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil || assignment.Acceptance.PendingCommit.OperationID != "" {
		t.Fatalf("durable reflection evidence did not preserve pending acceptance: %#v", fixture.run)
	}
}

func TestResumeCheckGeneratedChangeInvalidatesAcceptedState(t *testing.T) {
	fixture := newResumeFixture(t, "")
	preparePendingCommitReflection(t, fixture)
	fixture.workspace.diff = func(before, after git.Snapshot) git.Difference {
		if before.TreeOID != after.TreeOID || before.StatusHash != after.StatusHash {
			return git.Difference{Paths: []string{"generated.go"}}
		}
		return git.Difference{}
	}
	input := fixture.input()
	input.Runner = CheckRunnerFunc(func(_ context.Context, _ checkexec.Command) (checkexec.Result, error) {
		fixture.workspace.actual.TreeOID = "generated-by-resume-check"
		fixture.workspace.actual.StatusHash = "generated-by-resume-check-status"
		return checkexec.Result{}, nil
	})

	if _, err := Resume(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	assignment := fixture.run.Assignments[0]
	if fixture.run.Status != implstate.RunActive || assignment.Status != implstate.AssignmentActive || assignment.Acceptance != nil || fixture.run.CurrentState == fixture.baseline {
		t.Fatalf("resume check-generated mutation did not invalidate acceptance: %#v", fixture.run)
	}
}

type resumeFixture struct {
	repository    string
	run           *implstate.Run
	journal       *runstore.Run
	state         *runstore.StateStore
	baseline      implstate.EvidenceRef
	specification implstate.EvidenceRef
	workspace     *resumeWorkspace
	load          func(string) (setting.Configuration, error)
	classify      func([]byte, []byte) (SpecificationChange, error)
}

func prepareRestartActiveAssignment(t *testing.T, fixture *resumeFixture) {
	t.Helper()
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if err := fixture.run.AddRunOperation(implstate.Operation{ID: "baseline", Kind: implstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddRunResult(implstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.StartAssignment("assignment", []implstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := PersistBriefVersion(context.Background(), fixture.journal, fixture.state, fixture.run, "assignment", []implstate.TaskID{"task"}, "Implement the durable task."); err != nil {
		t.Fatal(err)
	}
}

func prepareAcceptedRestartAssignment(t *testing.T, fixture *resumeFixture) {
	t.Helper()
	prepareRestartActiveAssignment(t, fixture)
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	seedImplementationReadyReceipt(t, fixture, "accepted-implementation", "Use the implementer-authored commit message")
	evidence, err := fixture.journal.Publish("accepted-restart-check-evidence", []byte("required checks passed"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		operation implstate.Operation
		result    implstate.OperationResult
	}{
		{operation: implstate.Operation{ID: "accepted-checks", Kind: implstate.OperationCheck, BriefID: "brief-assignment-v1", Basis: basis, Description: "required acceptance checks", Counter: implstate.CycleCounterMandatoryChecks}, result: implstate.OperationResult{ID: "accepted-checks-result", OperationID: "accepted-checks", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis, Evidence: []implstate.EvidenceRef{evidence}}},
		{operation: implstate.Operation{ID: "accepted-review", Kind: implstate.OperationReview, BriefID: "brief-assignment-v1", Basis: basis, Description: "task review", Counter: implstate.CycleCounterAssignmentReview}, result: implstate.OperationResult{ID: "accepted-review-result", OperationID: "accepted-review", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}},
	} {
		if err := fixture.run.AddOperation("assignment", item.operation); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.run.StartAssignmentAttempt("assignment", item.operation.ID); err != nil {
			t.Fatal(err)
		}
		if err := fixture.run.RecordAssignmentAttemptOutcome("assignment", item.operation.ID, implstate.AttemptSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		if err := fixture.run.AddResult("assignment", item.result); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.run.AcceptAssignment("assignment", implstate.AcceptanceEvidence{BriefID: "brief-assignment-v1", State: fixture.run.CurrentState, Basis: basis, CheckResultIDs: []implstate.ResultID{"accepted-checks-result"}, ReviewResultID: "accepted-review-result"}); err != nil {
		t.Fatal(err)
	}
}

func restartTestOwner(t *testing.T, fixture *resumeFixture) (*SessionOwner, setting.Configuration, setting.CheckSelection) {
	t.Helper()
	configuration := resumeTestConfiguration(t, "initial-model", "")
	prepared, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{"test": &sessionRuntimeFactory{}})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewSessionOwner(prepared, agentruntime.ThreadConfig{Workspace: fixture.repository})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	checks, err := configuration.SelectHostChecks()
	if err != nil {
		t.Fatal(err)
	}
	return owner, configuration, checks
}

type restartCommitObserver struct {
	calls       int
	observation CommitObservation
}

func (observer *restartCommitObserver) Observe(context.Context, string) (CommitObservation, error) {
	observer.calls++
	return observer.observation, nil
}

func newResumeFixture(t *testing.T, rulesFile string) *resumeFixture {
	t.Helper()
	repository := t.TempDir()
	writeResumeFile(t, filepath.Join(repository, "openspec", "changes", "change", "proposal.md"), "proposal\n")
	writeResumeFile(t, filepath.Join(repository, "openspec", "changes", "change", "design.md"), "design\n")
	writeResumeFile(t, filepath.Join(repository, "openspec", "changes", "change", "tasks.md"), "- [ ] task\n")
	writeResumeFile(t, filepath.Join(repository, "openspec", "changes", "change", "specs", "feature", "spec.md"), "change requirement\n")
	writeResumeFile(t, filepath.Join(repository, "openspec", "specs", "base", "spec.md"), "base requirement\n")
	if rulesFile != "" {
		writeResumeFile(t, filepath.Join(repository, filepath.FromSlash(rulesFile)), "# Rules\n")
	}
	configuration := resumeTestConfiguration(t, "initial-model", rulesFile)
	configurationBytes, err := canonicalResumeConfiguration(configuration)
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
	store, err := runstore.New(filepath.Join(t.TempDir(), "stepan"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("resume-run")
	if err != nil {
		t.Fatal(err)
	}
	expected := resumeSnapshot("expected-tree", "expected-status")
	snapshotBytes, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := journal.Publish("baseline", snapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	specRef, err := journal.Publish("specification", specification)
	if err != nil {
		t.Fatal(err)
	}
	configRef, err := journal.Publish("configuration", configurationBytes)
	if err != nil {
		t.Fatal(err)
	}
	tasksRef, err := journal.Publish("tasks", []byte("tasks"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := implstate.NewRun(implstate.RunIdentity{ID: "resume-run", Change: "change", Repository: repository, WorkCopy: repository, Branch: "feature", BaselineCommit: "base", BaselineState: baseline, Specification: specRef, TaskList: tasksRef, Configuration: configRef}, []implstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Pause("user paused"); err != nil {
		t.Fatal(err)
	}
	state, err := runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	return &resumeFixture{repository: repository, run: run, journal: journal, state: state, baseline: baseline, specification: specRef, workspace: &resumeWorkspace{actual: expected}, load: func(string) (setting.Configuration, error) { return configuration, nil }}
}

func newPendingExtractionResumeFixture(t *testing.T) *resumeFixture {
	t.Helper()
	base := newResumeFixture(t, "")
	store, err := runstore.New(filepath.Join(t.TempDir(), "stepan-pending"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("pending-extraction-resume")
	if err != nil {
		t.Fatal(err)
	}
	copyReference := func(reference implstate.EvidenceRef) implstate.EvidenceRef {
		data, err := base.journal.Read(reference)
		if err != nil {
			t.Fatal(err)
		}
		copied, err := journal.Publish(reference.ID, data)
		if err != nil {
			t.Fatal(err)
		}
		return copied
	}
	identity := base.run.Identity
	identity.ID = journal.ID()
	identity.BaselineState = copyReference(identity.BaselineState)
	identity.Specification = copyReference(identity.Specification)
	identity.TaskList = copyReference(identity.TaskList)
	identity.Configuration = copyReference(identity.Configuration)
	run, state, err := PrepareInitialTaskExtraction(context.Background(), journal, identity, "extract")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	if _, _, err := state.RecordRunAttemptStart(context.Background(), run, "extract"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RecordRunAttemptOutcome(context.Background(), run, "extract", implstate.AttemptInterrupted, "process stopped"); err != nil {
		t.Fatal(err)
	}
	if err := run.Pause("process stopped during task extraction"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return &resumeFixture{repository: base.repository, run: run, journal: journal, state: state, baseline: identity.BaselineState, specification: identity.Specification, workspace: base.workspace, load: base.load}
}

func (fixture *resumeFixture) input() ResumeInput {
	return ResumeInput{
		Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Workspace: fixture.workspace,
		Runner:              CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		ConfigurationLoader: fixture.load, ClassifySpecificationChange: fixture.classify,
	}
}

func configureAcceptanceRefresh(t *testing.T, fixture *resumeFixture, refresh string) {
	t.Helper()
	switch refresh {
	case "configuration":
		changed := resumeTestConfiguration(t, "changed-model", "")
		fixture.load = func(string) (setting.Configuration, error) { return changed, nil }
	case "compatible specification":
		fixture.classify = func([]byte, []byte) (SpecificationChange, error) { return SpecificationChange{}, nil }
		writeResumeFile(t, filepath.Join(fixture.repository, "openspec", "changes", "change", "proposal.md"), "compatible clarification\n")
	default:
		t.Fatalf("unknown acceptance refresh %q", refresh)
	}
}

func assertCurrentResumeBaseline(t *testing.T, run *implstate.Run) {
	t.Helper()
	basis := implstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	if run.InitialBaseline == nil || run.InitialBaseline.Basis != basis {
		t.Fatalf("initial baseline was not refreshed to current acceptance basis: %#v", run.InitialBaseline)
	}
	operation := finalRunOperation(run, run.InitialBaseline.OperationID)
	if operation == nil || !operation.UncountedResumeCheck {
		t.Fatalf("refreshed initial baseline is not backed by the resume gate: %#v", operation)
	}
}

func countRoleTurns(factory *sessionRuntimeFactory, role ResponseRole) int {
	turns := 0
	for _, runtime := range factory.runtimes {
		for _, config := range runtime.configurations {
			if roleFromBootstrap(config.BootstrapInstructions) == role {
				turns += runtime.turnCount
				break
			}
		}
	}
	return turns
}

func preparePendingCommitReflection(t *testing.T, fixture *resumeFixture) {
	t.Helper()
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if err := fixture.run.AddRunOperation(implstate.Operation{ID: "baseline-check", Kind: implstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartRunAttempt("baseline-check"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddRunResult(implstate.OperationResult{ID: "baseline-check-result", OperationID: "baseline-check", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordInitialBaselinePass("baseline-check", "baseline-check-result"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.StartAssignment("assignment", []implstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief, err := fixture.journal.Publish("pending-brief", []byte("brief"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddBriefVersion("assignment", implstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	seedImplementationReadyReceipt(t, fixture, "pending-implementation", "Commit the accepted pending work")
	for _, operation := range []implstate.Operation{{ID: "check", Kind: implstate.OperationCheck, BriefID: "brief", Basis: basis, Counter: implstate.CycleCounterMandatoryChecks}, {ID: "review", Kind: implstate.OperationReview, BriefID: "brief", Basis: basis, Counter: implstate.CycleCounterAssignmentReview}} {
		if err := fixture.run.AddOperation("assignment", operation); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.run.StartAssignmentAttempt("assignment", operation.ID); err != nil {
			t.Fatal(err)
		}
		if err := fixture.run.AddResult("assignment", implstate.OperationResult{ID: implstate.ResultID(string(operation.ID) + "-result"), OperationID: operation.ID, Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	acceptance := implstate.AcceptanceEvidence{BriefID: "brief", State: fixture.run.CurrentState, Basis: basis, CheckResultIDs: []implstate.ResultID{"check-result"}, ReviewResultID: "review-result", PendingCommit: implstate.CommitIntent{OperationID: "commit", ParentCommit: "head", Tree: "informational-reflection-tree", Message: "commit accepted work"}}
	if err := fixture.run.AcceptAssignment("assignment", acceptance); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.Pause("commit hook failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
}

func seedImplementationReadyReceipt(t *testing.T, fixture *resumeFixture, operationID implstate.OperationID, message string) {
	t.Helper()
	assignment := assignmentByID(fixture.run, "assignment")
	if assignment == nil || len(assignment.Briefs) == 0 {
		t.Fatal("active assignment with a brief is required")
	}
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	briefID := assignment.Briefs[len(assignment.Briefs)-1].ID
	operation := implstate.Operation{ID: operationID, Kind: implstate.OperationAgent, BriefID: briefID, Basis: basis, Description: "implementation response"}
	if err := fixture.run.AddOperation("assignment", operation); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartAssignmentAttempt("assignment", operationID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordAssignmentAttemptOutcome("assignment", operationID, implstate.AttemptSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	response := AgentResponse{Kind: ResponseImplementationReady, Message: &message, Binding: ResponseBinding{CallID: string(operationID) + "-call", RunID: fixture.run.Identity.ID, AssignmentID: "assignment", BriefID: briefID, Specification: basis.Specification, Configuration: basis.Configuration, TaskList: fixture.run.Identity.TaskList}}
	resultID, _, _ := implementationReadyReceiptIDs(operationID)
	responseRef, workspaceRef, err := publishControlledAgentSuccessReceipt(fixture.journal, operationID, response, resumeSnapshot("implementation-tree", "implementation-status"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddResult("assignment", implstate.OperationResult{ID: resultID, OperationID: operationID, Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis, Evidence: []implstate.EvidenceRef{responseRef, workspaceRef}}); err != nil {
		t.Fatal(err)
	}
}

func prepareAcceptedReflectionEvidence(t *testing.T, fixture *resumeFixture) {
	t.Helper()
	preparePendingCommitReflection(t, fixture)
	// Model the narrower crash window after a successful reflection was made
	// durable but before CommitAcceptedAssignment saved its intent.
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	fixture.run.Assignments[0].Acceptance.PendingCommit = implstate.CommitIntent{}
	if err := fixture.run.AddRunOperation(implstate.Operation{ID: "reflect-progress", Kind: implstate.OperationAgent, Basis: implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}, Description: "reflect accepted task progress in tasks.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartRunAttempt("reflect-progress"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordRunAttemptOutcome("reflect-progress", implstate.AttemptSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	state := resumeSnapshot("reflected-tasks-tree", "reflected-tasks-status")
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := fixture.journal.Publish("reflect-progress-result-workspace", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddRunResult(implstate.OperationResult{ID: "reflect-progress-result", OperationID: "reflect-progress", Status: implstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}, Evidence: []implstate.EvidenceRef{evidence}}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.Pause("before pending commit intent"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
}

func resumeTestConfiguration(t *testing.T, model, rulesFile string) setting.Configuration {
	t.Helper()
	profiles := `{"low":{"provider":"test","model":"` + model + `"},"medium":{"provider":"test","model":"` + model + `"},"high":{"provider":"test","model":"` + model + `"},"ultra":{"provider":"test","model":"` + model + `"}}`
	raw := `{"profiles":` + profiles + `,"checks":{"unit":{"kind":"tests","command":{"program":"unit","args":[]}}},"required_checks":["unit"]`
	if rulesFile != "" {
		raw += `,"rules_file":"` + rulesFile + `"`
	}
	raw += `}`
	configuration, err := setting.Merge(loopTestSources(nil, json.RawMessage(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

func resumeChecksConfiguration(t *testing.T, checks, required string) setting.Configuration {
	t.Helper()
	profiles := `{"low":{"provider":"test","model":"initial-model"},"medium":{"provider":"test","model":"initial-model"},"high":{"provider":"test","model":"initial-model"},"ultra":{"provider":"test","model":"initial-model"}}`
	configuration, err := setting.Merge(loopTestSources(nil, json.RawMessage(`{"profiles":`+profiles+`,"checks":`+checks+`,"required_checks":`+required+`}`)))
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

type resumeProfileRuntimeFactory struct {
	created []setting.RuntimeProfile
}

func (*resumeProfileRuntimeFactory) Preflight(setting.RuntimeProfile) error { return nil }

func (factory *resumeProfileRuntimeFactory) Create(_ context.Context, profile setting.RuntimeProfile) (agentruntime.Runtime, error) {
	factory.created = append(factory.created, profile)
	return &sessionRuntime{}, nil
}

func resumeSnapshot(tree, status string) git.Snapshot {
	return git.Snapshot{HeadOID: "head", HeadRef: "refs/heads/feature", TreeOID: tree, IndexHash: "index", StatusHash: status, SubmodulesHash: "submodules"}
}

func threadConfigForTest(workspace string) agentruntime.ThreadConfig {
	return agentruntime.ThreadConfig{Workspace: workspace}
}

func writeResumeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

type resumeWorkspace struct {
	actual   git.Snapshot
	paths    []string
	compare  func(before, after git.Snapshot) []string
	diff     func(before, after git.Snapshot) git.Difference
	restores int
}

type restartCommitControl struct {
	parent  string
	tree    string
	message *string
}

func (control restartCommitControl) Commit(_ context.Context, _ string, message string) (CommitObservation, error) {
	if control.message != nil {
		*control.message = message
	}
	return CommitObservation{
		CommitID: "restart-commit", ParentCommit: control.parent, Tree: control.tree, Message: message,
		Worktree: git.Snapshot{HeadOID: "restart-commit", TreeOID: control.tree},
	}, nil
}

func (workspace *resumeWorkspace) Capture(context.Context, string) (git.Snapshot, error) {
	return workspace.actual, nil
}

func (workspace *resumeWorkspace) EnsureUnchanged(_ context.Context, _ string, expected git.Snapshot) error {
	if sameResumeSnapshot(workspace.actual, expected) {
		return nil
	}
	return git.ErrRepositoryDiverged
}

func sameResumeSnapshot(left, right git.Snapshot) bool {
	return left.HeadOID == right.HeadOID && left.HeadRef == right.HeadRef && left.TreeOID == right.TreeOID && left.IndexHash == right.IndexHash && left.StatusHash == right.StatusHash && left.SubmodulesHash == right.SubmodulesHash
}

func (workspace *resumeWorkspace) Compare(_ context.Context, _ string, before, after git.Snapshot) ([]string, error) {
	if workspace.compare != nil {
		return append([]string(nil), workspace.compare(before, after)...), nil
	}
	return append([]string(nil), workspace.paths...), nil
}

func (workspace *resumeWorkspace) Diff(_ context.Context, _ string, before, after git.Snapshot) (git.Difference, error) {
	if workspace.diff != nil {
		return workspace.diff(before, after), nil
	}
	return git.Difference{}, nil
}

func (workspace *resumeWorkspace) RestorePaths(context.Context, string, git.Snapshot, git.Snapshot, []string) (git.Snapshot, error) {
	workspace.restores++
	return workspace.actual, nil
}

func (workspace *resumeWorkspace) AssignmentDiff(context.Context, string, string) (string, error) {
	return "No assignment changes.", nil
}
