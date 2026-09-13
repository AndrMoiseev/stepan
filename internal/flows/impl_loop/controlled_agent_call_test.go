package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestInvokeControlledAgentCallRetriesCrashAndMalformedResponse(t *testing.T) {
	if DefaultAgentCallTimeout != 1800*time.Second {
		t.Fatalf("default timeout = %s, want 30m", DefaultAgentCallTimeout)
	}
	for _, test := range []struct {
		name    string
		outputs []controlledTurn
	}{
		{"crash", []controlledTurn{{err: agentruntime.ErrRuntimeExited}, {raw: controlledResponse(t, "accepted after crash")}}},
		{"malformed response", []controlledTurn{{raw: json.RawMessage(`{}`)}, {raw: controlledResponse(t, "accepted after repair")}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &controlledCallRuntime{turns: test.outputs}
			call := controlledCallFixture(t, runtime)
			result, err := InvokeControlledAgentCall(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			if result.Attempts != 2 || result.Response.Message == nil || !strings.Contains(*result.Response.Message, "accepted") {
				t.Fatalf("result = %#v", result)
			}
			if len(runtime.messages) != 2 || !strings.Contains(runtime.messages[1], "previous response was not accepted") {
				t.Fatalf("retry messages = %#v", runtime.messages)
			}
		})
	}
}

func TestInvokeControlledAgentCallTimeoutRetries(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{waitForInterrupt: true}, {raw: controlledResponse(t, "accepted after timeout")}}}
	call := controlledCallFixture(t, runtime)
	call.Timeout = 10 * time.Millisecond
	result, err := InvokeControlledAgentCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 || runtime.interrupts != 1 {
		t.Fatalf("timeout result=%#v interrupts=%d", result, runtime.interrupts)
	}
}

func TestInvokeControlledAgentCallCancellationDoesNotRetry(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{waitForInterrupt: true}}}
	call := controlledCallFixture(t, runtime)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := InvokeControlledAgentCall(ctx, call)
		done <- err
	}()
	if !runtime.waitForTurn(1, time.Second) {
		t.Fatal("agent turn did not start")
	}
	cancel()
	err := <-done
	if !errors.Is(err, ErrAgentCallCancelled) || len(runtime.messages) != 1 || runtime.interrupts != 1 {
		t.Fatalf("cancel error=%v messages=%#v interrupts=%d", err, runtime.messages, runtime.interrupts)
	}
}

func TestInvokeControlledAgentCallRejectsRestoredViolationBeforeRetry(t *testing.T) {
	repository := newSnapshotRepository(t)
	writeAgentFile(t, repository, ".stepan/settings.json", "protected before\n")
	runtime := &controlledCallRuntime{turns: []controlledTurn{
		{raw: controlledResponse(t, "MUST NOT BE ACCEPTED"), before: func() { writeAgentFile(t, repository, ".stepan/settings.json", "forbidden\n") }},
		{raw: controlledResponse(t, "accepted after restoration")},
	}}
	call := controlledCallFixture(t, runtime)
	call.Repository = repository
	call.Policy = AgentCallPolicy{Role: AgentRoleExecutor, CallID: call.Expectation.Binding.CallID, AllowUnprotected: true, ProtectedPaths: []string{".stepan/settings.json"}}
	result, err := InvokeControlledAgentCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 || result.Response.Message == nil || *result.Response.Message != "accepted after restoration" {
		t.Fatalf("violation result = %#v", result)
	}
	assertAgentFile(t, repository, ".stepan/settings.json", "protected before\n")
	if len(runtime.messages) != 2 || !strings.Contains(runtime.messages[1], "changed prohibited paths") {
		t.Fatalf("violation did not produce a retry diagnostic: %#v", runtime.messages)
	}
	if records := readViolationRecords(t, call.Journal); len(records) != 1 || records[0].RestorationResult != "restored" {
		t.Fatalf("violation records = %#v", records)
	}
}

func controlledCallFixture(t *testing.T, runtime *controlledCallRuntime) ControlledAgentCall {
	t.Helper()
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("controlled-agent-call")
	if err != nil {
		t.Fatal(err)
	}
	expectation := expectationFor(ResponseRoleImplementer, ResponseImplementationReady)
	return ControlledAgentCall{
		Session:    &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "thread"},
		Repository: newSnapshotRepository(t),
		Policy:     AgentCallPolicy{Role: AgentRoleExecutor, CallID: expectation.Binding.CallID, AllowUnprotected: true},
		Run: &implementationstate.Run{Status: implementationstate.RunActive, RunOperations: []implementationstate.Operation{{
			ID: "agent-operation", Kind: implementationstate.OperationAgent, Counter: implementationstate.CycleCounterNone,
			Basis: implementationstate.AcceptanceBasis{Specification: implementationstate.EvidenceRef{ID: "spec", Digest: "v1"}, Configuration: implementationstate.EvidenceRef{ID: "config", Digest: "v1"}},
		}}},
		Journal: journal, OperationID: "agent-operation", Limits: controlledCallLimits(), Expectation: expectation, Message: "perform the requested action",
	}
}

func controlledCallLimits() implementationstate.CycleLimits {
	return implementationstate.CycleLimits{AssignmentReview: 3, MandatoryChecks: 3, ChecksRequested: 5, BriefRefinement: 3, Explorer: 10, TechnicalAttempts: 3, FinalReview: 3}
}

func controlledResponse(t *testing.T, message string) json.RawMessage {
	t.Helper()
	payload := responsePayloadMap(ResponseImplementationReady)
	payload["message"] = message
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type controlledTurn struct {
	raw              json.RawMessage
	err              error
	before           func()
	waitForInterrupt bool
}

type controlledCallRuntime struct {
	mu         sync.Mutex
	turns      []controlledTurn
	messages   []string
	interrupts int
	started    chan struct{}
	interrupt  chan struct{}
}

func (runtime *controlledCallRuntime) StartThread(agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	return "thread", nil
}

func (runtime *controlledCallRuntime) RunTurn(_ agentruntime.Thread, message string) (json.RawMessage, error) {
	runtime.mu.Lock()
	index := len(runtime.messages)
	if index >= len(runtime.turns) {
		runtime.mu.Unlock()
		return nil, errors.New("unexpected extra agent turn")
	}
	turn := runtime.turns[index]
	runtime.messages = append(runtime.messages, message)
	if runtime.started == nil {
		runtime.started = make(chan struct{})
	}
	if index == 0 {
		close(runtime.started)
	}
	if turn.waitForInterrupt {
		runtime.interrupt = make(chan struct{})
	}
	interrupt := runtime.interrupt
	runtime.mu.Unlock()
	if turn.before != nil {
		turn.before()
	}
	if turn.waitForInterrupt {
		<-interrupt
		return nil, agentruntime.ErrTurnInterrupted
	}
	return turn.raw, turn.err
}

func (runtime *controlledCallRuntime) Interrupt() error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.interrupts++
	if runtime.interrupt != nil {
		close(runtime.interrupt)
		runtime.interrupt = nil
	}
	return nil
}

func (runtime *controlledCallRuntime) CloseThread(_ agentruntime.Thread) error { return nil }
func (runtime *controlledCallRuntime) Close() error                            { return nil }

func (runtime *controlledCallRuntime) waitForTurn(want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		count := len(runtime.messages)
		runtime.mu.Unlock()
		if count >= want {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
