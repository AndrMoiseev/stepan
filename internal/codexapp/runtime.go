package codexapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrRuntimeClosed   = errors.New("App Server runtime closed")
	ErrTurnInterrupted = errors.New("turn interrupted by operator")
	ErrAppServerExited = errors.New("App Server exited unexpectedly")
)

const interruptGracePeriod = 3 * time.Second

// Runtime owns one Process and Connection. A failed or closed Runtime is never
// restarted; create a new one so thread IDs cannot leak across processes.
type Runtime struct {
	process    *Process
	connection *Connection
	workspace  string
	grace      time.Duration

	mu          sync.Mutex
	closing     bool
	interrupted bool
	closeOnce   sync.Once
	closeErr    error
	closed      chan struct{}
}

func StartRuntime(executable, workspace string) (*Runtime, error) {
	process := NewProcess(executable, workspace)
	if err := process.Start(); err != nil {
		_ = process.Close()
		return nil, err
	}
	connection, err := NewConnection(NewTransport(process.Stdout(), process.Stdin()), Handler{})
	if err != nil {
		_ = process.Close()
		return nil, err
	}
	runtime := &Runtime{process: process, connection: connection, workspace: workspace, grace: interruptGracePeriod, closed: make(chan struct{})}
	go func() {
		waitErr := process.Wait()
		runtime.mu.Lock()
		closing := runtime.closing
		runtime.mu.Unlock()
		if !closing {
			if waitErr == nil {
				waitErr = errors.New("process exited")
			}
			connection.fail(fmt.Errorf("%w: %v; %s", ErrAppServerExited, waitErr, process.Diagnostic()))
		}
	}()
	return runtime, nil
}

func (runtime *Runtime) StartThread() (*Thread, error) {
	if err := runtime.stateError(); err != nil {
		return nil, err
	}
	thread, err := runtime.connection.StartThread(runtime.workspace)
	return thread, runtime.classify(err)
}

func (runtime *Runtime) RunTurn(thread *Thread, prompt string, options TurnOptions) (json.RawMessage, error) {
	if err := runtime.stateError(); err != nil {
		return nil, err
	}
	output, err := runtime.connection.RunTurn(thread, prompt, options)
	return output, runtime.classify(err)
}

// Interrupt sends turn/interrupt when a turn ID exists, waits only for its
// terminal path, and always closes the contained process tree.
func (runtime *Runtime) Interrupt() error {
	runtime.mu.Lock()
	if runtime.closing {
		runtime.mu.Unlock()
		<-runtime.closed
		return runtime.closeErr
	}
	runtime.interrupted = true
	runtime.mu.Unlock()

	threadID, turnID, done, active := runtime.connection.activeTurn()
	if active && turnID != "" {
		go func() {
			_ = runtime.connection.Call("turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}, nil)
		}()
		timer := time.NewTimer(runtime.grace)
		select {
		case <-done:
		case <-runtime.connection.Done():
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	return runtime.Close()
}

func (runtime *Runtime) Close() error {
	runtime.closeOnce.Do(func() {
		runtime.mu.Lock()
		runtime.closing = true
		runtime.mu.Unlock()
		_ = runtime.connection.Close()
		runtime.closeErr = runtime.process.Close()
		close(runtime.closed)
	})
	return runtime.closeErr
}

func (runtime *Runtime) stateError() error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.interrupted {
		return ErrTurnInterrupted
	}
	if runtime.closing {
		return ErrRuntimeClosed
	}
	return nil
}

func (runtime *Runtime) classify(err error) error {
	if err == nil {
		return nil
	}
	runtime.mu.Lock()
	interrupted, closing := runtime.interrupted, runtime.closing
	runtime.mu.Unlock()
	if interrupted {
		return fmt.Errorf("%w: %v", ErrTurnInterrupted, err)
	}
	if closing {
		return fmt.Errorf("%w: %v", ErrRuntimeClosed, err)
	}
	if errors.Is(err, ErrConnectionClosed) {
		return fmt.Errorf("%w: %v; %s", ErrAppServerExited, err, runtime.process.Diagnostic())
	}
	return err
}
