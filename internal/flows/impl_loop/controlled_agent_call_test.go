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
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/git"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

func TestInvokeControlledAgentCallUsesWorkspaceControlSeam(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: controlledResponse(t, "accepted without a Git process")}}}
	call := controlledCallFixture(t, runtime)
	call.Repository = t.TempDir() // Deliberately not a Git repository.
	workspace := &unchangedWorkspaceControl{}
	call.Workspace = workspace

	result, err := InvokeControlledAgentCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Message == nil || *result.Response.Message != "accepted without a Git process" {
		t.Fatalf("response = %#v", result.Response)
	}
	if workspace.captures != 2 || workspace.diffs != 1 {
		t.Fatalf("workspace observations = captures:%d diffs:%d", workspace.captures, workspace.diffs)
	}
}

func TestInvokeControlledAgentCallPublishesReceiptBeforeSucceededAndRecoversIdempotently(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: controlledResponse(t, "exact accepted commit message")}}}
	call := controlledCallFixture(t, runtime)
	crash := errors.New("injected crash after receipt publication")
	call.AfterSuccessReceipt = func() error { return crash }

	first, err := InvokeControlledAgentCall(context.Background(), call)
	if !errors.Is(err, crash) || first.Response.Message == nil || *first.Response.Message != "exact accepted commit message" {
		t.Fatalf("crash-boundary result=%#v err=%v", first, err)
	}
	operation := finalRunOperation(call.Run, call.OperationID)
	if operation == nil || len(operation.Attempts) != 1 || operation.Attempts[0].Outcome != "" {
		t.Fatalf("attempt was marked succeeded before the receipt boundary: %#v", operation)
	}
	receiptResponse, receiptSnapshot, _, _, found, err := readControlledAgentSuccessReceipt(call.Journal, call.OperationID)
	if err != nil || !found || receiptResponse.Message == nil || *receiptResponse.Message != "exact accepted commit message" || receiptSnapshot.TreeOID != "unchanged" {
		t.Fatalf("durable accepted-turn receipt = response %#v snapshot %#v found=%t err=%v", receiptResponse, receiptSnapshot, found, err)
	}

	call.AfterSuccessReceipt = nil
	second, err := InvokeControlledAgentCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	operation = finalRunOperation(call.Run, call.OperationID)
	if second.Response.Message == nil || *second.Response.Message != "exact accepted commit message" || len(runtime.messages) != 1 || operation == nil || operation.Attempts[0].Outcome != implstate.AttemptSucceeded {
		t.Fatalf("receipt recovery repeated or changed the completed turn: result=%#v messages=%#v operation=%#v", second, runtime.messages, operation)
	}
}

type unchangedWorkspaceControl struct {
	captures        int
	diffs           int
	assignmentDiffs int
}

type scriptedWorkspaceControl struct {
	unchangedWorkspaceControl
	differences []git.Difference
}

func (w *scriptedWorkspaceControl) Diff(context.Context, string, git.Snapshot, git.Snapshot) (git.Difference, error) {
	w.diffs++
	if len(w.differences) == 0 {
		return git.Difference{}, nil
	}
	difference := w.differences[0]
	w.differences = w.differences[1:]
	return difference, nil
}

func (w *unchangedWorkspaceControl) Capture(context.Context, string) (git.Snapshot, error) {
	w.captures++
	return git.Snapshot{HeadOID: "unchanged", HeadRef: "refs/heads/feature", TreeOID: "unchanged", IndexHash: "unchanged", StatusHash: "unchanged", SubmodulesHash: "unchanged"}, nil
}

func (w *unchangedWorkspaceControl) Diff(context.Context, string, git.Snapshot, git.Snapshot) (git.Difference, error) {
	w.diffs++
	return git.Difference{}, nil
}

func (*unchangedWorkspaceControl) RestorePaths(context.Context, string, git.Snapshot, git.Snapshot, []string) (git.Snapshot, error) {
	return git.Snapshot{HeadOID: "unchanged", HeadRef: "refs/heads/feature", TreeOID: "unchanged", IndexHash: "unchanged", StatusHash: "unchanged", SubmodulesHash: "unchanged"}, nil
}

func (*unchangedWorkspaceControl) EnsureUnchanged(context.Context, string, git.Snapshot) error {
	return nil
}

func (w *unchangedWorkspaceControl) AssignmentDiff(context.Context, string, string) (string, error) {
	w.assignmentDiffs++
	return "No assignment changes.", nil
}

func TestInvokeControlledAgentCallRetriesCrashAndMalformedResponse(t *testing.T) {
	if DefaultAgentCallTimeout != 1800*time.Second {
		t.Fatalf("default timeout = %s, want 30m", DefaultAgentCallTimeout)
	}
	for _, test := range []struct {
		name         string
		outputs      []controlledTurn
		firstOutcome implstate.AttemptOutcome
	}{
		{"crash", []controlledTurn{{err: agentruntime.ErrRuntimeExited}, {raw: controlledResponse(t, "accepted after crash")}}, implstate.AttemptFailed},
		{"malformed response", []controlledTurn{{raw: json.RawMessage(`{}`)}, {raw: controlledResponse(t, "accepted after repair")}}, implstate.AttemptRejected},
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
			recovered, _, err := call.StateStore.Current(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			attempts := recovered.RunOperations[0].Attempts
			if len(attempts) != 2 || attempts[0].Outcome != test.firstOutcome || attempts[0].Diagnostic == "" || attempts[1].Outcome != implstate.AttemptSucceeded {
				t.Fatalf("persisted technical outcomes = %#v", attempts)
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

func TestInvokeControlledAgentCallPersistsTechnicalFailureAndLimitPause(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{err: agentruntime.ErrRuntimeExited}}}
	call := controlledCallFixture(t, runtime)
	call.Limits.TechnicalAttempts = 1

	_, err := InvokeControlledAgentCall(context.Background(), call)
	if !errors.Is(err, implstate.ErrLimitExceeded) {
		t.Fatalf("call error = %v, want technical limit", err)
	}
	if err := call.StateStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runstore.OpenState(call.Journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, sequence, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sequence < 3 || recovered.Status != implstate.RunPaused || recovered.LimitPause == nil || !recovered.LimitPause.Technical {
		t.Fatalf("recovered durable boundary = sequence %d, state %#v", sequence, recovered)
	}
	attempts := recovered.RunOperations[0].Attempts
	if len(attempts) != 1 || attempts[0].Outcome != implstate.AttemptFailed || !strings.Contains(attempts[0].Diagnostic, agentruntime.ErrRuntimeExited.Error()) {
		t.Fatalf("recovered technical outcome = %#v", attempts)
	}
}

func TestInvokeControlledAgentCallRecreatesTerminatedSessionForRetry(t *testing.T) {
	factory := &terminatedSessionFactory{turns: []controlledTurn{
		{waitForInterrupt: true},
		{raw: controlledResponse(t, "accepted from a fresh session")},
	}}
	owner := newSessionOwnerForTest(t, factory)
	t.Cleanup(func() { _ = owner.Close() })
	start := sessionStartContext(t, ResponseRoleImplementer)
	session, err := owner.Assignment(context.Background(), "assignment", ResponseRoleImplementer, start)
	if err != nil {
		t.Fatal(err)
	}
	call := controlledCallFixture(t, &controlledCallRuntime{})
	call.Session = session
	call.Timeout = 10 * time.Millisecond

	result, err := InvokeControlledAgentCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 || result.Response.Message == nil || *result.Response.Message != "accepted from a fresh session" {
		t.Fatalf("retry result = %#v", result)
	}
	if factory.created() != 2 || !factory.runtime(0).isClosed() {
		t.Fatalf("runtime lifecycle = created %d, first closed %t", factory.created(), factory.runtime(0).isClosed())
	}
	if message := factory.runtime(1).lastMessage(); !strings.Contains(message, call.Message) || !strings.Contains(message, "Original requested action") {
		t.Fatalf("fresh session retry did not receive original action: %q", message)
	}
	if current, err := owner.Assignment(context.Background(), "assignment", ResponseRoleImplementer, start); err != nil || current == session {
		t.Fatalf("owner retained terminated session: session=%p current=%p err=%v", session, current, err)
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
	stateStore, err := runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stateStore.Close() })
	repository := t.TempDir()
	baseline := controlledCallReference(t, journal, "baseline")
	specification := controlledCallReference(t, journal, "specification")
	taskList := controlledCallReference(t, journal, "tasks")
	configuration := controlledCallReference(t, journal, "configuration")
	model, err := implstate.NewRun(implstate.RunIdentity{
		ID:             journal.ID(),
		Change:         "change",
		Repository:     repository,
		WorkCopy:       repository,
		Branch:         "feature",
		BaselineCommit: "base",
		BaselineState:  baseline,
		Specification:  specification,
		TaskList:       taskList,
		Configuration:  configuration,
	}, []implstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	basis := implstate.AcceptanceBasis{Specification: specification, Configuration: configuration}
	if err := model.AddRunOperation(implstate.Operation{ID: "agent-operation", Kind: implstate.OperationAgent, Counter: implstate.CycleCounterNone, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	expectation := expectationFor(ResponseRoleImplementer, ResponseImplementationReady)
	return ControlledAgentCall{
		Session: &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "thread", restart: func(context.Context) (*AgentSession, error) {
			return &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "thread"}, nil
		}},
		Repository: repository,
		Workspace:  &unchangedWorkspaceControl{},
		Policy:     AgentCallPolicy{Role: AgentRoleExecutor, CallID: expectation.Binding.CallID, AllowUnprotected: true},
		Run:        model,
		Journal:    journal, StateStore: stateStore, OperationID: "agent-operation", Limits: controlledCallLimits(), Expectation: expectation, Message: "perform the requested action",
	}
}

func controlledCallReference(t *testing.T, run *runstore.Run, id implstate.EvidenceID) implstate.EvidenceRef {
	t.Helper()
	reference, err := run.Publish(id, []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func controlledCallLimits() implstate.CycleLimits {
	return implstate.CycleLimits{AssignmentReview: 3, MandatoryChecks: 3, ChecksRequested: 5, BriefRefinement: 3, Explorer: 10, TechnicalAttempts: 3, FinalReview: 3}
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
	threads    []agentruntime.Thread
	configs    []agentruntime.ThreadConfig
	interrupts int
	started    chan struct{}
	interrupt  chan struct{}
}

func (runtime *controlledCallRuntime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	runtime.mu.Lock()
	runtime.configs = append(runtime.configs, config)
	runtime.mu.Unlock()
	return "thread", nil
}

func (runtime *controlledCallRuntime) RunTurn(thread agentruntime.Thread, message string) (json.RawMessage, error) {
	runtime.mu.Lock()
	index := len(runtime.messages)
	if index >= len(runtime.turns) {
		runtime.mu.Unlock()
		return nil, errors.New("unexpected extra agent turn")
	}
	turn := runtime.turns[index]
	runtime.messages = append(runtime.messages, message)
	runtime.threads = append(runtime.threads, thread)
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

// terminatedSessionFactory models adapter runtimes that cannot accept another
// turn after Interrupt or a runtime failure. A retry must therefore open a
// distinct runtime and thread through SessionOwner.
type terminatedSessionFactory struct {
	mu       sync.Mutex
	turns    []controlledTurn
	runtimes []*terminatedSessionRuntime
}

func (factory *terminatedSessionFactory) Preflight(setting.RuntimeProfile) error {
	return nil
}

func (factory *terminatedSessionFactory) Create(context.Context, setting.RuntimeProfile) (agentruntime.Runtime, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.turns) == 0 {
		return nil, errors.New("unexpected replacement runtime")
	}
	runtime := &terminatedSessionRuntime{turn: factory.turns[0]}
	factory.turns = factory.turns[1:]
	factory.runtimes = append(factory.runtimes, runtime)
	return runtime, nil
}

func (factory *terminatedSessionFactory) created() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return len(factory.runtimes)
}

func (factory *terminatedSessionFactory) runtime(index int) *terminatedSessionRuntime {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.runtimes[index]
}

type terminatedSessionRuntime struct {
	mu        sync.Mutex
	turn      controlledTurn
	closed    bool
	interrupt chan struct{}
	messages  []string
}

func (runtime *terminatedSessionRuntime) StartThread(agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	return "fresh-thread", nil
}

func (runtime *terminatedSessionRuntime) RunTurn(_ agentruntime.Thread, message string) (json.RawMessage, error) {
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return nil, agentruntime.ErrRuntimeClosed
	}
	runtime.messages = append(runtime.messages, message)
	turn := runtime.turn
	if turn.waitForInterrupt {
		runtime.interrupt = make(chan struct{})
	}
	interrupt := runtime.interrupt
	runtime.mu.Unlock()
	if turn.waitForInterrupt {
		<-interrupt
		return nil, agentruntime.ErrTurnInterrupted
	}
	return turn.raw, turn.err
}

func (runtime *terminatedSessionRuntime) Interrupt() error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.closed = true
	if runtime.interrupt != nil {
		close(runtime.interrupt)
		runtime.interrupt = nil
	}
	return nil
}

func (runtime *terminatedSessionRuntime) CloseThread(agentruntime.Thread) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return agentruntime.ErrTurnInterrupted
	}
	return nil
}
func (runtime *terminatedSessionRuntime) Close() error { return nil }

func (runtime *terminatedSessionRuntime) isClosed() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.closed
}

func (runtime *terminatedSessionRuntime) lastMessage() string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.messages) == 0 {
		return ""
	}
	return runtime.messages[len(runtime.messages)-1]
}
