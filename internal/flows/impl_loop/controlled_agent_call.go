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
	StateStore   *runstore.StateStore
	AssignmentID implementationstate.AssignmentID // empty for run-scoped work
	OperationID  implementationstate.OperationID
	Limits       implementationstate.CycleLimits
	Expectation  ResponseExpectation
	Message      string
	Timeout      time.Duration // zero selects DefaultAgentCallTimeout
	// ValidateResponse applies controller-specific acceptance criteria after a
	// response has passed its role schema. A rejection is a technical attempt,
	// not a new semantic operation.
	ValidateResponse func(AgentResponse) error
	// ContinueOnResponseRejection keeps a still-healthy provider session for a
	// controller follow-up. It is intentionally limited to a decoded response:
	// crashes, timeouts, malformed transport, and file-policy violations still
	// recreate the session before retrying.
	ContinueOnResponseRejection bool
}

// ControlledAgentCallResult is returned only for a response that passed both
// the file post-check and response binding. Attempts includes rejected
// technical attempts, making the caller's progress display deterministic.
type ControlledAgentCallResult struct {
	Response AgentResponse
	// Session is the live session that produced Response. A technical retry can
	// recreate a provider thread, so a controller that immediately continues a
	// successful conversation must use this value rather than its original
	// input pointer.
	Session *AgentSession
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
	session := call.Session
	var attempts uint64
	for {
		if err := ctx.Err(); err != nil {
			return ControlledAgentCallResult{Attempts: attempts}, errors.Join(ErrAgentCallCancelled, err)
		}
		attempt, err := reserveAgentAttempt(ctx, call)
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
			raw, turnErr = runBoundedAgentTurn(ctx, session, message, timeout)
			return turnErr
		})
		if err != nil {
			if outcomeErr := recordAgentAttemptOutcome(context.WithoutCancel(ctx), call, implementationstate.AttemptFailed, err.Error()); outcomeErr != nil {
				return ControlledAgentCallResult{Attempts: attempts}, errors.Join(err, outcomeErr)
			}
			return ControlledAgentCallResult{Attempts: attempts}, err
		}
		if outcome.Disposition == CallExecutionBlocked {
			if err := recordAgentAttemptOutcome(context.WithoutCancel(ctx), call, implementationstate.AttemptFailed, outcome.Diagnostic); err != nil {
				return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
			}
			return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, fmt.Errorf("%s", outcome.Diagnostic)
		}

		if errors.Is(outcome.InvocationError, ErrAgentCallCancelled) || errors.Is(outcome.InvocationError, context.Canceled) {
			if err := recordAgentAttemptOutcome(context.WithoutCancel(ctx), call, implementationstate.AttemptInterrupted, outcome.InvocationError.Error()); err != nil {
				return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
			}
			_ = session.Close()
			return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, outcome.InvocationError
		}
		if outcome.Disposition == CallRetry {
			if err := recordAgentAttemptOutcome(context.WithoutCancel(ctx), call, implementationstate.AttemptRejected, outcome.Diagnostic); err != nil {
				return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
			}
			session, err = recreateAgentSession(ctx, session)
			if err != nil {
				return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
			}
			message = retryPrompt(call.Message, outcome.Diagnostic)
			continue
		}
		if outcome.InvocationError != nil {
			if err := recordAgentAttemptOutcome(context.WithoutCancel(ctx), call, implementationstate.AttemptFailed, outcome.InvocationError.Error()); err != nil {
				return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
			}
			session, err = recreateAgentSession(ctx, session)
			if err != nil {
				return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
			}
			message = retryPrompt(call.Message, outcome.InvocationError.Error())
			continue
		}
		response, bindErr := BindAgentResponse(call.Expectation, raw)
		if bindErr != nil {
			if err := recordAgentAttemptOutcome(context.WithoutCancel(ctx), call, implementationstate.AttemptRejected, bindErr.Error()); err != nil {
				return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
			}
			session, err = recreateAgentSession(ctx, session)
			if err != nil {
				return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
			}
			message = retryPrompt(call.Message, bindErr.Error())
			continue
		}
		if call.ValidateResponse != nil {
			if validationErr := call.ValidateResponse(response); validationErr != nil {
				if err := recordAgentAttemptOutcome(context.WithoutCancel(ctx), call, implementationstate.AttemptRejected, validationErr.Error()); err != nil {
					return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
				}
				message = retryPrompt(call.Message, validationErr.Error())
				if !call.ContinueOnResponseRejection {
					session, err = recreateAgentSession(ctx, session)
					if err != nil {
						return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
					}
				}
				continue
			}
		}
		if err := recordAgentAttemptOutcome(context.WithoutCancel(ctx), call, implementationstate.AttemptSucceeded, ""); err != nil {
			return ControlledAgentCallResult{Snapshot: outcome.Snapshot, Attempts: attempts}, err
		}
		return ControlledAgentCallResult{Response: response, Session: session, Snapshot: outcome.Snapshot, Attempts: attempts}, nil
	}
}

func validateControlledAgentCall(call ControlledAgentCall) error {
	if call.Session == nil || call.Run == nil || call.Journal == nil || call.StateStore == nil || call.Repository == "" || call.OperationID == "" {
		return errors.New("controlled agent call requires session, repository, run, journal, state store, and operation ID")
	}
	if call.Expectation.Binding.CallID == "" || call.Policy.CallID != call.Expectation.Binding.CallID {
		return errors.New("controlled agent call policy and response binding must share a call ID")
	}
	if call.Session.Role != call.Expectation.Role {
		return errors.New("controlled agent call session and response expectation must share a role")
	}
	return nil
}

func reserveAgentAttempt(ctx context.Context, call ControlledAgentCall) (implementationstate.OperationAttempt, error) {
	if call.AssignmentID != "" {
		attempt, _, err := call.StateStore.RecordAssignmentAttemptStartWithLimits(ctx, call.Run, call.AssignmentID, call.OperationID, call.Limits)
		return attempt, err
	}
	attempt, _, err := call.StateStore.RecordRunAttemptStartWithLimits(ctx, call.Run, call.OperationID, call.Limits)
	return attempt, err
}

func recordAgentAttemptOutcome(ctx context.Context, call ControlledAgentCall, outcome implementationstate.AttemptOutcome, diagnostic string) error {
	if call.AssignmentID != "" {
		_, err := call.StateStore.RecordAssignmentAttemptOutcome(ctx, call.Run, call.AssignmentID, call.OperationID, outcome, diagnostic)
		return err
	}
	_, err := call.StateStore.RecordRunAttemptOutcome(ctx, call.Run, call.OperationID, outcome, diagnostic)
	return err
}

func recreateAgentSession(ctx context.Context, session *AgentSession) (*AgentSession, error) {
	next, err := session.Recreate(ctx)
	if err != nil {
		return nil, fmt.Errorf("recreate implementation agent session for technical retry: %w", err)
	}
	return next, nil
}

func retryPrompt(action, diagnostic string) string {
	return "The previous response was not accepted by the controller: " + diagnostic + ". Repeat the original requested action and return a valid structured response.\n\nOriginal requested action:\n" + action
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
