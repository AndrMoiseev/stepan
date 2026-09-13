package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// DefaultAgentCallTimeout bounds one agent turn, including provider tools.
// Project configuration may supply a different positive value.
const DefaultAgentCallTimeout = 1800 * time.Second

var (
	ErrAgentCallTimedOut  = errors.New("implementation agent call timed out")
	ErrAgentCallCancelled = errors.New("implementation agent call cancelled")
)

// ControlledAgentCall is the controller-owned input for one logical agent
// operation. Attempts are reserved against OperationID before each dispatch;
// a retry therefore stays in the same semantic round.
type ControlledAgentCall struct {
	Session      *AgentSession
	Repository   string
	Policy       AgentCallPolicy
	Run          *implementationstate.Run
	Journal      *runstore.Run
	AssignmentID implementationstate.AssignmentID // empty for run-scoped work
	OperationID  implementationstate.OperationID
	Limits       implementationstate.CycleLimits
	Expectation  ResponseExpectation
	Message      string
	Timeout      time.Duration // zero selects DefaultAgentCallTimeout
}

// ControlledAgentCallResult is returned only for a response that passed both
// the file post-check and response binding. Attempts includes rejected
// technical attempts, making the caller's progress display deterministic.
type ControlledAgentCallResult struct {
	Response AgentResponse
	// Snapshot is the post-call Git fingerprint that must be checked before
	// the next controller operation.
	Snapshot gitsnapshot.Snapshot
	Attempts uint64
}

// InvokeControlledAgentCall runs one logical operation with bounded technical
// retries. Provider crashes, malformed responses, timeouts, and restored
// write-policy violations all consume a technical attempt. Operator
// cancellation is terminal: it interrupts the turn, performs the post-check,
// and never silently starts a replacement call.
func InvokeControlledAgentCall(ctx context.Context, call ControlledAgentCall) (ControlledAgentCallResult, error) {
	if err := validateControlledAgentCall(call); err != nil {
		return ControlledAgentCallResult{}, err
	}
	timeout := call.Timeout
	if timeout == 0 {
		timeout = DefaultAgentCallTimeout
	}
	if timeout < 0 {
		return ControlledAgentCallResult{}, errors.New("implementation agent call timeout must not be negative")
	}

	message := call.Message
	var attempts uint64
	for {
		if err := ctx.Err(); err != nil {
			return ControlledAgentCallResult{Attempts: attempts}, errors.Join(ErrAgentCallCancelled, err)
		}
		attempt, err := reserveAgentAttempt(call)
		if err != nil {
			return ControlledAgentCallResult{Attempts: attempts}, err
		}
		attempts = attempt.Number

		var raw json.RawMessage
		// The provider is cancelled through ctx, but the Git post-check must
		// still run after cancellation so a cancelled turn cannot evade the
		// write boundary.
		outcome, err := ObserveAgentCall(context.WithoutCancel(ctx), call.Repository, call.Policy, call.Run, call.Journal, func() error {
			var turnErr error
			raw, turnErr = runBoundedAgentTurn(ctx, call.Session, message, timeout)
			return turnErr
		})
		if err != nil {
			return ControlledAgentCallResult{Attempts: attempts}, err
		}
		if outcome.Disposition == CallExecutionBlocked {
			return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, fmt.Errorf("%s", outcome.Diagnostic)
		}

		if errors.Is(outcome.InvocationError, ErrAgentCallCancelled) || errors.Is(outcome.InvocationError, context.Canceled) {
			return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, outcome.InvocationError
		}
		if outcome.Disposition == CallRetry {
			message = retryPrompt(outcome.Diagnostic)
			continue
		}
		if outcome.InvocationError != nil {
			message = retryPrompt(outcome.InvocationError.Error())
			continue
		}
		response, bindErr := BindAgentResponse(call.Expectation, raw)
		if bindErr != nil {
			message = retryPrompt(bindErr.Error())
			continue
		}
		return ControlledAgentCallResult{Response: response, Snapshot: outcome.Snapshot, Attempts: attempts}, nil
	}
}

func validateControlledAgentCall(call ControlledAgentCall) error {
	if call.Session == nil || call.Run == nil || call.Journal == nil || call.Repository == "" || call.OperationID == "" {
		return errors.New("controlled agent call requires session, repository, run, journal, and operation ID")
	}
	if call.Expectation.Binding.CallID == "" || call.Policy.CallID != call.Expectation.Binding.CallID {
		return errors.New("controlled agent call policy and response binding must share a call ID")
	}
	return nil
}

func reserveAgentAttempt(call ControlledAgentCall) (implementationstate.OperationAttempt, error) {
	if call.AssignmentID != "" {
		return call.Run.StartAssignmentAttemptWithLimits(call.AssignmentID, call.OperationID, call.Limits)
	}
	return call.Run.StartRunAttemptWithLimits(call.OperationID, call.Limits)
}

func retryPrompt(diagnostic string) string {
	return "The previous response was not accepted by the controller: " + diagnostic + ". Repeat the same requested action and return a valid structured response."
}

func runBoundedAgentTurn(parent context.Context, session *AgentSession, message string, timeout time.Duration) (json.RawMessage, error) {
	turnContext, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	type result struct {
		raw json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		raw, err := session.RunTurn(message)
		done <- result{raw: raw, err: err}
	}()

	select {
	case result := <-done:
		return result.raw, result.err
	case <-turnContext.Done():
		interruptErr := session.Interrupt()
		result := <-done // post-check must observe the completed provider turn.
		if parent.Err() != nil {
			return result.raw, errors.Join(ErrAgentCallCancelled, parent.Err(), interruptErr)
		}
		return result.raw, errors.Join(ErrAgentCallTimedOut, context.DeadlineExceeded, interruptErr)
	}
}
