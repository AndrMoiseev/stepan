//go:build git_integration

package impl_loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

// TestDeterministicImplementationLoopEndToEnd exercises the public controller
// boundaries together against a real, disposable Git repository.  Every agent
// turn and configured check is a deterministic in-process fake: this is a
// conformance test for the loop, not a provider smoke test.
func TestDeterministicImplementationLoopEndToEnd(t *testing.T) {
	ctx := context.Background()
	repository := newGitWorkspace(t)
	gitFixture(t, repository, "checkout", "--quiet", "-b", "implementation")
	writeResumeGitSpecification(t, repository)

	gitWorkspace := GitWorkspaceControl{}
	baseline, err := gitWorkspace.Capture(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	// Agent and check runtimes are deliberately in-process fakes.  Their
	// workspace seam is deterministic as well; the local commit boundary below
	// still observes and mutates the disposable real Git repository.
	var workspace WorkspaceControl = &unchangedWorkspaceControl{}
	store := mustControllerStore(t, t.TempDir())
	journal, err := store.Create("deterministic-e2e")
	if err != nil {
		t.Fatal(err)
	}
	baselineData, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	ref := func(id implementationstate.EvidenceID, data []byte) implementationstate.EvidenceRef {
		t.Helper()
		value, publishErr := journal.Publish(id, data)
		if publishErr != nil {
			t.Fatal(publishErr)
		}
		return value
	}
	identity := implementationstate.RunIdentity{
		ID: journal.ID(), Change: "change", Repository: repository, WorkCopy: repository, Branch: "implementation", BaselineCommit: baseline.HeadOID,
		BaselineState: ref("baseline", baselineData), Specification: ref("specification", []byte("# complete specification\n")),
		TaskList: ref("tasks", []byte("- [ ] source task\n")), Configuration: ref("configuration", []byte("deterministic test configuration")),
	}
	run, state, err := PrepareInitialTaskExtraction(ctx, journal, identity, "extract-tasks")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	extracted := e2ePayload(t, ResponseTasksExtracted, func(payload map[string]any) {
		payload["task_ids"] = []string{"source"}
		payload["task_payloads"] = []string{`{"id":"source","parent_id":"","title":"implement source behavior"}`}
	})
	if _, err := ExecuteInitialTaskExtraction(ctx, ControlledAgentCall{
		Session:    &AgentSession{Role: ResponseRoleOrchestrator, runtime: &controlledCallRuntime{turns: []controlledTurn{{raw: extracted}}}, thread: "fake-orchestrator"},
		Repository: repository, Workspace: workspace, Policy: AgentCallPolicy{Role: AgentRoleOrchestrator, CallID: "extract-call"},
		Run: run, Journal: journal, StateStore: state, OperationID: "extract-tasks", Limits: controlledCallLimits(),
		Expectation: e2eRunExpectation(ResponseRoleOrchestrator, ResponseStateExtractingTasks, run, "extract-call"), Message: "extract tasks",
	}); err != nil {
		t.Fatal(err)
	}

	checks := testCheckSelection([]string{"lint", "test_all"})
	runner := &recordingCheckRunner{}
	if _, err := RunInitialRequiredChecks(ctx, InitialRequiredChecks{Run: run, Workspace: workspace, StateStore: state, Journal: journal, Repository: repository, Selection: checks, Runner: runner, MaxCycles: 1, Operation: "initial-checks", Result: "initial-checks-result"}); err != nil {
		t.Fatal(err)
	}

	selectAssignment := func(assignmentID implementationstate.AssignmentID, taskID implementationstate.TaskID, operation, callID string) {
		t.Helper()
		if err := PrepareBriefSelection(ctx, state, run, implementationstate.OperationID(operation)); err != nil {
			t.Fatal(err)
		}
		brief := e2ePayload(t, ResponseBriefReady, func(payload map[string]any) {
			payload["task_ids"] = []string{string(taskID)}
			payload["brief"] = "Implement " + string(taskID) + " with deterministic acceptance evidence."
		})
		result, selectionErr := ExecuteBriefSelection(ctx, BriefSelectionCall{assignmentID: assignmentID, call: ControlledAgentCall{
			Session:    &AgentSession{Role: ResponseRoleBriefer, runtime: &controlledCallRuntime{turns: []controlledTurn{{raw: brief}}}, thread: "fake-briefer"},
			Repository: repository, Workspace: workspace, Policy: AgentCallPolicy{Role: AgentRoleBriefer, CallID: callID}, Run: run, Journal: journal, StateStore: state,
			OperationID: implementationstate.OperationID(operation), Limits: controlledCallLimits(), Expectation: e2eRunExpectation(ResponseRoleBriefer, ResponseStateInitialBriefing, run, callID), Message: "select assignment",
		}})
		if selectionErr != nil || result.AssignmentID != assignmentID {
			t.Fatalf("brief selection assignment=%#v error=%v", result, selectionErr)
		}
	}
	selectAssignment("source-assignment", "source", "select-source", "select-source-call")

	// The first executor asks for an additional narrow check, then reports an
	// implementation which the task reviewer rejects.  Its continuation makes
	// the repair and produces fresh mandatory acceptance evidence.
	sourceReady := e2eRunImplementer(t, ctx, run, state, journal, repository, workspace, checks, runner, "source-assignment", "source", []controlledTurn{
		{raw: e2ePayload(t, ResponseChecksRequested, func(payload map[string]any) { payload["check_names"] = []string{"test_auth"} }), before: func() {
			writeGitWorkspaceFile(t, filepath.Join(repository, "feature.txt"), "source implementation missing final validation\n")
		}},
		{raw: e2ePayload(t, ResponseImplementationReady, func(payload map[string]any) { payload["message"] = "implement source task" })},
	}, true)

	firstReview := e2eTaskReview(t, ctx, run, state, journal, repository, workspace, "source-assignment", "source-review-1", "source-review-1-result", "source-review-1-call", ResponseChangesRequested)
	if firstReview.Response.Kind != ResponseChangesRequested || len(run.OpenTaskReviewFindings("source-assignment")) != 1 {
		t.Fatalf("first task review = %#v", firstReview)
	}
	sourceReady = e2eRunImplementer(t, ctx, run, state, journal, repository, workspace, checks, runner, "source-assignment", "source", []controlledTurn{{raw: e2ePayload(t, ResponseImplementationReady, func(payload map[string]any) { payload["message"] = "fix source review finding" }), before: func() {
		writeGitWorkspaceFile(t, filepath.Join(repository, "feature.txt"), "source implementation with validation\n")
	}}}, false)
	secondReview := e2eTaskReview(t, ctx, run, state, journal, repository, workspace, "source-assignment", "source-review-2", "source-review-2-result", "source-review-2-call", ResponseReviewPassed)
	if secondReview.Response.Kind != ResponseReviewPassed {
		t.Fatalf("second task review = %#v", secondReview)
	}

	tasksPath := "openspec/changes/change/tasks.md"
	if _, err := e2eAcceptAndCommit(ctx, run, state, journal, repository, workspace, "source-assignment", sourceReady, "source-review-2-result", "source-reflect", "source-reflect-result", "source-reflect-call", "source-commit", func() {
		writeGitWorkspaceFile(t, filepath.Join(repository, filepath.FromSlash(tasksPath)), "- [x] source task\n")
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := RunFinalRequiredChecks(ctx, FinalRequiredChecks{Run: run, Workspace: workspace, StateStore: state, Journal: journal, Repository: repository, Selection: checks, Runner: runner, MaxCycles: 1, Operation: "final-checks-1", Result: "final-checks-1-result"}); err != nil {
		t.Fatal(err)
	}
	failedFinal, err := runFinalReviewerTurn(ctx, FinalReviewInput{Workspace: workspace, Run: run, StateStore: state, Journal: journal, Repository: repository, CheckResult: "final-checks-1-result", OperationID: "final-review-1", ResultID: "final-review-1-result", CallID: "final-review-1-call", RoundID: "round-1", Limits: controlledCallLimits()}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: &controlledCallRuntime{turns: []controlledTurn{{raw: e2ePayload(t, ResponseChangesRequested, nil)}}}, thread: "fake-final-reviewer"}, baseline.HeadOID)
	if err != nil || failedFinal.Response.Kind != ResponseChangesRequested {
		t.Fatalf("failed final review=%#v error=%v", failedFinal, err)
	}
	addedPayload := e2ePayload(t, ResponseTasksAdded, func(payload map[string]any) {
		payload["task_ids"] = []string{"final-fix"}
		payload["task_payloads"] = []string{`{"id":"final-fix","parent_id":"","title":"repair final finding","finding_ids":["F-1"]}`}
	})
	if _, err := AddFinalFindingTasks(ctx, FinalFindingTasksInput{
		Run: run, StateStore: state, Journal: journal, Repository: repository, Workspace: workspace,
		Session: &AgentSession{Role: ResponseRoleOrchestrator, runtime: &controlledCallRuntime{turns: []controlledTurn{{raw: addedPayload, before: func() {
			writeGitWorkspaceFile(t, filepath.Join(repository, filepath.FromSlash(tasksPath)), "- [x] source task\n- [ ] repair final finding\n")
		}}}}, thread: "fake-orchestrator"},
		Review: failedFinal, TasksPath: tasksPath, OperationID: "append-final-finding", ResultID: "append-final-finding-result", CallID: "append-final-finding-call", Limits: controlledCallLimits(),
	}); err != nil {
		t.Fatal(err)
	}

	selectAssignment("final-fix-assignment", "final-fix", "select-final-fix", "select-final-fix-call")
	finalReady := e2eRunImplementer(t, ctx, run, state, journal, repository, workspace, checks, runner, "final-fix-assignment", "final-fix", []controlledTurn{{raw: e2ePayload(t, ResponseImplementationReady, func(payload map[string]any) { payload["message"] = "repair final finding" }), before: func() {
		writeGitWorkspaceFile(t, filepath.Join(repository, "feature.txt"), "source implementation with validation and final correction\n")
	}}}, false)
	finalTaskReview := e2eTaskReview(t, ctx, run, state, journal, repository, workspace, "final-fix-assignment", "final-fix-review", "final-fix-review-result", "final-fix-review-call", ResponseReviewPassed)
	if finalTaskReview.Response.Kind != ResponseReviewPassed {
		t.Fatalf("final task review = %#v", finalTaskReview)
	}
	if _, err := e2eAcceptAndCommit(ctx, run, state, journal, repository, workspace, "final-fix-assignment", finalReady, "final-fix-review-result", "final-fix-reflect", "final-fix-reflect-result", "final-fix-reflect-call", "final-fix-commit", func() {
		writeGitWorkspaceFile(t, filepath.Join(repository, filepath.FromSlash(tasksPath)), "- [x] source task\n- [x] repair final finding\n")
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := RunFinalRequiredChecks(ctx, FinalRequiredChecks{Run: run, Workspace: workspace, StateStore: state, Journal: journal, Repository: repository, Selection: checks, Runner: runner, MaxCycles: 1, Operation: "final-checks-2", Result: "final-checks-2-result"}); err != nil {
		t.Fatal(err)
	}
	passedFinal, err := runFinalReviewerTurn(ctx, FinalReviewInput{Workspace: workspace, Run: run, StateStore: state, Journal: journal, Repository: repository, CheckResult: "final-checks-2-result", OperationID: "final-review-2", ResultID: "final-review-2-result", CallID: "final-review-2-call", RoundID: "round-2", Limits: controlledCallLimits()}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: &controlledCallRuntime{turns: []controlledTurn{{raw: e2ePayload(t, ResponseReviewPassed, nil)}}}, thread: "fake-final-reviewer"}, baseline.HeadOID)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompleteFinalAcceptance(ctx, run, state, journal, workspace, repository, "final-checks-2-result", passedFinal); err != nil {
		t.Fatal(err)
	}

	persisted, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != implementationstate.RunSucceeded || len(persisted.Assignments) != 2 || persisted.LeafStatus["source"] != implementationstate.TaskComplete || persisted.LeafStatus["final-fix"] != implementationstate.TaskComplete || persisted.FinalAcceptance == nil {
		t.Fatalf("durable machine accounting = %#v", persisted)
	}
	if len(persisted.RunOperations) < 8 || len(persisted.RunResults) < 8 {
		t.Fatalf("journal did not retain complete run accounting: operations=%d results=%d", len(persisted.RunOperations), len(persisted.RunResults))
	}
	if journalData, readErr := os.ReadFile(state.JournalPath()); readErr != nil || !strings.Contains(string(journalData), "final-review-2-result") {
		t.Fatalf("JSONL journal lacks final evidence: error=%v data=%q", readErr, journalData)
	}
	if branch := strings.TrimSpace(gitFixture(t, repository, "branch", "--show-current")); branch != "implementation" {
		t.Fatalf("final branch=%q, want implementation", branch)
	}
	if commits := strings.TrimSpace(gitFixture(t, repository, "rev-list", "--count", "HEAD")); commits != "3" {
		t.Fatalf("commit count=%q, want initial plus two verified local commits", commits)
	}
	if status := strings.TrimSpace(gitFixture(t, repository, "status", "--porcelain")); status != "" {
		t.Fatalf("final working copy is dirty: %q", status)
	}
	if remotes := strings.TrimSpace(gitFixture(t, repository, "remote")); remotes != "" {
		t.Fatalf("test repository unexpectedly has a remote: %q", remotes)
	}
	config, err := os.ReadFile(filepath.Join(repository, ".git", "config"))
	if err != nil || strings.Contains(string(config), "[remote ") {
		t.Fatalf("forbidden external Git configuration exists: error=%v config=%q", err, config)
	}
}

func e2ePayload(t *testing.T, kind ResponseKind, change func(map[string]any)) json.RawMessage {
	t.Helper()
	payload := responsePayloadMap(kind)
	if change != nil {
		change(payload)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func e2eRunExpectation(role ResponseRole, state ResponseState, run *implementationstate.Run, callID string) ResponseExpectation {
	return ResponseExpectation{Role: role, State: state, Scope: ResponseScopeRun, Binding: ResponseBinding{CallID: callID, RunID: run.Identity.ID, Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}}
}

func e2eAssignmentExpectation(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, briefID implementationstate.BriefID, callID string) ResponseExpectation {
	return ResponseExpectation{Role: ResponseRoleImplementer, State: ResponseStateImplementing, Scope: ResponseScopeAssignment, Binding: ResponseBinding{CallID: callID, RunID: run.Identity.ID, AssignmentID: assignmentID, BriefID: briefID, Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}}
}

func e2eRunImplementer(t *testing.T, ctx context.Context, run *implementationstate.Run, state *runstore.StateStore, journal *runstore.Run, repository string, workspace WorkspaceControl, checks setting.CheckSelection, runner CheckRunner, assignmentID implementationstate.AssignmentID, _ implementationstate.TaskID, turns []controlledTurn, requestFirst bool) AgentResponse {
	t.Helper()
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil || len(assignment.Briefs) == 0 {
		t.Fatalf("assignment %q has no brief", assignmentID)
	}
	briefID := assignment.Briefs[len(assignment.Briefs)-1].ID
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	phase := "initial"
	if !requestFirst {
		phase = "fix"
	}
	originOperation := implementationstate.OperationID(string(assignmentID) + "-" + phase + "-implement")
	if err := run.AddOperation(assignmentID, implementationstate.Operation{ID: originOperation, Kind: implementationstate.OperationAgent, BriefID: briefID, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	continuationOperation := implementationstate.OperationID(string(assignmentID) + "-" + phase + "-ready")
	if requestFirst {
		if err := run.AddOperation(assignmentID, implementationstate.Operation{ID: continuationOperation, Kind: implementationstate.OperationAgent, BriefID: briefID, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.Record(ctx, run); err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: turns}
	originCallID := string(originOperation) + "-call"
	transition := ImplementerTransitionInput{Run: run, Workspace: workspace, StateStore: state, Journal: journal, Repository: repository, AssignmentID: assignmentID, BriefID: briefID, Selection: checks, Runner: runner, Limits: controlledCallLimits(), OperationID: implementationstate.OperationID(string(assignmentID) + "-" + phase + "-requested-checks"), ResultID: implementationstate.ResultID(string(assignmentID) + "-" + phase + "-requested-checks-result")}
	route := ImplementerCheckRoute{OriginatingCall: ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "fake-executor"}, Repository: repository, Workspace: workspace, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: originCallID, AllowUnprotected: true}, Run: run, Journal: journal, StateStore: state, AssignmentID: assignmentID, OperationID: originOperation, Limits: controlledCallLimits(), Expectation: e2eAssignmentExpectation(run, assignmentID, briefID, originCallID), Message: "implement assignment"}, Transition: transition}
	if requestFirst {
		continuationCallID := string(continuationOperation) + "-call"
		route.Continuation = ControlledAgentCall{Repository: repository, Workspace: workspace, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: continuationCallID, AllowUnprotected: true}, Run: run, Journal: journal, StateStore: state, AssignmentID: assignmentID, OperationID: continuationOperation, Limits: controlledCallLimits(), Expectation: e2eAssignmentExpectation(run, assignmentID, briefID, continuationCallID)}
		route.ContinuationTransition = ImplementerTransitionInput{Run: run, Workspace: workspace, StateStore: state, Journal: journal, Repository: repository, AssignmentID: assignmentID, BriefID: briefID, Selection: checks, Runner: runner, Limits: controlledCallLimits(), OperationID: implementationstate.OperationID(string(assignmentID) + "-" + phase + "-required-checks"), ResultID: implementationstate.ResultID(string(assignmentID) + "-" + phase + "-required-checks-result")}
	} else {
		route.Transition.OperationID = implementationstate.OperationID(string(assignmentID) + "-" + phase + "-required-checks")
		route.Transition.ResultID = implementationstate.ResultID(string(assignmentID) + "-" + phase + "-required-checks-result")
	}
	result, err := RouteImplementerChecks(ctx, route)
	if err != nil || !result.ReviewReady || !result.RequiredChecks.Set.Succeeded() {
		t.Fatalf("implementer route=%#v error=%v", result, err)
	}
	if requestFirst {
		return result.ContinuationResponse
	}
	return result.Response
}

func e2eTaskReview(t *testing.T, ctx context.Context, run *implementationstate.Run, state *runstore.StateStore, journal *runstore.Run, repository string, workspace WorkspaceControl, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID, resultID implementationstate.ResultID, callID string, kind ResponseKind) TaskReviewResult {
	t.Helper()
	runtime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: e2ePayload(t, kind, nil)}}}
	owner := newSessionOwnerForTest(t, &explorerRoutingFactory{runtime: runtime})
	t.Cleanup(func() { _ = owner.Close() })
	result, err := StartTaskReview(ctx, TaskReviewInput{Owner: owner, Workspace: workspace, Run: run, StateStore: state, Journal: journal, Repository: repository, AssignmentID: assignmentID, OperationID: operationID, ResultID: resultID, CallID: callID, Limits: controlledCallLimits()})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func e2eAcceptAndCommit(ctx context.Context, run *implementationstate.Run, state *runstore.StateStore, journal *runstore.Run, repository string, workspace WorkspaceControl, assignmentID implementationstate.AssignmentID, ready AgentResponse, reviewResult implementationstate.ResultID, reflectionOperation implementationstate.OperationID, reflectionResult implementationstate.ResultID, reflectionCall string, commitOperation implementationstate.OperationID, reflect func()) (CommitAcceptedAssignmentResult, error) {
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil || len(assignment.Briefs) == 0 {
		return CommitAcceptedAssignmentResult{}, ErrAcceptanceReflection
	}
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	_, err := AcceptAssignmentAndReflectProgress(ctx, AcceptanceReflectionInput{
		Run: run, StateStore: state, Journal: journal, Repository: repository, Workspace: workspace,
		Session: &AgentSession{Role: ResponseRoleOrchestrator, runtime: &controlledCallRuntime{
			turns: []controlledTurn{{raw: e2ePayloadNoTest(ResponseProgressReflected), before: reflect}},
		}, thread: "fake-orchestrator"},
		AssignmentID: assignmentID,
		Acceptance: implementationstate.AcceptanceEvidence{
			BriefID: assignment.Briefs[len(assignment.Briefs)-1].ID, State: run.CurrentState, Basis: basis,
			CheckResultIDs: []implementationstate.ResultID{implementationstate.ResultID(string(assignmentID) + "-fix-required-checks-result")}, ReviewResultID: reviewResult,
		},
		TasksPath: "openspec/changes/change/tasks.md", ReflectionOperationID: reflectionOperation, ReflectionResultID: reflectionResult, ReflectionCallID: reflectionCall, Limits: controlledCallLimits(),
	})
	if err != nil {
		return CommitAcceptedAssignmentResult{}, err
	}
	snapshot, err := (GitWorkspaceControl{}).Capture(ctx, repository)
	if err != nil {
		return CommitAcceptedAssignmentResult{}, err
	}
	preparation, err := CommitPreparationFromSnapshot(snapshot)
	if err != nil {
		return CommitAcceptedAssignmentResult{}, err
	}
	return CommitAcceptedAssignment(ctx, CommitAcceptedAssignmentInput{Run: run, StateStore: state, Journal: journal, Repository: repository, AssignmentID: assignmentID, OperationID: commitOperation, Response: ready, Preparation: preparation, Control: GitCommitControl{}})
}

func e2ePayloadNoTest(kind ResponseKind) json.RawMessage {
	payload := responsePayloadMap(kind)
	raw, _ := json.Marshal(payload)
	return raw
}
