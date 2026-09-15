package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var (
	// ErrUserControlBusy means that a controller has already registered an
	// external action for this run. The implementation flow is sequential, so
	// a second action would make a user interruption ambiguous.
	ErrUserControlBusy = errors.New("implementation user control already has an active operation")
	// ErrUserControlTransitioning means pause or close is waiting for the
	// active operation's cancellation cleanup. Starting new work in that gap is
	// forbidden.
	ErrUserControlTransitioning = errors.New("implementation user control is transitioning run state")
)

// UserRunControl is the controller-owned user pause/close boundary. It gives
// an active agent or command a cancellable context, waits for that operation
// to finish its own post-interruption bookkeeping, and only then records the
// requested lifecycle transition durably. It never changes the workspace or
// invokes Git, so commits and any allowed uncommitted work are retained.
//
// Callers must use BeginOperation for every externally-running agent or
// command that the user may pause or close. ControlledAgentCall accepts this
// type directly; ControlledCheckRunner adapts configured commands.
type UserRunControl struct {
	mu            sync.Mutex
	run           *implementationstate.Run
	stateStore    *runstore.StateStore
	active        *userControlledOperation
	transitioning bool
}

type userControlledOperation struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// NewUserRunControl creates the process-local control boundary for one
// already-open durable run.
func NewUserRunControl(run *implementationstate.Run, stateStore *runstore.StateStore) (*UserRunControl, error) {
	if run == nil || stateStore == nil {
		return nil, errors.New("implementation user control requires run and state store")
	}
	return &UserRunControl{run: run, stateStore: stateStore}, nil
}

// BeginOperation registers one agent or command and returns the context that
// must be passed to it. finish must be called after all post-call state writes
// have completed. A pause/close cancels this context and waits for finish,
// which prevents the durable lifecycle event from racing an interrupted
// attempt result.
func (control *UserRunControl) BeginOperation(parent context.Context) (context.Context, func(), error) {
	if control == nil {
		return nil, nil, errors.New("implementation user control is nil")
	}
	if parent == nil {
		parent = context.Background()
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.transitioning {
		return nil, nil, ErrUserControlTransitioning
	}
	if control.run.Status != implementationstate.RunActive {
		return nil, nil, fmt.Errorf("%w: only an active run can start an operation", implementationstate.ErrInvalidTransition)
	}
	if control.active != nil {
		return nil, nil, ErrUserControlBusy
	}
	ctx, cancel := context.WithCancel(parent)
	active := &userControlledOperation{cancel: cancel, done: make(chan struct{})}
	control.active = active
	finish := func() {
		active.once.Do(func() {
			control.mu.Lock()
			if control.active == active {
				control.active = nil
			}
			control.mu.Unlock()
			close(active.done)
		})
	}
	return ctx, finish, nil
}

// Pause interrupts an active operation, if any, and durably records a
// resumable user pause. It intentionally performs no Git reset, checkout, or
// workspace restoration. A normal pause does not reset counters; that policy
// remains in implementationstate.Resume for limit pauses only.
func (control *UserRunControl) Pause(ctx context.Context, reason string) error {
	return control.transition(ctx, reason, false)
}

// Close interrupts an active operation, if any, and durably records terminal
// closure. It deliberately calls Run.Close, never Run.Succeed: a user stop is
// not a successful implementation of the change and cannot be resumed.
func (control *UserRunControl) Close(ctx context.Context, reason string) error {
	return control.transition(ctx, reason, true)
}

func (control *UserRunControl) transition(ctx context.Context, reason string, closeRun bool) error {
	if control == nil {
		return errors.New("implementation user control is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	control.mu.Lock()
	if control.transitioning {
		control.mu.Unlock()
		return ErrUserControlTransitioning
	}
	if !mayUserTransition(control.run.Status, closeRun) {
		control.mu.Unlock()
		return fmt.Errorf("%w: run cannot be user-%s", implementationstate.ErrInvalidTransition, userTransitionName(closeRun))
	}
	control.transitioning = true
	active := control.active
	if active != nil {
		active.cancel()
	}
	control.mu.Unlock()

	if active != nil {
		// The active operation owns provider/process termination and records its
		// interrupted attempt. Do not inherit a UI cancellation here: the user
		// request must still become durable once the child has stopped.
		<-active.done
	}

	control.mu.Lock()
	defer control.mu.Unlock()
	defer func() { control.transitioning = false }()
	if !mayUserTransition(control.run.Status, closeRun) {
		return fmt.Errorf("%w: run changed while user-%s was in progress", implementationstate.ErrInvalidTransition, userTransitionName(closeRun))
	}
	candidate, err := userControlCandidate(control.run)
	if err != nil {
		return err
	}
	if closeRun {
		err = candidate.Close(reason)
	} else {
		err = candidate.Pause(reason)
	}
	if err != nil {
		return err
	}
	event, err := control.stateStore.Record(context.WithoutCancel(ctx), candidate)
	// A non-zero event means the JSONL record is durable even when projection
	// application reported an error. Keep the in-memory object aligned so a
	// retry applies the pending event instead of undoing the user decision.
	if event.Sequence != 0 {
		*control.run = *candidate
	}
	if err != nil {
		return fmt.Errorf("persist user-%s: %w", userTransitionName(closeRun), err)
	}
	return nil
}

func mayUserTransition(status implementationstate.RunStatus, closeRun bool) bool {
	if closeRun {
		return status == implementationstate.RunActive || status == implementationstate.RunPaused
	}
	return status == implementationstate.RunActive
}

func userTransitionName(closeRun bool) string {
	if closeRun {
		return "close"
	}
	return "pause"
}

func userControlCandidate(run *implementationstate.Run) (*implementationstate.Run, error) {
	event, err := implementationstate.NewRunStateEvent(1, run)
	if err != nil {
		return nil, err
	}
	return event.State, nil
}

// ControlledCheckRunner makes a configured command interruptible by
// UserRunControl. DirectCheckRunner remains the command execution primitive;
// this wrapper merely owns the cancellable lifecycle boundary used by the UI.
type ControlledCheckRunner struct {
	Control *UserRunControl
	Runner  CheckRunner
}

func (runner ControlledCheckRunner) RunCheck(ctx context.Context, command checkexec.Command) (checkexec.Result, error) {
	if runner.Runner == nil {
		return checkexec.Result{}, fmt.Errorf("%w: check runner is required", ErrInvalidCheckSet)
	}
	if runner.Control == nil {
		return runner.Runner.RunCheck(ctx, command)
	}
	operationContext, finish, err := runner.Control.BeginOperation(ctx)
	if err != nil {
		return checkexec.Result{}, err
	}
	defer finish()
	return runner.Runner.RunCheck(operationContext, command)
}
