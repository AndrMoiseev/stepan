package qwenapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

const interruptGracePeriod = 3 * time.Second

// The non-zero-sized token makes pointer identity stable even under the Go
// implementation's permitted coalescing of zero-sized allocations.
type runtimeIdentity struct{ token byte }

// threadHandle contains only process-local ownership data. ACP process,
// session, and request identities never cross the adapter boundary.
type threadHandle struct {
	identity   *runtimeIdentity
	generation uint64
	state      *threadState
}

type threadState struct {
	generation uint64
	backend    runtimeThread
	closed     bool
	failure    error
}

type activeTurn struct {
	thread *threadState
	done   chan struct{}
}

type startupState struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type runtimeDependencies struct {
	startThread func(context.Context, Config, agentruntime.ThreadConfig) (runtimeThread, error)
	after       func(time.Duration) <-chan time.Time
}

// Runtime owns a set of independent contained Qwen processes. Processes are
// created lazily by StartThread and are never shared between logical threads.
type Runtime struct {
	config   Config
	identity *runtimeIdentity
	deps     runtimeDependencies
	grace    time.Duration

	mu          sync.Mutex
	threads     map[uint64]*threadState
	next        uint64
	nextStart   uint64
	active      *activeTurn
	closing     bool
	interrupted bool
	starting    map[uint64]*startupState
	tearingDown sync.WaitGroup
	turnMu      sync.Mutex
	closeOnce   sync.Once
	closeDone   chan struct{}
	closeErr    error
}

// StartRuntime creates a lazy Qwen runtime. Executable resolution, process
// containment, ACP initialization, and session preflight happen per thread.
func StartRuntime(config Config) (*Runtime, error) {
	if err := validateSchemaObject(config.EnvelopeSchema); err != nil {
		return nil, safeRuntimeError("configure runtime", errors.Join(ErrConfiguration, err))
	}
	return newRuntime(config, runtimeDependencies{
		startThread: startQwenThread,
		after:       time.After,
	}), nil
}

func newRuntime(config Config, deps runtimeDependencies) *Runtime {
	config.Executable = strings.Clone(config.Executable)
	config.Workspace = strings.Clone(config.Workspace)
	config.JSONContract = strings.Clone(config.JSONContract)
	config.EnvelopeSchema = append(json.RawMessage(nil), config.EnvelopeSchema...)
	if deps.startThread == nil {
		deps.startThread = startQwenThread
	}
	if deps.after == nil {
		deps.after = time.After
	}
	return &Runtime{
		config: config, identity: &runtimeIdentity{}, deps: deps,
		grace: interruptGracePeriod, threads: make(map[uint64]*threadState), starting: make(map[uint64]*startupState), closeDone: make(chan struct{}),
	}
}

var _ agentruntime.Runtime = (*Runtime)(nil)

func (runtime *Runtime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	config = config.Clone()
	config.BootstrapInstructions = strings.Clone(config.BootstrapInstructions)
	if err := config.Validate(); err != nil {
		return nil, agentruntime.MarkThreadFailed(safeRuntimeError("start thread", errors.Join(ErrConfiguration, err)))
	}
	if config.Workspace != runtime.config.Workspace {
		return nil, agentruntime.MarkThreadFailed(safeRuntimeError("start thread", ErrConfiguration))
	}

	runtime.mu.Lock()
	if err := runtime.stateErrorLocked(); err != nil {
		runtime.mu.Unlock()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime.nextStart++
	startID := runtime.nextStart
	startup := &startupState{cancel: cancel, done: make(chan struct{})}
	runtime.starting[startID] = startup
	runtime.mu.Unlock()
	defer func() {
		cancel()
		runtime.mu.Lock()
		if runtime.starting[startID] == startup {
			delete(runtime.starting, startID)
		}
		close(startup.done)
		runtime.mu.Unlock()
	}()

	backend, err := runtime.deps.startThread(ctx, runtime.config, config)
	if err != nil {
		if backend != nil {
			backend.BeginClose()
			_ = backend.Close()
		}
		runtime.mu.Lock()
		stateErr := runtime.stateErrorLocked()
		runtime.mu.Unlock()
		if stateErr != nil {
			return nil, stateErr
		}
		return nil, agentruntime.MarkThreadFailed(safeRuntimeError("start thread", err))
	}
	if backend == nil {
		runtime.mu.Lock()
		stateErr := runtime.stateErrorLocked()
		runtime.mu.Unlock()
		if stateErr != nil {
			return nil, stateErr
		}
		return nil, agentruntime.MarkThreadFailed(safeRuntimeError("start thread", agentruntime.ErrRuntimeExited))
	}

	runtime.mu.Lock()
	if stateErr := runtime.stateErrorLocked(); stateErr != nil {
		runtime.mu.Unlock()
		backend.BeginClose()
		_ = backend.Close()
		return nil, stateErr
	}
	select {
	case <-backend.Done():
		failure := backend.Err()
		runtime.mu.Unlock()
		_ = backend.Close()
		return nil, agentruntime.MarkThreadFailed(safeRuntimeError("start thread", failure))
	default:
	}
	runtime.next++
	state := &threadState{generation: runtime.next, backend: backend}
	runtime.threads[state.generation] = state
	handle := &threadHandle{identity: runtime.identity, generation: state.generation, state: state}
	runtime.mu.Unlock()

	go runtime.monitor(state)
	return handle, nil
}

func (runtime *Runtime) RunTurn(handle agentruntime.Thread, prompt string) (json.RawMessage, error) {
	state, err := runtime.lookup(handle)
	if err != nil {
		return nil, err
	}
	if !runtime.turnMu.TryLock() {
		return nil, agentruntime.ErrTurnInProgress
	}
	defer runtime.turnMu.Unlock()

	runtime.mu.Lock()
	if err := runtime.validateStateLocked(state); err != nil {
		runtime.mu.Unlock()
		return nil, err
	}
	active := &activeTurn{thread: state, done: make(chan struct{})}
	runtime.active = active
	runtime.mu.Unlock()
	defer func() {
		close(active.done)
		runtime.mu.Lock()
		if runtime.active == active {
			runtime.active = nil
		}
		runtime.mu.Unlock()
	}()

	output, runErr := state.backend.RunTurn(prompt)
	if runErr == nil {
		return append(json.RawMessage(nil), output...), nil
	}
	runtime.mu.Lock()
	interrupted := runtime.interrupted
	runtime.mu.Unlock()
	if interrupted {
		return nil, agentruntime.ErrTurnInterrupted
	}
	if errors.Is(runErr, agentruntime.ErrTurnInProgress) {
		return nil, agentruntime.ErrTurnInProgress
	}
	classified := safeRuntimeError("run turn", runErr)
	runtime.failThread(state, classified)
	return nil, agentruntime.MarkThreadFailed(classified)
}

func (runtime *Runtime) CloseThread(handle agentruntime.Thread) error {
	state, err := runtime.lookup(handle)
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	if err := runtime.validateStateLocked(state); err != nil {
		runtime.mu.Unlock()
		return err
	}
	backend := runtime.detachThreadLocked(state, nil)
	runtime.tearingDown.Add(1)
	runtime.mu.Unlock()
	defer runtime.tearingDown.Done()
	if closeErr := backend.Close(); closeErr != nil {
		failure := safeRuntimeError("close thread", errors.Join(agentruntime.ErrRuntimeCleanup, closeErr))
		runtime.mu.Lock()
		state.failure = failure
		runtime.mu.Unlock()
		return agentruntime.MarkThreadFailed(failure)
	}
	return nil
}

// Interrupt preserves the provider-neutral global semantics: cancel only the
// active session, close its pending approvals, wait at most three seconds, and
// then tear down every process tree and invalidate every handle.
func (runtime *Runtime) Interrupt() error {
	runtime.mu.Lock()
	if runtime.closing {
		runtime.mu.Unlock()
		<-runtime.closeDone
		return runtime.closeErr
	}
	runtime.interrupted = true
	active := runtime.active
	runtime.mu.Unlock()

	if active != nil {
		go func() { _ = active.thread.backend.Cancel() }()
		grace := runtime.grace
		if grace > interruptGracePeriod {
			grace = interruptGracePeriod
		}
		select {
		case <-active.done:
		case <-runtime.deps.after(grace):
		}
	}
	return runtime.Close()
}

func (runtime *Runtime) Close() error {
	runtime.closeOnce.Do(func() {
		runtime.mu.Lock()
		runtime.closing = true // global close checkpoint
		threads := make([]*threadState, 0, len(runtime.threads))
		for _, state := range runtime.threads {
			runtime.detachThreadLocked(state, nil)
			threads = append(threads, state)
		}
		startups := make([]*startupState, 0, len(runtime.starting))
		for _, startup := range runtime.starting {
			startup.cancel()
			startups = append(startups, startup)
		}
		runtime.mu.Unlock()

		// Starting processes are cancelled at the checkpoint. Production startup
		// owns a cancellation watcher that closes its connection/process, while
		// this bounded wait prevents an uncooperative dependency from hanging the
		// global lifecycle forever.
		if len(startups) > 0 {
			grace := runtime.grace
			if grace <= 0 || grace > interruptGracePeriod {
				grace = interruptGracePeriod
			}
			startupDeadline := runtime.deps.after(grace)
		startupWait:
			for _, startup := range startups {
				select {
				case <-startup.done:
				case <-startupDeadline:
					runtime.closeErr = errors.Join(runtime.closeErr, safeRuntimeError("close startup", agentruntime.ErrRuntimeCleanup))
					break startupWait
				}
			}
		}
		runtime.tearingDown.Wait()
		for _, state := range threads {
			if err := state.backend.Close(); err != nil {
				runtime.closeErr = errors.Join(runtime.closeErr, safeRuntimeError("close runtime", errors.Join(agentruntime.ErrRuntimeCleanup, err)))
			}
		}
		close(runtime.closeDone)
	})
	<-runtime.closeDone
	return runtime.closeErr
}

func (runtime *Runtime) lookup(value agentruntime.Thread) (*threadState, error) {
	handle, ok := value.(*threadHandle)
	if !ok || handle == nil || handle.identity != runtime.identity || handle.state == nil || handle.generation == 0 {
		return nil, errors.New("invalid Qwen thread handle")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := runtime.stateErrorLocked(); err != nil {
		return nil, err
	}
	state := runtime.threads[handle.generation]
	if state != handle.state || state.generation != handle.generation || state.closed {
		if handle.state.failure != nil {
			return nil, agentruntime.MarkThreadFailed(handle.state.failure)
		}
		return nil, errors.New("invalid Qwen thread handle")
	}
	return state, nil
}

func (runtime *Runtime) validateStateLocked(state *threadState) error {
	if err := runtime.stateErrorLocked(); err != nil {
		return err
	}
	if state == nil || state.closed || runtime.threads[state.generation] != state {
		return errors.New("invalid Qwen thread handle")
	}
	return nil
}

func (runtime *Runtime) stateErrorLocked() error {
	if runtime.interrupted {
		return agentruntime.ErrTurnInterrupted
	}
	if runtime.closing {
		return agentruntime.ErrRuntimeClosed
	}
	return nil
}

func (runtime *Runtime) monitor(state *threadState) {
	<-state.backend.Done()
	runtime.failThread(state, safeRuntimeError("connection", state.backend.Err()))
}

func (runtime *Runtime) failThread(state *threadState, failure error) {
	runtime.mu.Lock()
	if runtime.threads[state.generation] != state || state.closed || runtime.closing {
		runtime.mu.Unlock()
		return
	}
	backend := runtime.detachThreadLocked(state, failure)
	runtime.tearingDown.Add(1)
	runtime.mu.Unlock()
	defer runtime.tearingDown.Done()
	_ = backend.Close()
}

// detachThreadLocked establishes the close checkpoint shared by local close,
// localized failure, and global Close (including Interrupt). Callers decide
// whether teardown belongs to the global closer or an independently tracked
// operation, but lock ordering and handle invalidation remain identical.
func (runtime *Runtime) detachThreadLocked(state *threadState, failure error) runtimeThread {
	if failure != nil {
		state.failure = failure
	}
	state.closed = true
	delete(runtime.threads, state.generation)
	state.backend.BeginClose()
	return state.backend
}

// safeRuntimeError retains only provider-neutral categories. Lower layers
// already keep method/capability names bounded, but runtime errors are the
// user-facing boundary and therefore never forward arbitrary child text.
func safeRuntimeError(action string, err error) error {
	if err == nil {
		err = agentruntime.ErrRuntimeExited
	}
	categories := make([]error, 0, 3)
	for _, category := range []error{
		agentruntime.ErrRuntimeConfiguration,
		agentruntime.ErrRuntimeStartup,
		agentruntime.ErrRuntimeContainment,
		agentruntime.ErrRuntimeIncompatible,
		agentruntime.ErrRuntimeProtocol,
		agentruntime.ErrStructuredResponse,
		agentruntime.ErrRuntimeCleanup,
		agentruntime.ErrPermissionDenied,
		agentruntime.ErrTurnInterrupted,
		agentruntime.ErrTurnInProgress,
		agentruntime.ErrRuntimeClosed,
		agentruntime.ErrRuntimeExited,
	} {
		if errors.Is(err, category) {
			categories = append(categories, category)
		}
	}
	if len(categories) == 0 {
		categories = append(categories, agentruntime.ErrRuntimeExited)
	}
	contexts := make([]string, 0, 2)
	if context := errorDiagnosticContext(err); context != "" {
		contexts = append(contexts, context)
	}
	if context := errorProcessExitDiagnostic(err); context != "" {
		contexts = append(contexts, context)
	}
	if context := errorRPCDiagnostic(err); context != "" {
		contexts = append(contexts, context)
	}
	if len(contexts) > 0 {
		return fmt.Errorf("qwen %s (%s): %w", action, strings.Join(contexts, "; "), errors.Join(categories...))
	}
	return fmt.Errorf("qwen %s: %w", action, errors.Join(categories...))
}
