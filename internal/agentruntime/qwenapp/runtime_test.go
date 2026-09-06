package qwenapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

type fakeRuntimeThread struct {
	output json.RawMessage
	runErr error
	run    func() (json.RawMessage, error)

	done       chan struct{}
	doneOnce   sync.Once
	closeCount atomic.Int32
	cancelled  atomic.Int32
	closeGate  <-chan struct{}
	closeStart chan struct{}
	startOnce  sync.Once
	closeErr   error
	onClose    func()
	closed     atomic.Bool
	errMu      sync.Mutex
	err        error
}

func newFakeRuntimeThread(output string) *fakeRuntimeThread {
	return &fakeRuntimeThread{output: json.RawMessage(output), done: make(chan struct{})}
}

func (thread *fakeRuntimeThread) RunTurn(string) (json.RawMessage, error) {
	if thread.run != nil {
		return thread.run()
	}
	return append(json.RawMessage(nil), thread.output...), thread.runErr
}

func (thread *fakeRuntimeThread) Cancel() error {
	thread.cancelled.Add(1)
	return nil
}

func (thread *fakeRuntimeThread) Done() <-chan struct{} { return thread.done }
func (thread *fakeRuntimeThread) BeginClose()           {}
func (thread *fakeRuntimeThread) Err() error {
	thread.errMu.Lock()
	defer thread.errMu.Unlock()
	return thread.err
}

func (thread *fakeRuntimeThread) fail(err error) {
	thread.errMu.Lock()
	thread.err = err
	thread.errMu.Unlock()
	thread.doneOnce.Do(func() { close(thread.done) })
}

func (thread *fakeRuntimeThread) Close() error {
	first := thread.closed.CompareAndSwap(false, true)
	if first {
		thread.closeCount.Add(1)
	}
	if thread.closeStart != nil {
		thread.startOnce.Do(func() { close(thread.closeStart) })
	}
	if thread.closeGate != nil {
		<-thread.closeGate
	}
	thread.doneOnce.Do(func() { close(thread.done) })
	if first && thread.onClose != nil {
		thread.onClose()
	}
	return thread.closeErr
}

type fakeRuntimeFactory struct {
	mu        sync.Mutex
	threads   []*fakeRuntimeThread
	errors    []error
	configs   []agentruntime.ThreadConfig
	started   int
	startGate <-chan struct{}
	onStart   func()
}

func (factory *fakeRuntimeFactory) start(ctx context.Context, _ Config, config agentruntime.ThreadConfig) (runtimeThread, error) {
	factory.mu.Lock()
	index := factory.started
	factory.started++
	factory.configs = append(factory.configs, config.Clone())
	var result *fakeRuntimeThread
	if index < len(factory.threads) {
		result = factory.threads[index]
	}
	var resultErr error
	if index < len(factory.errors) && factory.errors[index] != nil {
		resultErr = factory.errors[index]
	}
	factory.mu.Unlock()
	if factory.startGate != nil {
		select {
		case <-factory.startGate:
		case <-ctx.Done():
			if result != nil {
				_ = result.Close()
			}
			return nil, ctx.Err()
		}
	}
	if resultErr != nil {
		return nil, resultErr
	}
	if result == nil {
		return nil, errors.New("unexpected fake thread start")
	}
	if factory.onStart != nil {
		factory.onStart()
	}
	return result, nil
}

func testRuntime(t *testing.T, factory *fakeRuntimeFactory) *Runtime {
	t.Helper()
	workspace := t.TempDir()
	runtime := newRuntime(Config{Executable: "qwen", Workspace: workspace, JSONContract: "one object"}, runtimeDependencies{
		startThread: factory.start,
		after:       time.After,
	})
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func testThreadConfig(runtime *Runtime) agentruntime.ThreadConfig {
	return agentruntime.ThreadConfig{Workspace: runtime.config.Workspace, OutputSchema: json.RawMessage(`{"type":"object"}`)}
}

func TestStartRuntimeRequiresAndClonesEnvelopeSchema(t *testing.T) {
	for _, schema := range []json.RawMessage{nil, json.RawMessage(`[]`), json.RawMessage(`{"type":"object"} trailing`)} {
		runtime, err := StartRuntime(Config{EnvelopeSchema: schema})
		if runtime != nil || !errors.Is(err, agentruntime.ErrRuntimeConfiguration) || !strings.Contains(err.Error(), "qwen configure runtime") {
			t.Fatalf("StartRuntime(%s) = %#v, %v", schema, runtime, err)
		}
	}

	schema := json.RawMessage(`{"type":"object"}`)
	runtime, err := StartRuntime(Config{EnvelopeSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	schema[0] = '['
	if got := string(runtime.config.EnvelopeSchema); got != `{"type":"object"}` {
		t.Fatalf("runtime retained mutable envelope schema: %s", got)
	}
}

func TestRuntimeOwnsIndependentContainedThreads(t *testing.T) {
	first := newFakeRuntimeThread(`{"thread":1}`)
	second := newFakeRuntimeThread(`{"thread":2}`)
	factory := &fakeRuntimeFactory{threads: []*fakeRuntimeThread{first, second}, errors: []error{nil, nil, ErrContainment}}
	runtime := testRuntime(t, factory)

	firstHandle, err := runtime.StartThread(testThreadConfig(runtime))
	if err != nil {
		t.Fatal(err)
	}
	secondHandle, err := runtime.StartThread(testThreadConfig(runtime))
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.threads) != 2 || firstHandle == secondHandle {
		t.Fatalf("published threads = %d, handles %#v %#v", len(runtime.threads), firstHandle, secondHandle)
	}
	if err := runtime.CloseThread(firstHandle); err != nil {
		t.Fatal(err)
	}
	if first.closeCount.Load() != 1 || second.closeCount.Load() != 0 || len(runtime.threads) != 1 {
		t.Fatalf("independent close: first=%d second=%d live=%d", first.closeCount.Load(), second.closeCount.Load(), len(runtime.threads))
	}
	output, err := runtime.RunTurn(secondHandle, "continue")
	if err != nil || string(output) != `{"thread":2}` {
		t.Fatalf("sibling turn = %s, %v", output, err)
	}
	if handle, err := runtime.StartThread(testThreadConfig(runtime)); handle != nil || !errors.Is(err, agentruntime.ErrThreadFailed) || !errors.Is(err, agentruntime.ErrRuntimeContainment) {
		t.Fatalf("contained startup failure = %#v, %v", handle, err)
	}
	if second.closeCount.Load() != 0 || len(runtime.threads) != 1 {
		t.Fatal("failed supervisor affected an existing thread")
	}
}

func TestRuntimeProductionFactoryUsesOneProcessAndSessionPerThread(t *testing.T) {
	workspace := makeGitRoot(t)
	t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
	t.Setenv("STEPAN_QWEN_ACP_CASE", "structured-turn")
	t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", filepath.Join(t.TempDir(), "observation.json"))
	runtime, err := StartRuntime(Config{
		Executable:     testExecutableName(t),
		Workspace:      workspace,
		JSONContract:   testJSONContract,
		EnvelopeSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)
	first, err := runtime.StartThread(agentruntime.ThreadConfig{Workspace: workspace, OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.StartThread(agentruntime.ThreadConfig{Workspace: workspace, ArtifactRoot: t.TempDir(), OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	firstBackend := first.(*threadHandle).state.backend.(*qwenThread)
	secondBackend := second.(*threadHandle).state.backend.(*qwenThread)
	firstRoot := firstBackend.process.TransportRoot()
	if firstBackend.process.command.Process.Pid == secondBackend.process.command.Process.Pid ||
		firstBackend.connection == secondBackend.connection || firstBackend.process.TransportRoot() == secondBackend.process.TransportRoot() {
		t.Fatal("logical threads shared a process, connection, or artifact root")
	}
	if err := runtime.CloseThread(first); err != nil {
		t.Fatal(err)
	}
	if firstBackend.process.command.ProcessState == nil || secondBackend.process.command.ProcessState != nil {
		t.Fatalf("process states after independent close: first=%v second=%v", firstBackend.process.command.ProcessState, secondBackend.process.command.ProcessState)
	}
	if _, err := os.Stat(firstRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime-owned read-only root remains: %v", err)
	}
	output, err := runtime.RunTurn(second, "return an answer")
	if err != nil || string(output) != `{"answer":"repaired"}` {
		t.Fatalf("remaining process turn = %s, %v", output, err)
	}
}

func TestRuntimeProductionStartupReportsSafeExitDiagnostic(t *testing.T) {
	workspace := makeGitRoot(t)
	executable := testExecutableName(t)
	const secret = "credential-body-do-not-echo"
	t.Setenv("GO_WANT_QWENAPP_FAKE", "startup-diagnostic")
	t.Setenv("STEPAN_QWENAPP_DIAGNOSTIC_SECRET", secret)
	runtime, err := StartRuntime(Config{
		Executable:     executable,
		Workspace:      workspace,
		JSONContract:   testJSONContract,
		EnvelopeSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	_, err = runtime.StartThread(agentruntime.ThreadConfig{
		Workspace:    workspace,
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if !errors.Is(err, agentruntime.ErrThreadFailed) || !errors.Is(err, agentruntime.ErrRuntimeExited) {
		t.Fatalf("startup error = %v", err)
	}
	for _, want := range []string{`executable "` + executable + `"`, "exit code 23", "during initialize lifecycle", "authentication"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("startup diagnostic %q does not contain %q", err, want)
		}
	}
	for _, forbidden := range []string{secret, workspace, testJSONContract, "Authentication required while loading"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("startup diagnostic leaked %q: %v", forbidden, err)
		}
	}
}

func TestRuntimeProductionStartupReportsSafeRPCDiagnostic(t *testing.T) {
	workspace := makeGitRoot(t)
	t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
	t.Setenv("STEPAN_QWEN_ACP_CASE", "session-auth-error")
	t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", filepath.Join(t.TempDir(), "observation.json"))
	runtime, err := StartRuntime(Config{
		Executable:     testExecutableName(t),
		Workspace:      workspace,
		JSONContract:   testJSONContract,
		EnvelopeSchema: json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	_, err = runtime.StartThread(agentruntime.ThreadConfig{
		Workspace:    workspace,
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if !errors.Is(err, agentruntime.ErrThreadFailed) || !errors.Is(err, agentruntime.ErrRuntimeIncompatible) {
		t.Fatalf("startup error = %v", err)
	}
	for _, want := range []string{"session/new lifecycle", "ACP error code -32000", "provider error class authentication"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("startup diagnostic %q does not contain %q", err, want)
		}
	}
	for _, forbidden := range []string{"credential-body-do-not-echo", workspace, "Authentication required"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("startup diagnostic leaked %q: %v", forbidden, err)
		}
	}
}

func TestRuntimeDoesNotPublishThreadBeforePreflightOrAfterClose(t *testing.T) {
	gate := make(chan struct{})
	created := newFakeRuntimeThread(`{}`)
	factory := &fakeRuntimeFactory{threads: []*fakeRuntimeThread{created}, startGate: gate}
	runtime := testRuntime(t, factory)
	result := make(chan error, 1)
	go func() {
		_, err := runtime.StartThread(testThreadConfig(runtime))
		result <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		factory.mu.Lock()
		started := factory.started
		factory.mu.Unlock()
		if started != 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(runtime.threads) != 0 {
		t.Fatal("thread was published while preflight was blocked")
	}
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	for {
		runtime.mu.Lock()
		closing := runtime.closing
		runtime.mu.Unlock()
		if closing {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(gate)
	if err := <-result; !errors.Is(err, agentruntime.ErrRuntimeClosed) {
		t.Fatalf("start racing close = %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if created.closeCount.Load() != 1 || len(runtime.threads) != 0 {
		t.Fatalf("unpublished process cleanup = %d, live=%d", created.closeCount.Load(), len(runtime.threads))
	}
}

func TestRuntimeCloseAndInterruptCancelHungStartup(t *testing.T) {
	for _, test := range []struct {
		name      string
		interrupt bool
		want      error
	}{
		{name: "close", want: agentruntime.ErrRuntimeClosed},
		{name: "interrupt", interrupt: true, want: agentruntime.ErrTurnInterrupted},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate := make(chan struct{})
			backend := newFakeRuntimeThread(`{}`)
			factory := &fakeRuntimeFactory{threads: []*fakeRuntimeThread{backend}, startGate: gate}
			runtime := testRuntime(t, factory)
			started := make(chan error, 1)
			go func() {
				_, err := runtime.StartThread(testThreadConfig(runtime))
				started <- err
			}()
			waitForRuntimeFactoryStart(t, factory)

			closed := make(chan error, 1)
			if test.interrupt {
				go func() { closed <- runtime.Interrupt() }()
			} else {
				go func() { closed <- runtime.Close() }()
			}
			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("lifecycle hung behind synchronous startup")
			}
			select {
			case err := <-started:
				if !errors.Is(err, test.want) {
					t.Fatalf("cancelled startup = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled startup goroutine leaked")
			}
			runtime.mu.Lock()
			starting := len(runtime.starting)
			runtime.mu.Unlock()
			if starting != 0 || backend.closeCount.Load() != 1 {
				t.Fatalf("startup owners after checkpoint = %d, closes=%d", starting, backend.closeCount.Load())
			}
		})
	}
}

func TestRuntimeProductionStartupCancellationClosesHungACPProcess(t *testing.T) {
	for _, test := range []struct {
		name     string
		scenario string
	}{
		{name: "initialize", scenario: "hang-initialize"},
		{name: "session new", scenario: "hang-session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := makeGitRoot(t)
			observation := filepath.Join(t.TempDir(), "started.json")
			t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
			t.Setenv("STEPAN_QWEN_ACP_CASE", test.scenario)
			t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", observation)
			runtime, err := StartRuntime(Config{
				Executable: testExecutableName(t), Workspace: workspace, JSONContract: testJSONContract,
				EnvelopeSchema: json.RawMessage(`{"type":"object"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan error, 1)
			go func() {
				_, err := runtime.StartThread(agentruntime.ThreadConfig{Workspace: workspace, OutputSchema: json.RawMessage(`{"type":"object"}`)})
				started <- err
			}()
			waitForFile(t, observation)
			closed := make(chan error, 1)
			go func() { closed <- runtime.Close() }()
			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("runtime close did not tear down hung ACP startup")
			}
			select {
			case err := <-started:
				if !errors.Is(err, agentruntime.ErrRuntimeClosed) {
					t.Fatalf("hung production startup = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("production startup goroutine leaked")
			}
			runtime.mu.Lock()
			starting := len(runtime.starting)
			runtime.mu.Unlock()
			if starting != 0 {
				t.Fatalf("runtime retained %d startup owners", starting)
			}
		})
	}
}

func TestRuntimeUsesOneGlobalDeadlineForUncooperativeStartups(t *testing.T) {
	const count = 4
	workspace := t.TempDir()
	gate := make(chan struct{})
	var entered atomic.Int32
	backends := make([]*fakeRuntimeThread, count)
	for index := range backends {
		backends[index] = newFakeRuntimeThread(`{}`)
	}
	start := func(_ context.Context, _ Config, _ agentruntime.ThreadConfig) (runtimeThread, error) {
		index := int(entered.Add(1)) - 1
		<-gate // deliberately ignores cancellation to exercise the global bound
		return backends[index], nil
	}
	var deadlines atomic.Int32
	runtime := newRuntime(Config{Workspace: workspace}, runtimeDependencies{
		startThread: start,
		after: func(time.Duration) <-chan time.Time {
			deadlines.Add(1)
			ready := make(chan time.Time, 1)
			ready <- time.Now()
			return ready
		},
	})
	results := make(chan error, count)
	for range count {
		go func() {
			_, err := runtime.StartThread(testThreadConfig(runtime))
			results <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for entered.Load() != count {
		if time.Now().After(deadline) {
			t.Fatal("concurrent startups did not enter")
		}
		time.Sleep(time.Millisecond)
	}
	if err := runtime.Close(); !errors.Is(err, agentruntime.ErrRuntimeCleanup) {
		t.Fatalf("bounded close error = %v", err)
	}
	if deadlines.Load() != 1 {
		t.Fatalf("startup grace timers = %d, want one global budget", deadlines.Load())
	}
	close(gate)
	for range count {
		select {
		case err := <-results:
			if !errors.Is(err, agentruntime.ErrRuntimeClosed) {
				t.Fatalf("late startup = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("late startup goroutine leaked")
		}
	}
	for index, backend := range backends {
		if backend.closeCount.Load() != 1 {
			t.Fatalf("late backend %d closes = %d", index, backend.closeCount.Load())
		}
	}
}

func waitForRuntimeFactoryStart(t *testing.T, factory *fakeRuntimeFactory) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		factory.mu.Lock()
		started := factory.started > 0
		factory.mu.Unlock()
		if started {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("startup did not begin")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRuntimeCloseWaitsForConcurrentThreadTeardown(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	thread := newFakeRuntimeThread(`{}`)
	thread.closeGate = gate
	thread.closeStart = started
	runtime := testRuntime(t, &fakeRuntimeFactory{threads: []*fakeRuntimeThread{thread}})
	handle, _ := runtime.StartThread(testThreadConfig(runtime))
	threadClosed := make(chan error, 1)
	go func() { threadClosed <- runtime.CloseThread(handle) }()
	<-started
	runtimeClosed := make(chan error, 1)
	go func() { runtimeClosed <- runtime.Close() }()
	select {
	case err := <-runtimeClosed:
		t.Fatalf("runtime close returned before thread teardown: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(gate)
	if err := <-threadClosed; err != nil {
		t.Fatal(err)
	}
	if err := <-runtimeClosed; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSerializesTurnsAcrossThreads(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	first := newFakeRuntimeThread(`{}`)
	first.run = func() (json.RawMessage, error) {
		close(entered)
		<-release
		return json.RawMessage(`{"done":true}`), nil
	}
	second := newFakeRuntimeThread(`{"second":true}`)
	runtime := testRuntime(t, &fakeRuntimeFactory{threads: []*fakeRuntimeThread{first, second}})
	firstHandle, _ := runtime.StartThread(testThreadConfig(runtime))
	secondHandle, _ := runtime.StartThread(testThreadConfig(runtime))
	finished := make(chan error, 1)
	go func() {
		_, err := runtime.RunTurn(firstHandle, "first")
		finished <- err
	}()
	<-entered
	if _, err := runtime.RunTurn(secondHandle, "second"); !errors.Is(err, agentruntime.ErrTurnInProgress) {
		t.Fatalf("concurrent sibling turn = %v", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeLocalizesProtocolFailureAndPreservesSibling(t *testing.T) {
	const secret = "credential-body-do-not-echo"
	first := newFakeRuntimeThread(`{}`)
	first.runErr = fmt.Errorf("%w: foreign session %s", ErrProtocol, secret)
	second := newFakeRuntimeThread(`{"valid":true}`)
	runtime := testRuntime(t, &fakeRuntimeFactory{threads: []*fakeRuntimeThread{first, second}})
	firstHandle, _ := runtime.StartThread(testThreadConfig(runtime))
	secondHandle, _ := runtime.StartThread(testThreadConfig(runtime))

	if _, err := runtime.RunTurn(firstHandle, "fail"); !errors.Is(err, agentruntime.ErrThreadFailed) || !errors.Is(err, agentruntime.ErrRuntimeProtocol) || strings.Contains(err.Error(), secret) {
		t.Fatalf("localized protocol error = %v", err)
	}
	if first.closeCount.Load() != 1 || second.closeCount.Load() != 0 || len(runtime.threads) != 1 {
		t.Fatalf("localized close = first %d second %d live %d", first.closeCount.Load(), second.closeCount.Load(), len(runtime.threads))
	}
	if _, err := runtime.RunTurn(firstHandle, "stale"); !errors.Is(err, agentruntime.ErrThreadFailed) {
		t.Fatalf("failed handle = %v", err)
	}
	if output, err := runtime.RunTurn(secondHandle, "healthy"); err != nil || string(output) != `{"valid":true}` {
		t.Fatalf("sibling output = %s, %v", output, err)
	}
}

func TestRuntimeLocalizesThreadCleanupFailureAndPreservesSibling(t *testing.T) {
	const secret = "cleanup-secret-do-not-echo"
	first := newFakeRuntimeThread(`{}`)
	first.closeErr = fmt.Errorf("remove root %s: access denied", secret)
	second := newFakeRuntimeThread(`{"valid":true}`)
	runtime := testRuntime(t, &fakeRuntimeFactory{threads: []*fakeRuntimeThread{first, second}})
	firstHandle, _ := runtime.StartThread(testThreadConfig(runtime))
	secondHandle, _ := runtime.StartThread(testThreadConfig(runtime))
	if err := runtime.CloseThread(firstHandle); !errors.Is(err, agentruntime.ErrThreadFailed) || !errors.Is(err, agentruntime.ErrRuntimeCleanup) || strings.Contains(err.Error(), secret) {
		t.Fatalf("localized cleanup error = %v", err)
	}
	if output, err := runtime.RunTurn(secondHandle, "continue"); err != nil || string(output) != `{"valid":true}` {
		t.Fatalf("sibling after cleanup error = %s, %v", output, err)
	}
}

func TestRuntimeGlobalCloseSanitizesCleanupFailure(t *testing.T) {
	const secret = "global-cleanup-secret"
	thread := newFakeRuntimeThread(`{}`)
	thread.closeErr = fmt.Errorf("job close leaked %s", secret)
	runtime := testRuntime(t, &fakeRuntimeFactory{threads: []*fakeRuntimeThread{thread}})
	if _, err := runtime.StartThread(testThreadConfig(runtime)); err != nil {
		t.Fatal(err)
	}
	err := runtime.Close()
	if !errors.Is(err, agentruntime.ErrRuntimeCleanup) || errors.Is(err, agentruntime.ErrThreadFailed) || strings.Contains(err.Error(), secret) {
		t.Fatalf("global cleanup error = %v", err)
	}
	if again := runtime.Close(); again == nil || again.Error() != err.Error() {
		t.Fatalf("idempotent close error = %v, want %v", again, err)
	}
}

func TestRuntimeLocalizesEveryOwnedTurnFailure(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"process exit", agentruntime.ErrRuntimeExited, agentruntime.ErrRuntimeExited},
		{"protocol", ErrProtocol, agentruntime.ErrRuntimeProtocol},
		{"permission", ErrPermissionDenied, agentruntime.ErrPermissionDenied},
		{"repair exhaustion", errors.Join(ErrProtocol, ErrRepairExhausted), agentruntime.ErrStructuredResponse},
		{"cancelled terminal", agentruntime.ErrTurnInterrupted, agentruntime.ErrTurnInterrupted},
		{"classified fallback", errors.New("provider stopped without a final response"), agentruntime.ErrRuntimeExited},
	} {
		t.Run(test.name, func(t *testing.T) {
			failed := newFakeRuntimeThread(`{}`)
			failed.runErr = test.err
			healthy := newFakeRuntimeThread(`{"ok":true}`)
			runtime := testRuntime(t, &fakeRuntimeFactory{threads: []*fakeRuntimeThread{failed, healthy}})
			failedHandle, _ := runtime.StartThread(testThreadConfig(runtime))
			healthyHandle, _ := runtime.StartThread(testThreadConfig(runtime))
			if _, err := runtime.RunTurn(failedHandle, "fail"); !errors.Is(err, agentruntime.ErrThreadFailed) || !errors.Is(err, test.want) {
				t.Fatalf("owned failure = %v", err)
			}
			if failed.closeCount.Load() != 1 || healthy.closeCount.Load() != 0 {
				t.Fatalf("localized closes = %d/%d", failed.closeCount.Load(), healthy.closeCount.Load())
			}
			if _, err := runtime.RunTurn(healthyHandle, "continue"); err != nil {
				t.Fatalf("healthy sibling failed: %v", err)
			}
		})
	}
}

func TestRuntimeInterruptCancelGraceAndGlobalTeardown(t *testing.T) {
	for _, acknowledge := range []bool{true, false} {
		t.Run(fmt.Sprintf("acknowledge=%t", acknowledge), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			first := newFakeRuntimeThread(`{}`)
			first.run = func() (json.RawMessage, error) {
				close(entered)
				select {
				case <-release:
					return nil, agentruntime.ErrTurnInterrupted
				case <-first.done:
					return nil, agentruntime.ErrRuntimeClosed
				}
			}
			second := newFakeRuntimeThread(`{}`)
			factory := &fakeRuntimeFactory{threads: []*fakeRuntimeThread{first, second}}
			runtime := testRuntime(t, factory)
			var requested time.Duration
			timer := make(chan time.Time, 1)
			runtime.deps.after = func(duration time.Duration) <-chan time.Time {
				requested = duration
				if !acknowledge {
					go func() {
						deadline := time.Now().Add(time.Second)
						for first.cancelled.Load() == 0 && time.Now().Before(deadline) {
							time.Sleep(time.Millisecond)
						}
						timer <- time.Now()
					}()
				}
				return timer
			}
			firstHandle, _ := runtime.StartThread(testThreadConfig(runtime))
			secondHandle, _ := runtime.StartThread(testThreadConfig(runtime))
			turnDone := make(chan error, 1)
			go func() {
				_, err := runtime.RunTurn(firstHandle, "active")
				turnDone <- err
			}()
			<-entered
			if acknowledge {
				go func() {
					deadline := time.Now().Add(time.Second)
					for first.cancelled.Load() == 0 && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					close(release)
				}()
			}
			if err := runtime.Interrupt(); err != nil {
				t.Fatal(err)
			}
			if requested > interruptGracePeriod || requested <= 0 {
				t.Fatalf("cancel grace = %v", requested)
			}
			if first.cancelled.Load() != 1 || second.cancelled.Load() != 0 {
				t.Fatalf("cancel counts = first %d second %d", first.cancelled.Load(), second.cancelled.Load())
			}
			if first.closeCount.Load() != 1 || second.closeCount.Load() != 1 || len(runtime.threads) != 0 {
				t.Fatalf("global teardown = first %d second %d live %d", first.closeCount.Load(), second.closeCount.Load(), len(runtime.threads))
			}
			if _, err := runtime.RunTurn(secondHandle, "late"); !errors.Is(err, agentruntime.ErrTurnInterrupted) {
				t.Fatalf("invalidated sibling handle = %v", err)
			}
			if _, err := runtime.RunTurn(firstHandle, "late"); !errors.Is(err, agentruntime.ErrTurnInterrupted) {
				t.Fatalf("invalidated active handle = %v", err)
			}
			if err := <-turnDone; !errors.Is(err, agentruntime.ErrTurnInterrupted) {
				t.Fatalf("interrupted turn = %v", err)
			}
		})
	}
}

func TestRuntimeInterruptWithoutActiveTurnSkipsCancelAndCloseIsIdempotent(t *testing.T) {
	thread := newFakeRuntimeThread(`{}`)
	runtime := testRuntime(t, &fakeRuntimeFactory{threads: []*fakeRuntimeThread{thread}})
	handle, _ := runtime.StartThread(testThreadConfig(runtime))
	if err := runtime.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if thread.cancelled.Load() != 0 || thread.closeCount.Load() != 1 {
		t.Fatalf("idle interrupt cancel=%d close=%d", thread.cancelled.Load(), thread.closeCount.Load())
	}
	if err := runtime.CloseThread(handle); !errors.Is(err, agentruntime.ErrTurnInterrupted) {
		t.Fatalf("closed handle after interrupt = %v", err)
	}
}

func TestRuntimeRejectsForeignAndStaleHandlesAcrossGenerations(t *testing.T) {
	one := newFakeRuntimeThread(`{}`)
	two := newFakeRuntimeThread(`{}`)
	firstRuntime := testRuntime(t, &fakeRuntimeFactory{threads: []*fakeRuntimeThread{one}})
	secondRuntime := testRuntime(t, &fakeRuntimeFactory{threads: []*fakeRuntimeThread{two}})
	old, _ := firstRuntime.StartThread(testThreadConfig(firstRuntime))
	fresh, _ := secondRuntime.StartThread(testThreadConfig(secondRuntime))
	if err := firstRuntime.CloseThread(old); err != nil {
		t.Fatal(err)
	}
	if _, err := firstRuntime.RunTurn(old, "stale"); err == nil {
		t.Fatal("stale handle was accepted")
	}
	if _, err := secondRuntime.RunTurn(old, "foreign"); err == nil {
		t.Fatal("foreign runtime accepted old handle")
	}
	if _, err := secondRuntime.RunTurn(fresh, "fresh"); err != nil {
		t.Fatal(err)
	}
	one.fail(fmt.Errorf("%w: late provider ID 1", ErrProtocol))
	if _, err := secondRuntime.RunTurn(fresh, "after old late event"); err != nil {
		t.Fatalf("late event crossed runtime identity: %v", err)
	}
}

func TestRuntimeRepeatedLifecycleStress(t *testing.T) {
	const cycles = 40
	for cycle := 0; cycle < cycles; cycle++ {
		var live atomic.Int32
		newTracked := func(output string) *fakeRuntimeThread {
			thread := newFakeRuntimeThread(output)
			thread.onClose = func() { live.Add(-1) }
			return thread
		}
		first := newTracked(`{"slot":1}`)
		second := newTracked(`{"slot":2}`)
		third := newTracked(`{"slot":3}`)
		gate := make(chan struct{})
		closeStarted := make(chan struct{})
		replacement := newTracked(`{"slot":4}`)
		replacement.closeGate = gate
		replacement.closeStart = closeStarted
		factory := &fakeRuntimeFactory{
			threads: []*fakeRuntimeThread{first, second, third, replacement},
			onStart: func() { live.Add(1) },
		}
		workspace := t.TempDir()
		runtime := newRuntime(Config{Executable: "qwen", Workspace: workspace, JSONContract: "one object"}, runtimeDependencies{
			startThread: factory.start,
			after:       time.After,
		})
		config := testThreadConfig(runtime)
		handles := make([]agentruntime.Thread, 3)
		for index := range handles {
			var err error
			handles[index], err = runtime.StartThread(config)
			if err != nil {
				t.Fatalf("cycle %d start %d: %v", cycle, index, err)
			}
			if _, err := runtime.RunTurn(handles[index], "turn"); err != nil {
				t.Fatalf("cycle %d turn %d: %v", cycle, index, err)
			}
		}
		if live.Load() != 3 || len(runtime.threads) != 3 {
			t.Fatalf("cycle %d initial live = %d/%d", cycle, live.Load(), len(runtime.threads))
		}

		first.runErr = fmt.Errorf("%w: localized cycle %d", ErrProtocol, cycle)
		if _, err := runtime.RunTurn(handles[0], "fail"); !errors.Is(err, agentruntime.ErrThreadFailed) {
			t.Fatalf("cycle %d local failure: %v", cycle, err)
		}
		if live.Load() != 2 || len(runtime.threads) != 2 {
			t.Fatalf("cycle %d after failure live = %d/%d", cycle, live.Load(), len(runtime.threads))
		}
		first.fail(fmt.Errorf("%w: late provider event", ErrProtocol))
		if _, err := runtime.RunTurn(handles[2], "after late event"); err != nil {
			t.Fatalf("cycle %d late event crossed thread: %v", cycle, err)
		}

		fresh, err := runtime.StartThread(config)
		if err != nil {
			t.Fatalf("cycle %d replacement: %v", cycle, err)
		}
		if fresh.(*threadHandle).generation <= handles[2].(*threadHandle).generation || live.Load() != 3 {
			t.Fatalf("cycle %d generation/live invariant failed", cycle)
		}
		if err := runtime.CloseThread(handles[1]); err != nil {
			t.Fatalf("cycle %d close second: %v", cycle, err)
		}
		if live.Load() != 2 || len(runtime.threads) != 2 {
			t.Fatalf("cycle %d after independent close live = %d/%d", cycle, live.Load(), len(runtime.threads))
		}
		if _, err := runtime.RunTurn(handles[1], "stale"); err == nil {
			t.Fatalf("cycle %d accepted stale handle", cycle)
		}

		foreignBackend := newFakeRuntimeThread(`{}`)
		foreignRuntime := newRuntime(Config{Workspace: workspace}, runtimeDependencies{startThread: (&fakeRuntimeFactory{threads: []*fakeRuntimeThread{foreignBackend}}).start})
		foreign, err := foreignRuntime.StartThread(config)
		if err != nil {
			t.Fatalf("cycle %d foreign start: %v", cycle, err)
		}
		if _, err := runtime.RunTurn(foreign, "foreign"); err == nil {
			t.Fatalf("cycle %d accepted foreign handle", cycle)
		}
		if err := foreignRuntime.Close(); err != nil {
			t.Fatalf("cycle %d foreign close: %v", cycle, err)
		}

		threadClose := make(chan error, 1)
		go func() { threadClose <- runtime.CloseThread(fresh) }()
		select {
		case <-closeStarted:
		case <-time.After(time.Second):
			t.Fatalf("cycle %d thread close did not reach teardown", cycle)
		}
		runtimeClose := make(chan error, 1)
		go func() { runtimeClose <- runtime.Close() }()
		deadline := time.Now().Add(time.Second)
		for {
			runtime.mu.Lock()
			closing := runtime.closing
			runtime.mu.Unlock()
			if closing {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("cycle %d runtime close did not reach checkpoint", cycle)
			}
			goruntime.Gosched()
		}
		select {
		case err := <-runtimeClose:
			t.Fatalf("cycle %d close bypassed in-flight teardown: %v", cycle, err)
		default:
		}
		close(gate)
		select {
		case err := <-threadClose:
			if err != nil {
				t.Fatalf("cycle %d thread close: %v", cycle, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("cycle %d thread teardown timed out", cycle)
		}
		select {
		case err := <-runtimeClose:
			if err != nil {
				t.Fatalf("cycle %d runtime close: %v", cycle, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("cycle %d runtime teardown timed out", cycle)
		}
		if live.Load() != 0 || len(runtime.threads) != 0 {
			t.Fatalf("cycle %d final live = %d/%d", cycle, live.Load(), len(runtime.threads))
		}
		if err := runtime.Close(); err != nil {
			t.Fatalf("cycle %d idempotent close: %v", cycle, err)
		}
	}
}

func TestQwenSafeErrorClassificationMatrix(t *testing.T) {
	const secret = "credential-env-prompt-response-file-content"
	tests := []struct {
		name string
		in   error
		want error
	}{
		{"missing or non-regular executable", ErrConfiguration, agentruntime.ErrRuntimeConfiguration},
		{"startup", ErrStartup, agentruntime.ErrRuntimeStartup},
		{"containment", ErrContainment, agentruntime.ErrRuntimeContainment},
		{"handshake capability", ErrIncompatible, agentruntime.ErrRuntimeIncompatible},
		{"process exit", agentruntime.ErrRuntimeExited, agentruntime.ErrRuntimeExited},
		{"protocol", ErrProtocol, agentruntime.ErrRuntimeProtocol},
		{"permission", ErrPermissionDenied, agentruntime.ErrPermissionDenied},
		{"repair", errors.Join(ErrProtocol, ErrRepairExhausted), agentruntime.ErrStructuredResponse},
		{"operator cancellation", agentruntime.ErrTurnInterrupted, agentruntime.ErrTurnInterrupted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := safeRuntimeError("fixture", fmt.Errorf("%w: %s", test.in, secret))
			if !errors.Is(err, test.want) {
				t.Fatalf("classification = %v, want %v", err, test.want)
			}
			if !strings.Contains(err.Error(), "qwen") || strings.Contains(err.Error(), secret) {
				t.Fatalf("unsafe Qwen diagnostic = %q", err)
			}
		})
	}
}

func TestQwenSafeErrorRetainsOnlyAllowlistedCapabilityContext(t *testing.T) {
	const secret = "credential-body-do-not-echo"
	err := safeRuntimeError("start thread", withDiagnosticContext(fmt.Errorf("%w: missing promptCapabilities: %s", ErrIncompatible, secret), diagnosticPromptCapabilities))
	if !errors.Is(err, agentruntime.ErrRuntimeIncompatible) || !strings.Contains(err.Error(), "promptCapabilities") || strings.Contains(err.Error(), secret) {
		t.Fatalf("capability diagnostic = %q", err)
	}
	raw := safeRuntimeError("start thread", fmt.Errorf("%w: agent %s", ErrIncompatible, secret))
	if strings.Contains(raw.Error(), "(agent)") || strings.Contains(raw.Error(), secret) {
		t.Fatalf("untyped provider text escaped diagnostic boundary = %q", raw)
	}
}
