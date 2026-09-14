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
	first := explorationResponse(t, strings.Repeat(string(rune(0x044F)), DefaultExplorerResponseCharacters+1))
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

	result, err := RouteExplorer(context.Background(), ExplorerRoute{
		Owner: owner, SourceSession: &AgentSession{Role: ResponseRoleFinalReviewer},
		SourceExpectation: expectationFor(ResponseRoleFinalReviewer, ResponseReviewPassed),
		Request:           explorationRequest(t), ExplorerCall: call,
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
	if !strings.Contains(runtime.messages[1], "exceeds 12000 Unicode characters") {
		t.Fatalf("shortening request = %q", runtime.messages[1])
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
