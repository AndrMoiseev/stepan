package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	repository := newFilesystemWorkspace(t)
	for _, test := range []struct {
		name      string
		zeroEvent bool
	}{{"durable_event", false}, {"zero_event", true}} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "durable despite persistence failure")}}}
			run, store, journal, repository, owner, _ := briefRefinementFixtureInRepository(t, runtime, repository)
			defer store.Close()
			defer owner.Close()
			operationID := implementationstate.OperationID("refine-persist")
			if test.zeroEvent {
				operationID = "refine-zero-event"
			}
			input := refinementInput(run, store, journal, repository, owner, operationID, "persist-call")
			original, calls := recordBriefRefinementState, 0
			t.Cleanup(func() { recordBriefRefinementState = original })
			recordBriefRefinementState = func(ctx context.Context, stateStore *runstore.StateStore, state *implementationstate.Run) (implementationstate.Event, error) {
				calls++
				if calls == 2 && test.zeroEvent {
					return implementationstate.Event{}, errors.New("simulated zero-event state failure")
				}
				event, err := original(ctx, stateStore, state)
				if calls == 2 && err == nil {
					return event, errors.New("simulated projection failure after durable event")
				}
				return event, err
			}
			if _, err := RefineBrief(context.Background(), input); err == nil {
				t.Fatal("RefineBrief() error = nil, want simulated persistence error")
			}
			recordBriefRefinementState = original
			recovered, err := RefineBrief(context.Background(), input)
			if err != nil || recovered.Brief == nil || len(runtime.messages) != 1 || assignmentResultForOperation(run, "assignment-1", operationID) == nil {
				t.Fatalf("published response was not recovered: result=%#v err=%v turns=%d", recovered, err, len(runtime.messages))
			}
		})
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
	if !result.Paused || run.Status != implementationstate.RunPaused || run.ExecutionBlock == nil || run.ExecutionBlock.Diagnostic != "tool is not installed" || run.ExecutionBlock.RequiredUserAction != "install the configured tool" || !slices.Equal(run.ExecutionBlock.Attempts, []string{"checked PATH", "read project settings"}) || len(runtime.messages) != 1 || stored == nil || stored.Status != implementationstate.ResultFailed {
		t.Fatalf("execution_blocked did not preserve a resumable diagnostic: result=%#v run=%#v stored=%#v", result, run, stored)
	}
}

func TestRefineBriefRoutesExplorerThenContinuesSameBrieferSession(t *testing.T) {
	exploreRaw := briefExplorationRequest(t)
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: exploreRaw}, {raw: responsePayload(t, ResponseExplorationResult)}, {raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "brief after research")}}}
	run, store, journal, repository, owner, session := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	input := refinementInput(run, store, journal, repository, owner, "refine-explore", "explore-source")
	input.Explorer = &BriefRefinementExplorer{ExplorerOperationID: "refine-explorer", ExplorerResultID: "refine-explorer-result", ExplorerCallID: "explorer-call", ContinuationOperationID: "refine-after-explorer", ContinuationResultID: "refine-after-explorer-result"}
	result, err := RefineBrief(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	explorerResult := assignmentResultForOperation(run, "assignment-1", "refine-explorer")
	if result.Brief == nil || result.Call.Session != session || len(runtime.messages) != 3 || explorerResult == nil || len(explorerResult.Evidence) != 1 || assignmentResultForOperation(run, "assignment-1", "refine-after-explorer") == nil {
		t.Fatalf("Explorer did not return to the same effective briefer session: result=%#v messages=%#v", result, runtime.messages)
	}
}

func TestRefineBriefRestartBootstrapsFullRefinementContextBeforeDurableExplorerContinuation(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{
		{raw: briefExplorationRequest(t)},
		{raw: responsePayload(t, ResponseExplorationResult)},
		{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "brief after recovered research")},
	}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	input := refinementInput(run, store, journal, repository, owner, "refine-restart-context", "restart-context-call")
	input.Explorer = &BriefRefinementExplorer{ExplorerOperationID: "restart-context-explorer", ExplorerResultID: "restart-context-explorer-result", ExplorerCallID: "restart-context-explorer-call", ContinuationOperationID: "restart-context-continuation", ContinuationResultID: "restart-context-continuation-result"}

	original, calls := recordBriefRefinementState, 0
	t.Cleanup(func() { recordBriefRefinementState = original })
	recordBriefRefinementState = func(ctx context.Context, stateStore *runstore.StateStore, state *implementationstate.Run) (implementationstate.Event, error) {
		calls++
		if calls == 4 { // Explorer response is durable; its projection fails before continuation.
			event, err := original(ctx, stateStore, state)
			if err != nil {
				return event, err
			}
			return event, errors.New("simulated restart after durable Explorer result")
		}
		return original(ctx, stateStore, state)
	}
	if _, err := RefineBrief(context.Background(), input); err == nil {
		t.Fatal("initial refinement error = nil, want simulated restart")
	}
	recordBriefRefinementState = original
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	restartedOwner := newSessionOwnerForTest(t, &briefRefinementRuntimeFactory{runtime: runtime})
	defer restartedOwner.Close()
	input.Owner = restartedOwner
	result, err := RefineBrief(context.Background(), input)
	if err != nil || result.Brief == nil || result.Brief.Number != 2 {
		t.Fatalf("restart continuation = %#v, %v", result, err)
	}
	if len(runtime.configs) != 3 {
		t.Fatalf("started sessions = %d, want original briefer, Explorer, and replacement briefer", len(runtime.configs))
	}
	bootstrap := runtime.configs[2].BootstrapInstructions
	for _, marker := range []string{
		"# Active brief refinement recovery", "initial self-contained brief", "Which explicitly specified validation behavior applies?",
		"# Current assignment code diff", "# Durable Explorer history", "validation is centralized", "parser validates input",
	} {
		if !strings.Contains(bootstrap, marker) {
			t.Fatalf("replacement briefer bootstrap lacks %q:\n%s", marker, bootstrap)
		}
	}
	if len(runtime.messages) != 3 || !strings.Contains(runtime.messages[2], "Explorer result") {
		t.Fatalf("durable Explorer result was not continued after bootstrap: %#v", runtime.messages)
	}
}

func TestRefineBriefRecoversExplorerAfterPersistenceFailures(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	for _, test := range []struct {
		name   string
		failAt int
	}{{"after_source_request", 3}, {"after_explorer_result", 4}} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefExplorationRequest(t)}, {raw: responsePayload(t, ResponseExplorationResult)}, {raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "recovered Explorer route")}}}
			run, store, journal, repository, owner, _ := briefRefinementFixtureInRepository(t, runtime, repository)
			defer store.Close()
			defer owner.Close()
			input := refinementInput(run, store, journal, repository, owner, "refine-explorer-recovery", "explorer-recovery-call")
			input.Explorer = &BriefRefinementExplorer{ExplorerOperationID: "explorer-recovery", ExplorerResultID: "explorer-recovery-result", ExplorerCallID: "explorer-recovery-call", ContinuationOperationID: "after-explorer-recovery", ContinuationResultID: "after-explorer-recovery-result"}
			original, calls := recordBriefRefinementState, 0
			t.Cleanup(func() { recordBriefRefinementState = original })
			recordBriefRefinementState = func(ctx context.Context, stateStore *runstore.StateStore, state *implementationstate.Run) (implementationstate.Event, error) {
				calls++
				if calls == test.failAt {
					return implementationstate.Event{}, errors.New("simulated zero-event state failure")
				}
				return original(ctx, stateStore, state)
			}
			if _, err := RefineBrief(context.Background(), input); err == nil {
				t.Fatal("first refinement error = nil")
			}
			recordBriefRefinementState = original
			result, err := RefineBrief(context.Background(), input)
			if err != nil || result.Brief == nil || len(runtime.messages) != 3 || assignmentResult(run, "assignment-1", "explorer-recovery-result") == nil {
				t.Fatalf("Explorer workflow did not resume from durable boundary: result=%#v err=%v messages=%d", result, err, len(runtime.messages))
			}
		})
	}
}

func TestRefineBriefRoutesPublishedExplorerRequestAfterZeroEventFailure(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{
		{raw: briefExplorationRequest(t)},
		{raw: responsePayload(t, ResponseExplorationResult)},
		{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "recovered Explorer route")},
	}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	input := refinementInput(run, store, journal, repository, owner, "refine-published-request", "published-request-call")
	input.Explorer = &BriefRefinementExplorer{ExplorerOperationID: "published-request-explorer", ExplorerResultID: "published-request-explorer-result", ExplorerCallID: "published-request-explorer-call", ContinuationOperationID: "published-request-continuation", ContinuationResultID: "published-request-continuation-result"}

	original, calls := recordBriefRefinementState, 0
	t.Cleanup(func() { recordBriefRefinementState = original })
	recordBriefRefinementState = func(ctx context.Context, stateStore *runstore.StateStore, state *implementationstate.Run) (implementationstate.Event, error) {
		calls++
		if calls == 2 { // The request artifact exists, but its state event does not.
			return implementationstate.Event{}, errors.New("simulated zero-event state failure")
		}
		return original(ctx, stateStore, state)
	}
	if _, err := RefineBrief(context.Background(), input); err == nil {
		t.Fatal("first refinement error = nil")
	}
	recordBriefRefinementState = original

	result, err := RefineBrief(context.Background(), input)
	if err != nil || result.Brief == nil || len(runtime.messages) != 3 || assignmentResult(run, "assignment-1", "published-request-explorer-result") == nil {
		t.Fatalf("published Explorer request was not resumed exactly once: result=%#v err=%v messages=%d", result, err, len(runtime.messages))
	}
}

func TestRefineBriefRoutesRecoveredSecondExplorerRequest(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{
		{raw: briefExplorationRequest(t)},
		{raw: responsePayload(t, ResponseExplorationResult)},
		{raw: briefExplorationRequest(t)},
		{raw: responsePayload(t, ResponseExplorationResult)},
		{raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "brief after recovered second research")},
	}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	input := refinementInput(run, store, journal, repository, owner, "refine-recovered-second-explorer", "recovered-second-explorer-call")
	input.Explorer = &BriefRefinementExplorer{
		ExplorerOperationID: "recovered-second-explorer-one", ExplorerResultID: "recovered-second-explorer-one-result", ExplorerCallID: "recovered-second-explorer-one-call", ContinuationOperationID: "recovered-second-after-one", ContinuationResultID: "recovered-second-after-one-result",
		Additional: []BriefRefinementExplorer{{ExplorerOperationID: "recovered-second-explorer-two", ExplorerResultID: "recovered-second-explorer-two-result", ExplorerCallID: "recovered-second-explorer-two-call", ContinuationOperationID: "recovered-second-after-two", ContinuationResultID: "recovered-second-after-two-result"}},
	}

	original, calls := recordBriefRefinementState, 0
	t.Cleanup(func() { recordBriefRefinementState = original })
	recordBriefRefinementState = func(ctx context.Context, stateStore *runstore.StateStore, state *implementationstate.Run) (implementationstate.Event, error) {
		calls++
		if calls == 5 { // The second source response is published but not yet recorded.
			return implementationstate.Event{}, errors.New("simulated zero-event state failure")
		}
		return original(ctx, stateStore, state)
	}
	if _, err := RefineBrief(context.Background(), input); err == nil {
		t.Fatal("first refinement error = nil")
	}
	recordBriefRefinementState = original

	result, err := RefineBrief(context.Background(), input)
	if err != nil || result.Brief == nil || len(runtime.messages) != 5 || assignmentResult(run, "assignment-1", "recovered-second-explorer-two-result") == nil {
		t.Fatalf("recovered second Explorer request was not routed: result=%#v err=%v messages=%d", result, err, len(runtime.messages))
	}
}

func TestRefineBriefSupportsConsecutiveExplorerRequestsWithoutSecondRefinementRound(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefExplorationRequest(t)}, {raw: responsePayload(t, ResponseExplorationResult)}, {raw: briefExplorationRequest(t)}, {raw: responsePayload(t, ResponseExplorationResult)}, {raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "brief after two research episodes")}}}
	run, store, journal, repository, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	input := refinementInput(run, store, journal, repository, owner, "refine-two-explorers", "two-explorers-call")
	input.Explorer = &BriefRefinementExplorer{ExplorerOperationID: "explorer-one", ExplorerResultID: "explorer-one-result", ExplorerCallID: "explorer-one-call", ContinuationOperationID: "after-explorer-one", ContinuationResultID: "after-explorer-one-result", Additional: []BriefRefinementExplorer{{ExplorerOperationID: "explorer-two", ExplorerResultID: "explorer-two-result", ExplorerCallID: "explorer-two-call", ContinuationOperationID: "after-explorer-two", ContinuationResultID: "after-explorer-two-result"}}}
	result, err := RefineBrief(context.Background(), input)
	if err != nil || result.Brief == nil || len(runtime.messages) != 5 || run.Assignments[0].Counters.BriefRefinement != 1 || assignmentResult(run, "assignment-1", "explorer-two-result") == nil {
		t.Fatalf("consecutive Explorer route consumed refinement or lost a result: result=%#v err=%v messages=%d counters=%#v", result, err, len(runtime.messages), run.Assignments[0].Counters)
	}
}

func TestRefineBriefUsesWorkspaceControlAcrossConsecutiveExplorerTurns(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefExplorationRequest(t)}, {raw: responsePayload(t, ResponseExplorationResult)}, {raw: briefExplorationRequest(t)}, {raw: responsePayload(t, ResponseExplorationResult)}, {raw: briefReadyResponseWithContent(t, []implementationstate.TaskID{"A"}, "brief after two research episodes")}}}
	run, store, journal, _, owner, _ := briefRefinementFixture(t, runtime)
	defer store.Close()
	defer owner.Close()
	input := refinementInput(run, store, journal, t.TempDir(), owner, "refine-workspace-seam", "workspace-seam-call")
	input.Explorer = &BriefRefinementExplorer{ExplorerOperationID: "workspace-explorer-one", ExplorerResultID: "workspace-explorer-one-result", ExplorerCallID: "workspace-explorer-one-call", ContinuationOperationID: "workspace-after-one", ContinuationResultID: "workspace-after-one-result", Additional: []BriefRefinementExplorer{{ExplorerOperationID: "workspace-explorer-two", ExplorerResultID: "workspace-explorer-two-result", ExplorerCallID: "workspace-explorer-two-call", ContinuationOperationID: "workspace-after-two", ContinuationResultID: "workspace-after-two-result"}}}
	workspace := &unchangedWorkspaceControl{}
	input.Workspace = workspace

	result, err := RefineBrief(context.Background(), input)
	if err != nil || result.Brief == nil {
		t.Fatalf("refinement through workspace seam: result=%#v err=%v", result, err)
	}
	if workspace.assignmentDiffs != 1 || workspace.captures != 10 || workspace.diffs != 5 {
		t.Fatalf("workspace operations = assignment diffs:%d captures:%d diffs:%d", workspace.assignmentDiffs, workspace.captures, workspace.diffs)
	}
}

func briefExplorationRequest(t *testing.T) json.RawMessage {
	t.Helper()
	explore := responsePayloadMap(ResponseExplorationRequested)
	explore["question"], explore["context"], explore["boundaries"], explore["known_facts"] = "Where is the specified behavior?", "Need an unambiguous specification reference.", "Inspect the current repository only.", []string{"the current brief lacks the rule"}
	raw, err := json.Marshal(explore)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func briefRefinementFixture(t *testing.T, runtime *controlledCallRuntime) (*implementationstate.Run, *runstore.StateStore, *runstore.Run, string, *SessionOwner, *AgentSession) {
	t.Helper()
	return briefRefinementFixtureInRepository(t, runtime, newFilesystemWorkspace(t))
}

func briefRefinementFixtureInRepository(t *testing.T, runtime *controlledCallRuntime, repository string) (*implementationstate.Run, *runstore.StateStore, *runstore.Run, string, *SessionOwner, *AgentSession) {
	t.Helper()
	run, store, journal, repository, _ := newBriefSelectionFixtureInRepository(t, repository)
	run.Identity.BaselineCommit = "test-baseline"
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
	return BriefRefinementInput{Owner: owner, Workspace: &unchangedWorkspaceControl{}, Run: run, StateStore: store, Journal: journal, Repository: repository, AssignmentID: assignment.ID, RequesterRole: ResponseRoleImplementer, Request: request, OperationID: operationID, ResultID: implementationstate.ResultID(operationID + "-result"), CallID: callID, Limits: controlledCallLimits()}
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
