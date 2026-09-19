package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

func TestRouteExplorerReturnsResultToSourceAndPreservesEpisodeCounter(t *testing.T) {
	first := explorationResponseWithFact(t, "short message", strings.Repeat(string(rune(0x044F)), 250))
	second := explorationResponse(t, "validation is centralized")
	runtime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: first}, {raw: second}}}
	factory := &explorerRoutingFactory{runtime: runtime}
	owner := newSessionOwnerForTest(t, factory)
	t.Cleanup(func() { _ = owner.Close() })

	call := controlledCallFixture(t, &controlledCallRuntime{})
	call.Run.RunOperations[0].Counter = implstate.CycleCounterExplorer
	call.Run.RunOperations[0].Episode = "final-review"
	call.Expectation = explorerExpectationFrom(t, expectationFor(ResponseRoleFinalReviewer, ResponseReviewPassed), "explorer-call")
	call.Policy = AgentCallPolicy{Role: AgentRoleExplorer, CallID: call.Expectation.Binding.CallID}
	source := expectationFor(ResponseRoleFinalReviewer, ResponseReviewPassed)
	sourceRuntime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseReviewPassed)}}}
	sourceSession := &AgentSession{Role: ResponseRoleFinalReviewer, runtime: sourceRuntime, thread: "source"}

	result, err := RouteExplorer(context.Background(), ExplorerRoute{
		Owner: owner, SourceSession: sourceSession, SourceExpectation: source,
		Request: explorationRequest(t), ExplorerCall: call,
		SourceContinuation: sourceContinuationCall(t, call, source), ExplorerCharacters: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 || result.Response.Message == nil || *result.Response.Message != "validation is centralized" {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.ContinuationMessage, "Confirmed facts") || !strings.Contains(result.ContinuationMessage, "validation is centralized") {
		t.Fatalf("continuation = %q", result.ContinuationMessage)
	}
	if factory.created != 1 || len(runtime.messages) != 2 {
		t.Fatalf("Explorer did not shorten in its original session: runtimes=%d messages=%#v", factory.created, runtime.messages)
	}
	if !strings.Contains(runtime.messages[1], "exceeds 200 Unicode characters") {
		t.Fatalf("shortening request = %q", runtime.messages[1])
	}
	if result.SourceContinuationResponse.Kind != ResponseReviewPassed || result.SourceContinuationAttempts != 1 || len(sourceRuntime.messages) != 1 || !strings.Contains(sourceRuntime.messages[0], "Explorer result") {
		t.Fatalf("source continuation was not dispatched on the original thread: result=%#v messages=%#v", result, sourceRuntime.messages)
	}
	current, _, err := call.StateStore.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := current.RunExplorerCounters["final-review"]; got != 1 {
		t.Fatalf("Explorer counter after returning to source = %d, want 1", got)
	}
	attempts := current.RunOperations[0].Attempts
	if len(attempts) != 2 || attempts[0].Outcome != implstate.AttemptRejected || attempts[1].Outcome != implstate.AttemptSucceeded {
		t.Fatalf("durable technical attempts = %#v", attempts)
	}
}

func TestRouteExplorerContinuesValidEscalationsWithoutSizeRetry(t *testing.T) {
	for _, kind := range []ResponseKind{ResponseClarificationNeeded} {
		t.Run(string(kind), func(t *testing.T) {
			explorerRuntime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: responsePayload(t, kind)}}}
			owner := newSessionOwnerForTest(t, &explorerRoutingFactory{runtime: explorerRuntime})
			t.Cleanup(func() { _ = owner.Close() })
			call := controlledCallFixture(t, &controlledCallRuntime{})
			call.Run.RunOperations[0].Counter = implstate.CycleCounterExplorer
			call.Run.RunOperations[0].Episode = "final-review"
			source := expectationFor(ResponseRoleFinalReviewer, ResponseReviewPassed)
			call.Expectation = explorerExpectationFrom(t, source, "explorer-call")
			call.Policy = AgentCallPolicy{Role: AgentRoleExplorer, CallID: call.Expectation.Binding.CallID}
			sourceRuntime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseReviewPassed)}}}
			result, err := RouteExplorer(context.Background(), ExplorerRoute{
				Owner: owner, SourceSession: &AgentSession{Role: ResponseRoleFinalReviewer, runtime: sourceRuntime, thread: "source"},
				SourceExpectation: source, Request: explorationRequest(t), ExplorerCall: call,
				SourceContinuation: sourceContinuationCall(t, call, source),
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Response.Kind != kind || result.Attempts != 1 || len(explorerRuntime.messages) != 1 || len(sourceRuntime.messages) != 1 {
				t.Fatalf("Explorer escalation route = %#v, explorer=%#v source=%#v", result, explorerRuntime.messages, sourceRuntime.messages)
			}
			if kind == ResponseClarificationNeeded && !strings.Contains(result.ContinuationMessage, "requires clarification") || kind == ResponseExecutionBlocked && !strings.Contains(result.ContinuationMessage, "execution blocked") {
				t.Fatalf("kind-specific continuation = %q", result.ContinuationMessage)
			}
		})
	}
}

func TestRouteExplorerExecutionBlockedDurablyPausesWithoutSourceContinuation(t *testing.T) {
	explorerRuntime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseExecutionBlocked)}}}
	owner := newSessionOwnerForTest(t, &explorerRoutingFactory{runtime: explorerRuntime})
	t.Cleanup(func() { _ = owner.Close() })
	call := controlledCallFixture(t, &controlledCallRuntime{})
	call.Run.RunOperations[0].Counter = implstate.CycleCounterExplorer
	call.Run.RunOperations[0].Episode = "final-review"
	source := expectationFor(ResponseRoleFinalReviewer, ResponseReviewPassed)
	call.Expectation = explorerExpectationFrom(t, source, "explorer-call")
	call.Policy = AgentCallPolicy{Role: AgentRoleExplorer, CallID: call.Expectation.Binding.CallID}
	sourceRuntime := &explorerRoutingRuntime{}

	result, err := RouteExplorer(context.Background(), ExplorerRoute{
		Owner: owner, SourceSession: &AgentSession{Role: ResponseRoleFinalReviewer, runtime: sourceRuntime, thread: "source"},
		SourceExpectation: source, Request: explorationRequest(t), ExplorerCall: call,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Paused || result.SourceContinuationAttempts != 0 || len(sourceRuntime.messages) != 0 {
		t.Fatalf("execution-blocked route advanced the source: %#v, source=%#v", result, sourceRuntime.messages)
	}
	if err := call.StateStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runstore.OpenState(call.Journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	current, _, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != implstate.RunPaused || current.ExecutionBlock == nil || current.ExecutionBlock.Diagnostic != "tool is not installed" || current.ExecutionBlock.RequiredUserAction != "install the configured tool" || !slices.Equal(current.ExecutionBlock.Attempts, []string{"checked PATH", "read project settings"}) {
		t.Fatalf("durable execution-blocked pause = %#v", current)
	}
}

func TestRouteExplorerAfterRestartUsesDurableAuxiliaryResultAndFreshSourceSession(t *testing.T) {
	call := controlledCallFixture(t, &controlledCallRuntime{})
	call.Run.RunOperations[0].Counter = implstate.CycleCounterExplorer
	call.Run.RunOperations[0].Episode = "restart"
	source := expectationFor(ResponseRoleFinalReviewer, ResponseReviewPassed)
	call.Expectation = explorerExpectationFrom(t, source, "restart-explorer-call")
	call.Policy = AgentCallPolicy{Role: AgentRoleExplorer, CallID: call.Expectation.Binding.CallID}
	continuation := sourceContinuationCall(t, call, source)
	if _, _, err := call.StateStore.RecordRunAttemptStartWithLimits(context.Background(), call.Run, call.OperationID, call.Limits); err != nil {
		t.Fatal(err)
	}
	response, err := BindAgentResponse(call.Expectation, explorationResponse(t, "saved before restart"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := call.Journal.Publish("restart-explorer-result-response", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Run.AddRunResult(implstate.OperationResult{ID: "restart-explorer-result", OperationID: call.OperationID, Status: implstate.ResultSucceeded, State: call.Run.CurrentState, Basis: call.Run.RunOperations[0].Basis, Evidence: []implstate.EvidenceRef{evidence}}); err != nil {
		t.Fatal(err)
	}
	if _, err := call.StateStore.Record(context.Background(), call.Run); err != nil {
		t.Fatal(err)
	}
	if err := call.StateStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runstore.OpenState(call.Journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, _, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	call.Run, call.StateStore = recovered, reopened
	continuation.Run, continuation.StateStore = recovered, reopened

	factory := &explorerRoutingFactory{runtime: &explorerRoutingRuntime{}}
	owner := newSessionOwnerForTest(t, factory)
	t.Cleanup(func() { _ = owner.Close() })
	sourceRuntime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseReviewPassed)}}}
	result, err := RouteExplorer(context.Background(), ExplorerRoute{
		Owner: owner, SourceSession: &AgentSession{Role: ResponseRoleFinalReviewer, runtime: sourceRuntime, thread: "fresh-source"},
		SourceExpectation: source, Request: explorationRequest(t), ExplorerCall: call, ExplorerResultID: "restart-explorer-result", SourceContinuation: continuation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.created != 0 || result.Response.Message == nil || *result.Response.Message != "saved before restart" || len(sourceRuntime.messages) != 1 || !strings.Contains(sourceRuntime.messages[0], "saved before restart") {
		t.Fatalf("restart route repeated completed Explorer work: result=%#v created=%d source=%#v", result, factory.created, sourceRuntime.messages)
	}
	if operation := recovered.RunOperations[0]; len(operation.Attempts) != 1 || recovered.RunExplorerCounters["restart"] != 1 {
		t.Fatalf("restart route changed Explorer accounting: %#v counters=%#v", operation, recovered.RunExplorerCounters)
	}
}

func TestRouteExplorerAfterRestartRestartsInterruptedAuxiliaryWithinExistingCounter(t *testing.T) {
	call := controlledCallFixture(t, &controlledCallRuntime{})
	call.Run.RunOperations[0].Counter = implstate.CycleCounterExplorer
	call.Run.RunOperations[0].Episode = "restart-interrupted"
	source := expectationFor(ResponseRoleFinalReviewer, ResponseReviewPassed)
	call.Expectation = explorerExpectationFrom(t, source, "restart-interrupted-explorer-call")
	call.Policy = AgentCallPolicy{Role: AgentRoleExplorer, CallID: call.Expectation.Binding.CallID}
	continuation := sourceContinuationCall(t, call, source)
	if _, _, err := call.StateStore.RecordRunAttemptStartWithLimits(context.Background(), call.Run, call.OperationID, call.Limits); err != nil {
		t.Fatal(err)
	}
	if _, err := call.StateStore.Record(context.Background(), call.Run); err != nil {
		t.Fatal(err)
	}
	if err := call.StateStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runstore.OpenState(call.Journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, _, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	call.Run, call.StateStore = recovered, reopened
	continuation.Run, continuation.StateStore = recovered, reopened

	explorerRuntime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: explorationResponse(t, "rerun after interruption")}}}
	factory := &explorerRoutingFactory{runtime: explorerRuntime}
	owner := newSessionOwnerForTest(t, factory)
	t.Cleanup(func() { _ = owner.Close() })
	sourceRuntime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseReviewPassed)}}}
	result, err := RouteExplorer(context.Background(), ExplorerRoute{
		Owner: owner, SourceSession: &AgentSession{Role: ResponseRoleFinalReviewer, runtime: sourceRuntime, thread: "fresh-source"},
		SourceExpectation: source, Request: explorationRequest(t), ExplorerCall: call, ExplorerResultID: "interrupted-explorer-result", SourceContinuation: continuation,
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := recovered.RunOperations[0]
	if factory.created != 1 || result.Attempts != 2 || len(operation.Attempts) != 2 || operation.Attempts[0].SemanticRound != operation.Attempts[1].SemanticRound || recovered.RunExplorerCounters["restart-interrupted"] != 1 {
		t.Fatalf("interrupted Explorer did not restart under its original counter: result=%#v operation=%#v counters=%#v created=%d", result, operation, recovered.RunExplorerCounters, factory.created)
	}
}

type explorerRoutingFactory struct {
	runtime *explorerRoutingRuntime
	created int
}

func (f *explorerRoutingFactory) Preflight(setting.RuntimeProfile) error { return nil }
func (f *explorerRoutingFactory) Create(context.Context, setting.RuntimeProfile) (agentruntime.Runtime, error) {
	f.created++
	return f.runtime, nil
}

type explorerRoutingRuntime struct {
	mu       sync.Mutex
	turns    []controlledTurn
	messages []string
}

func (r *explorerRoutingRuntime) StartThread(agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	return "explorer", nil
}
func (r *explorerRoutingRuntime) RunTurn(_ agentruntime.Thread, message string) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.turns) == 0 {
		return nil, errors.New("unexpected Explorer turn")
	}
	turn := r.turns[0]
	r.turns = r.turns[1:]
	r.messages = append(r.messages, message)
	if turn.before != nil {
		turn.before()
	}
	return turn.raw, turn.err
}
func (r *explorerRoutingRuntime) Interrupt() error                      { return nil }
func (r *explorerRoutingRuntime) CloseThread(agentruntime.Thread) error { return nil }
func (r *explorerRoutingRuntime) Close() error                          { return nil }

func TestRouteExplorerRefusesExplorerDelegation(t *testing.T) {
	call := controlledCallFixture(t, &controlledCallRuntime{})
	call.Run.RunOperations[0].Counter = implstate.CycleCounterExplorer
	call.Run.RunOperations[0].Episode = "explorer"
	owner := newSessionOwnerForTest(t, &terminatedSessionFactory{})
	t.Cleanup(func() { _ = owner.Close() })
	explorerSource := explorerExpectationFrom(t, expectationFor(ResponseRoleImplementer, ResponseImplementationReady), "source-explorer")
	_, err := RouteExplorer(context.Background(), ExplorerRoute{
		Owner: owner, SourceSession: &AgentSession{Role: ResponseRoleExplorer}, SourceExpectation: explorerSource,
		Request: explorationRequest(t), ExplorerCall: call,
	})
	if !errors.Is(err, ErrInvalidExplorerRoute) {
		t.Fatalf("Explorer delegation error = %v", err)
	}
}

func explorationRequest(t *testing.T) AgentResponse {
	t.Helper()
	response, err := BindAgentResponse(expectationFor(ResponseRoleFinalReviewer, ResponseExplorationRequested), responsePayload(t, ResponseExplorationRequested))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func explorationResponse(t *testing.T, message string) json.RawMessage {
	t.Helper()
	payload := responsePayloadMap(ResponseExplorationResult)
	payload["message"] = message
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func explorationResponseWithFact(t *testing.T, message, fact string) json.RawMessage {
	t.Helper()
	payload := responsePayloadMap(ResponseExplorationResult)
	payload["message"], payload["known_facts"] = message, []string{fact}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sourceContinuationCall(t *testing.T, explorer ControlledAgentCall, source ResponseExpectation) ControlledAgentCall {
	t.Helper()
	basis := explorer.Run.RunOperations[0].Basis
	if err := explorer.Run.AddRunOperation(implstate.Operation{ID: "source-continuation", Kind: implstate.OperationReview, Counter: implstate.CycleCounterNone, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	expectation := source
	expectation.Binding.CallID = "source-continuation-call"
	return ControlledAgentCall{
		Repository: explorer.Repository, Workspace: explorer.Workspace, Policy: AgentCallPolicy{Role: AgentRoleExplorer, CallID: expectation.Binding.CallID},
		Run: explorer.Run, Journal: explorer.Journal, StateStore: explorer.StateStore,
		OperationID: "source-continuation", Limits: explorer.Limits, Expectation: expectation,
	}
}

func explorerExpectationFrom(t *testing.T, source ResponseExpectation, callID string) ResponseExpectation {
	t.Helper()
	expectation := source
	expectation.Role = ResponseRoleExplorer
	expectation.State = ResponseStateExploring
	expectation.ExplorerSource = explorerSourceFor(source)
	expectation.Binding.CallID = callID
	expectation.Scope = scopeForExpectation(expectation)
	if err := validateExpectation(expectation); err != nil {
		t.Fatal(err)
	}
	return expectation
}
