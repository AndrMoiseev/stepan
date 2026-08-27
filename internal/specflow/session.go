package specflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

type runtimeSlot struct{ runtime agentruntime.Runtime }

type runtimeAttempt struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// appRuntime is kept as a local test-facing name while the implementation
// depends on the provider-neutral runtime contract.
type appRuntime = agentruntime.Runtime

// Session lazily owns the single App Server used by one interactive process.
type Session struct {
	start func(context.Context) (agentruntime.Runtime, error)

	mu       sync.Mutex
	runtime  *runtimeSlot
	starting *runtimeAttempt
	stopped  bool
}

func NewSession(start func(context.Context) (agentruntime.Runtime, error)) *Session {
	return newSession(start)
}

func newSession(start func(context.Context) (agentruntime.Runtime, error)) *Session {
	return &Session{start: start}
}

func (session *Session) StartThread() (agentruntime.Thread, error) {
	slot, err := session.current()
	if err != nil {
		return nil, err
	}
	thread, err := slot.runtime.StartThread()
	if err != nil {
		session.discard(slot)
	}
	return thread, err
}

func (session *Session) RunTurn(thread agentruntime.Thread, prompt string, options agentruntime.TurnOptions) (json.RawMessage, error) {
	session.mu.Lock()
	slot := session.runtime
	session.mu.Unlock()
	if slot == nil {
		return nil, agentruntime.ErrRuntimeClosed
	}
	output, err := slot.runtime.RunTurn(thread, prompt, options)
	if err != nil {
		session.discard(slot)
	}
	return output, err
}

func (session *Session) Interrupt() error { return session.stop(true) }

func (session *Session) Close() error { return session.stop(false) }

func (session *Session) current() (*runtimeSlot, error) {
	session.mu.Lock()
	if session.stopped {
		session.mu.Unlock()
		return nil, agentruntime.ErrRuntimeClosed
	}
	if session.runtime != nil {
		runtime := session.runtime
		session.mu.Unlock()
		return runtime, nil
	}
	if attempt := session.starting; attempt != nil {
		session.mu.Unlock()
		<-attempt.done
		session.mu.Lock()
		runtime, err := session.runtime, attempt.err
		stopped := session.stopped
		session.mu.Unlock()
		if stopped {
			return nil, agentruntime.ErrRuntimeClosed
		}
		return runtime, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	attempt := &runtimeAttempt{cancel: cancel, done: make(chan struct{})}
	session.starting = attempt
	session.mu.Unlock()

	var runtime agentruntime.Runtime
	var err error
	if ctx.Err() != nil {
		err = ctx.Err()
	} else {
		runtime, err = session.start(ctx)
	}
	if err == nil && isNilRuntime(runtime) {
		err = errors.New("runtime factory returned nil")
	}

	session.mu.Lock()
	stopped := session.stopped
	var slot *runtimeSlot
	if err == nil && !stopped {
		slot = &runtimeSlot{runtime: runtime}
		session.runtime = slot
	}
	if stopped && (err == nil || errors.Is(err, context.Canceled)) {
		err = agentruntime.ErrRuntimeClosed
	}
	attempt.err = err
	session.starting = nil
	close(attempt.done)
	session.mu.Unlock()
	if err != nil && !isNilRuntime(runtime) {
		_ = runtime.Close()
	}
	return slot, err
}

func isNilRuntime(runtime agentruntime.Runtime) bool {
	if runtime == nil {
		return true
	}
	value := reflect.ValueOf(runtime)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (session *Session) discard(slot *runtimeSlot) {
	session.mu.Lock()
	if session.runtime == slot {
		session.runtime = nil
	}
	session.mu.Unlock()
	_ = slot.runtime.Close()
}

func (session *Session) stop(interrupt bool) error {
	session.mu.Lock()
	session.stopped = true
	slot := session.runtime
	attempt := session.starting
	session.runtime = nil
	session.mu.Unlock()
	if attempt != nil {
		attempt.cancel()
		<-attempt.done
	}
	if slot == nil {
		return nil
	}
	if interrupt {
		return slot.runtime.Interrupt()
	}
	return slot.runtime.Close()
}

var _ initialTurnRunner = (*Session)(nil)
