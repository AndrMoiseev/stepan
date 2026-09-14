package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestRouteExplorerReturnsResultToSourceAndPreservesEpisodeCounter(t *testing.T) {
	first := explorationResponseWithFact(t, "short message", strings.Repeat(string(rune(0x044F)), 250))
	second := explorationResponse(t, "validation is centralized")
	runtime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: first}, {raw: second}}}
	factory := &explorerRoutingFactory{runtime: runtime}
	owner := newSessionOwnerForTest(t, factory)
	t.Cleanup(func() { _ = owner.Close() })

	call := controlledCallFixture(t, &controlledCallRuntime{})
	call.Run.RunOperations[0].Counter = implementationstate.CycleCounterExplorer
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
	if len(attempts) != 2 || attempts[0].Outcome != implementationstate.AttemptRejected || attempts[1].Outcome != implementationstate.AttemptSucceeded {
		t.Fatalf("durable technical attempts = %#v", attempts)
	}
}

func TestRouteExplorerContinuesValidEscalationsWithoutSizeRetry(t *testing.T) {
	for _, kind := range []ResponseKind{ResponseClarificationNeeded, ResponseExecutionBlocked} {
		t.Run(string(kind), func(t *testing.T) {
			explorerRuntime := &explorerRoutingRuntime{turns: []controlledTurn{{raw: responsePayload(t, kind)}}}
			owner := newSessionOwnerForTest(t, &explorerRoutingFactory{runtime: explorerRuntime})
			t.Cleanup(func() { _ = owner.Close() })
			call := controlledCallFixture(t, &controlledCallRuntime{})
			call.Run.RunOperations[0].Counter = implementationstate.CycleCounterExplorer
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

type explorerRoutingFactory struct {
	runtime *explorerRoutingRuntime
	created int
}

func (f *explorerRoutingFactory) Preflight(implementationconfig.RuntimeProfile) error { return nil }
func (f *explorerRoutingFactory) Create(context.Context, implementationconfig.RuntimeProfile) (agentruntime.Runtime, error) {
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
	call.Run.RunOperations[0].Counter = implementationstate.CycleCounterExplorer
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
	if err := explorer.Run.AddRunOperation(implementationstate.Operation{ID: "source-continuation", Kind: implementationstate.OperationReview, Counter: implementationstate.CycleCounterNone, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	expectation := source
	expectation.Binding.CallID = "source-continuation-call"
	return ControlledAgentCall{
		Repository: explorer.Repository, Policy: AgentCallPolicy{Role: AgentRoleExplorer, CallID: expectation.Binding.CallID},
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
