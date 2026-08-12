package specflow

import (
	"encoding/json"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/codexapp"
)

type appRuntime interface {
	StartThread() (*codexapp.Thread, error)
	RunTurn(*codexapp.Thread, string, codexapp.TurnOptions) (json.RawMessage, error)
	Interrupt() error
	Close() error
}

type runtimeSlot struct{ runtime appRuntime }

// Session lazily owns the single App Server used by one interactive process.
type Session struct {
	start func() (appRuntime, error)

	mu      sync.Mutex
	runtime *runtimeSlot
	stopped bool
}

func NewSession(executable, workspace string) *Session {
	return newSession(func() (appRuntime, error) { return codexapp.StartRuntime(executable, workspace) })
}

func newSession(start func() (appRuntime, error)) *Session { return &Session{start: start} }

func (session *Session) StartThread() (*codexapp.Thread, error) {
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

func (session *Session) RunTurn(thread *codexapp.Thread, prompt string, options codexapp.TurnOptions) (json.RawMessage, error) {
	session.mu.Lock()
	slot := session.runtime
	session.mu.Unlock()
	if slot == nil {
		return nil, codexapp.ErrRuntimeClosed
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
	defer session.mu.Unlock()
	if session.stopped {
		return nil, codexapp.ErrRuntimeClosed
	}
	if session.runtime == nil {
		runtime, err := session.start()
		if err != nil {
			return nil, err
		}
		session.runtime = &runtimeSlot{runtime: runtime}
	}
	return session.runtime, nil
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
	session.runtime = nil
	session.mu.Unlock()
	if slot == nil {
		return nil
	}
	if interrupt {
		return slot.runtime.Interrupt()
	}
	return slot.runtime.Close()
}

var _ initialTurnRunner = (*Session)(nil)
