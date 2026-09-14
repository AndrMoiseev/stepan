package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestRefineBriefUsesExistingBrieferAndPublishesCurrentCodeRevision(t *testing.T) {
	raw := briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "refined brief grounded in the complete specification")
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: raw}}}
	run, store, journal, repository, owner, session := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()

	result, err := RefineBrief(context.Background(), refinementInput(run, store, journal, repository, owner, "refine-1", "refine-call"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Closed || result.Brief == nil || result.Brief.Number != 2 || result.Call.Session != session || len(run.Assignments[0].Briefs) != 2 || run.Assignments[0].Counters.BriefRefinement != 1 {
		t.Fatalf("unexpected refinement result: %#v, assignment=%#v", result, run.Assignments[0])
	}
	operationResult := assignmentResultForOperation(run, "assignment-1", "refine-1")
	if operationResult == nil || operationResult.Status != implementationstate.ResultSucceeded || len(operationResult.Evidence) != 2 || operationResult.Evidence[1] != result.Brief.Document {
		t.Fatalf("refined brief is not linked to durable response/result evidence: %#v", operationResult)
	}
	if len(runtime.messages) != 1 || !strings.Contains(runtime.messages[0], "Current assignment code diff") || !strings.Contains(runtime.messages[0], "Reported gap") {
		t.Fatalf("briefer did not receive current-code clarification packet: %#v", runtime.messages)
	}
	if same, err := owner.ExistingBriefer("assignment-1"); err != nil || same != session {
		t.Fatalf("brief refinement did not continue existing briefer session: %p, %v", same, err)
	}
}

func TestRefineBriefInvalidatesPendingAcceptanceOnNewVersion(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "revised contract")}}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	acceptForBriefRefinement(t, run, "assignment-1")
	if _, err := store.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	result, err := RefineBrief(context.Background(), refinementInput(run, store, journal, repository, owner, "refine-accepted", "accepted-call"))
	if err != nil {
		t.Fatal(err)
	}
	assignment := run.Assignments[0]
	if result.Brief == nil || assignment.Status != implementationstate.AssignmentActive || assignment.Acceptance != nil || len(assignment.AcceptanceHistory) != 1 || run.LeafStatus["A"] != implementationstate.TaskPending {
		t.Fatalf("old acceptance survived refined brief: assignment=%#v statuses=%#v", assignment, run.LeafStatus)
	}
}

func TestRefineBriefRejectsAnImpossibleOriginalTaskOrder(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{
		{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"B"}, "illegally reordered")},
		{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "retained original task order")},
	}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()

	result, err := RefineBrief(context.Background(), refinementInput(run, store, journal, repository, owner, "refine-order", "order-call"))
	if err != nil {
		t.Fatal(err)
	}
	operation := run.Assignments[0].Operations[len(run.Assignments[0].Operations)-1]
	if result.Call.Attempts != 2 || len(operation.Attempts) != 2 || operation.Attempts[0].Outcome != implementationstate.AttemptRejected || operation.Attempts[1].Outcome != implementationstate.AttemptSucceeded || result.Brief == nil || !sameTaskIDs(run, "assignment-1", []implementationstate.TaskID{"A"}) {
		t.Fatalf("impossible original order was accepted or not retried: result=%#v operation=%#v", result, operation)
	}
}

func TestRefineBriefAllowsThreeRefinementsAfterInitialBriefThenPauses(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{
		{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "revision one")},
		{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "revision two")},
		{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "revision three")},
	}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	for index := 1; index <= 3; index++ {
		suffix := string(rune('0' + rune(index)))
		if _, err := RefineBrief(context.Background(), refinementInput(run, store, journal, repository, owner, implementationstate.OperationID("refine-limit-"+suffix), "limit-call-"+suffix)); err != nil {
			t.Fatalf("refinement %d = %v", index, err)
		}
	}
	_, err := RefineBrief(context.Background(), refinementInput(run, store, journal, repository, owner, "refine-limit-4", "limit-call-4"))
	if !errors.Is(err, implementationstate.ErrLimitExceeded) || run.Status != implementationstate.RunPaused || run.LimitPause == nil || run.LimitPause.Counter != implementationstate.CycleCounterBriefRefinement || len(runtime.messages) != 3 {
		t.Fatalf("fourth refinement did not stop at configured post-initial limit: err=%v run=%#v messages=%d", err, run, len(runtime.messages))
	}
}

func TestRefineBriefClosesRunForMaterialProblemMissingFromSpecification(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseClarificationNeeded)}}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()

	result, err := RefineBrief(context.Background(), refinementInput(run, store, journal, repository, owner, "refine-close", "close-call"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Closed || run.Status != implementationstate.RunClosed || !strings.Contains(run.CloseReason, "which behavior is required?") {
		t.Fatalf("unresolved material specification issue did not close run: %#v", run)
	}
	operationResult := assignmentResultForOperation(run, "assignment-1", "refine-close")
	if operationResult == nil || operationResult.Status != implementationstate.ResultSucceeded || len(operationResult.Evidence) != 1 || !strings.Contains(run.CloseReason, "boundaries:") || !strings.Contains(run.CloseReason, "references:") {
		t.Fatalf("closed clarification was not preserved as durable response evidence: result=%#v close=%q", operationResult, run.CloseReason)
	}
	if err := run.Resume(); !errors.Is(err, implementationstate.ErrInvalidTransition) {
		t.Fatalf("closed specification issue unexpectedly resumed: %v", err)
	}
}

func TestRefineBriefRecoversDurableReadyAndClarificationResultsWithoutNewTurn(t *testing.T) {
	for _, test := range []struct {
		name   string
		raw    func(*testing.T) json.RawMessage
		closed bool
	}{
		{name: "brief_ready", raw: func(t *testing.T) json.RawMessage {
			return briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "durable revision")
		}},
		{name: "clarification", raw: func(t *testing.T) json.RawMessage { return responsePayload(t, ResponseClarificationNeeded) }, closed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: test.raw(t)}}}
			run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
			defer store.Close()
			defer owner.Close()
			input := refinementInput(run, store, journal, repository, owner, "refine-recover", "recover-call")
			first, err := RefineBrief(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			second, err := RefineBrief(context.Background(), input)
			if err != nil || second.Closed != test.closed || (test.closed && !first.Closed) || (!test.closed && (second.Brief == nil || first.Brief == nil || second.Brief.ID != first.Brief.ID)) || len(runtime.messages) != 1 {
				t.Fatalf("durable result was not recovered idempotently: first=%#v second=%#v err=%v turns=%d", first, second, err, len(runtime.messages))
			}
		})
	}
}

func TestRefineBriefRecoversAfterPersistFailureFollowingAgentResponse(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "durable despite projection failure")}}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	input := refinementInput(run, store, journal, repository, owner, "refine-persist", "persist-call")
	original := recordBriefRefinementState
	calls := 0
	recordBriefRefinementState = func(ctx context.Context, stateStore *runstore.StateStore, state *implementationstate.Run) (implementationstate.Event, error) {
		calls++
		event, err := original(ctx, stateStore, state)
		if calls == 2 && err == nil {
			return event, errors.New("simulated projection failure after durable event")
		}
		return event, err
	}
	t.Cleanup(func() { recordBriefRefinementState = original })
	if _, err := RefineBrief(context.Background(), input); err == nil {
		t.Fatal("RefineBrief() error = nil, want simulated durable persistence error")
	}
	recovered, err := RefineBrief(context.Background(), input)
	if err != nil || recovered.Brief == nil || len(runtime.messages) != 1 || assignmentResultForOperation(run, "assignment-1", "refine-persist") == nil {
		t.Fatalf("durable refinement was not recovered after persistence failure: result=%#v err=%v turns=%d", recovered, err, len(runtime.messages))
	}
}

func TestRefineBriefCancellationLeavesNoHalfConsumedResponseAndCanRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &controlledCallRuntime{turns: []controlledTurn{{waitForInterrupt: true, before: cancel}, {raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "retry after cancellation")}}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	input := refinementInput(run, store, journal, repository, owner, "refine-cancel", "cancel-call")
	if _, err := RefineBrief(ctx, input); !errors.Is(err, ErrAgentCallCancelled) {
		t.Fatalf("cancelled refinement error = %v", err)
	}
	if assignmentResultForOperation(run, "assignment-1", "refine-cancel") != nil {
		t.Fatal("cancelled turn unexpectedly acquired a response result")
	}
	result, err := RefineBrief(context.Background(), input)
	if err != nil || result.Brief == nil || len(runtime.messages) != 2 {
		t.Fatalf("cancelled refinement could not safely retry: result=%#v err=%v turns=%d", result, err, len(runtime.messages))
	}
}

func TestRefineBriefPersistsExecutionBlockedWithoutTechnicalRetry(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseExecutionBlocked)}}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	result, err := RefineBrief(context.Background(), refinementInput(run, store, journal, repository, owner, "refine-blocked", "blocked-call"))
	if err != nil {
		t.Fatal(err)
	}
	stored := assignmentResultForOperation(run, "assignment-1", "refine-blocked")
	if !result.Paused || run.Status != implementationstate.RunPaused || len(runtime.messages) != 1 || stored == nil || stored.Status != implementationstate.ResultFailed || !strings.Contains(run.PauseReason, "required user action") {
		t.Fatalf("execution_blocked did not preserve a resumable diagnostic: result=%#v run=%#v stored=%#v", result, run, stored)
	}
}

func TestRefineBriefRoutesExplorerThenContinuesSameBrieferSession(t *testing.T) {
	explore := responsePayloadMap(ResponseExplorationRequested)
	explore["question"], explore["context"], explore["boundaries"], explore["known_facts"] = "Where is the specified behavior?", "Need an unambiguous specification reference.", "Inspect the current repository only.", []string{"the current brief lacks the rule"}
	exploreRaw, err := json.Marshal(explore)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: exploreRaw}, {raw: responsePayload(t, ResponseExplorationResult)}, {raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "brief after research")}}}
	run, store, journal, repository, owner, session := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	input := refinementInput(run, store, journal, repository, owner, "refine-explore", "explore-source")
	input.Explorer = &BriefRefinementExplorer{ExplorerOperationID: "refine-explorer", ExplorerCallID: "explorer-call", ContinuationOperationID: "refine-after-explorer", ContinuationResultID: "refine-after-explorer-result"}
	result, err := RefineBrief(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Brief == nil || result.Call.Session != session || len(runtime.messages) != 3 || assignmentResultForOperation(run, "assignment-1", "refine-after-explorer") == nil {
		t.Fatalf("Explorer did not return to the same effective briefer session: result=%#v messages=%#v", result, runtime.messages)
	}
}

func briefRefinementFixture(t *testing.T, runtime *controlledCallRuntime) (*implementationstate.Run, *runstore.StateStore, *runstore.Run, string, *SessionOwner, *AgentSession) {
	t.Helper()
	run, store, journal, repository, _ := newBriefSelectionFixture(t)
	run.Identity.BaselineCommit = strings.TrimSpace(git(t, repository, "rev-parse", "HEAD"))
	if err := run.StartAssignment("assignment-1", []implementationstate.TaskID{"A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := PersistBriefVersion(context.Background(), journal, store, run, "assignment-1", []implementationstate.TaskID{"A"}, "initial self-contained brief"); err != nil {
		t.Fatal(err)
	}
	owner := newSessionOwnerForTest(t, &briefRefinementRuntimeFactory{runtime: runtime})
	start, err := BuildBrieferStartContext(journal, run, "assignment-1")
	if err != nil {
		t.Fatal(err)
	}
	session, err := owner.Briefer(context.Background(), "assignment-1", start)
	if err != nil {
		t.Fatal(err)
	}
	return run, store, journal, repository, owner, session
}

func refinementInput(run *implementationstate.Run, store *runstore.StateStore, journal *runstore.Run, repository string, owner *SessionOwner, operationID implementationstate.OperationID, callID string) BriefRefinementInput {
	assignment := run.Assignments[0]
	brief := assignment.Briefs[len(assignment.Briefs)-1]
	question, contextText, boundaries := "Which explicitly specified validation behavior applies?", "The executor found a gap in the initial brief.", "Do not introduce a new public contract."
	request := AgentResponse{Kind: ResponseClarificationNeeded, Question: &question, Context: &contextText, Boundaries: &boundaries, References: []string{"spec.md#validation"}, Binding: ResponseBinding{CallID: "executor-gap", RunID: run.Identity.ID, AssignmentID: assignment.ID, BriefID: brief.ID, Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}}
	return BriefRefinementInput{Owner: owner, Run: run, StateStore: store, Journal: journal, Repository: repository, AssignmentID: assignment.ID, RequesterRole: ResponseRoleImplementer, Request: request, OperationID: operationID, ResultID: implementationstate.ResultID(operationID + "-result"), CallID: callID, Limits: controlledCallLimits()}
}

func acceptForBriefRefinement(t *testing.T, run *implementationstate.Run, assignmentID implementationstate.AssignmentID) {
	t.Helper()
	assignment := run.Assignments[0]
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	for _, operation := range []implementationstate.Operation{
		{ID: "refinement-check", Kind: implementationstate.OperationCheck, BriefID: assignment.Briefs[0].ID, Basis: basis},
		{ID: "refinement-review", Kind: implementationstate.OperationReview, BriefID: assignment.Briefs[0].ID, Basis: basis},
	} {
		if err := run.AddOperation(assignmentID, operation); err != nil {
			t.Fatal(err)
		}
		if _, err := run.StartAssignmentAttempt(assignmentID, operation.ID); err != nil {
			t.Fatal(err)
		}
		if err := run.AddResult(assignmentID, implementationstate.OperationResult{ID: implementationstate.ResultID(operation.ID + "-result"), OperationID: operation.ID, Status: implementationstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	intent := implementationstate.CommitIntent{OperationID: "refinement-commit", ParentCommit: "base", Tree: "tree", Message: "accept initial brief"}
	if err := run.AcceptAssignment(assignmentID, implementationstate.AcceptanceEvidence{BriefID: assignment.Briefs[0].ID, State: run.CurrentState, Basis: basis, CheckResultIDs: []implementationstate.ResultID{"refinement-check-result"}, ReviewResultID: "refinement-review-result", PendingCommit: intent}); err != nil {
		t.Fatal(err)
	}
}

type briefRefinementRuntimeFactory struct{ runtime agentruntime.Runtime }

func (factory *briefRefinementRuntimeFactory) Preflight(implementationconfig.RuntimeProfile) error {
	return nil
}
func (factory *briefRefinementRuntimeFactory) Create(context.Context, implementationconfig.RuntimeProfile) (agentruntime.Runtime, error) {
	return factory.runtime, nil
}
