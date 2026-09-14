package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

var (
	ErrInvalidImplementerRoute = errors.New("invalid implementer check route")
	// ErrTaskReviewNotReady is the controller seam used by task review. It
	// deliberately describes the missing current required-check proof instead
	// of allowing a reviewer to infer readiness from an earlier narrow check.
	ErrTaskReviewNotReady = errors.New("task review requires current successful mandatory checks")
)

// ImplementerCheckRoute is the closed controller route from an executor
// checks_requested response, through configured checks, back to a following
// turn in that exact executor session. The agent never selects the check
// operation, the continuation call ID, or its session/thread.
type ImplementerCheckRoute struct {
	OriginatingCall ControlledAgentCall
	Transition      ImplementerTransitionInput
	Continuation    ControlledAgentCall
	// ContinuationTransition owns the distinct check operation/result used
	// when the feedback turn returns implementation_ready. It is deliberately
	// separate from Transition: a narrow requested set can never be reused as
	// evidence for the mandatory required set.
	ContinuationTransition ImplementerTransitionInput
	// FurtherContinuations make repeated checks_requested feedback cycles
	// finite and controller-owned. Each item supplies the next executor call
	// and its distinct check operation/result IDs; the configured request
	// counter remains the semantic upper bound.
	FurtherContinuations []ImplementerContinuation
}

type ImplementerContinuation struct {
	Call       ControlledAgentCall
	Transition ImplementerTransitionInput
}

// ImplementerCheckRouteResult keeps the initial response, durable check
// result, and continuation outcome. Feedback is suitable for the next agent
// turn and contains only bounded presentations and durable references.
type ImplementerCheckRouteResult struct {
	Response         AgentResponse
	ResponseAttempts uint64
	Checks           ImplementerTransitionResult
	RequiredChecks   ImplementerTransitionResult
	// ReviewReady is true only when RequiredChecks is a successful full set
	// for the current assignment state and acceptance inputs. The task-review
	// dispatcher is introduced separately; it must require this seam before
	// starting a reviewer session.
	ReviewReady          bool
	Feedback             string
	ContinuationResponse AgentResponse
	ContinuationAttempts uint64
	session              *AgentSession
}

// RouteImplementerChecks is the controller-owned executor dispatcher for the
// two task-9.1 responses. implementation_ready immediately starts a complete
// required set. A failed required set is fed back to the same live executor
// session, which may correct the code and present implementation_ready again.
// Every such presentation creates a distinct full set, so it starts from the
// first configured command; a narrow requested set is never reused as
// acceptance evidence. Mandatory-check attempt reservation enforces the
// configured per-cycle limit before a fourth external set could start.
//
// A successful required set is the only successful terminal result from this
// dispatcher. Review, acceptance, and commit remain outside this seam.
func RouteImplementerChecks(ctx context.Context, route ImplementerCheckRoute) (ImplementerCheckRouteResult, error) {
	if err := validateImplementerCheckRoute(route); err != nil {
		return ImplementerCheckRouteResult{}, err
	}
	currentCall := route.OriginatingCall
	currentTransition := route.Transition
	continuations := append([]ImplementerContinuation{{Call: route.Continuation, Transition: route.ContinuationTransition}}, route.FurtherContinuations...)
	seenCallIDs := map[string]bool{currentCall.Expectation.Binding.CallID: true}
	var result ImplementerCheckRouteResult
	for turn := 0; ; turn++ {
		if turn > 0 {
			if len(continuations) == 0 {
				return result, fmt.Errorf("%w: checks_requested has no controller-owned continuation capacity", ErrInvalidImplementerRoute)
			}
			next := continuations[0]
			continuations = continuations[1:]
			if err := validateImplementerContinuation(next.Call, next.Transition, route.OriginatingCall); err != nil {
				return result, err
			}
			if seenCallIDs[next.Call.Expectation.Binding.CallID] {
				return result, fmt.Errorf("%w: continuation call ID is reused", ErrInvalidImplementerRoute)
			}
			seenCallIDs[next.Call.Expectation.Binding.CallID] = true
			currentCall, currentTransition = next.Call, next.Transition
		}
		previousValidation := currentCall.ValidateResponse
		currentCall.ValidateResponse = implementerRouteResponseValidator(currentTransition, previousValidation)
		if turn > 0 {
			currentCall.Session = result.session
			currentCall.Message = result.Feedback
		}
		turnResult, err := InvokeControlledAgentCall(ctx, currentCall)
		if err != nil {
			if turn == 0 {
				result.ResponseAttempts = turnResult.Attempts
			} else {
				result.ContinuationAttempts = turnResult.Attempts
			}
			return result, err
		}
		checks, err := ApplyImplementerTransition(ctx, currentTransition, turnResult.Response)
		if turn == 0 {
			result.Response, result.ResponseAttempts, result.Checks = turnResult.Response, turnResult.Attempts, checks
		} else {
			result.ContinuationResponse, result.ContinuationAttempts = turnResult.Response, turnResult.Attempts
		}
		if err != nil {
			return result, err
		}
		if turnResult.Response.Kind == ResponseImplementationReady {
			result.RequiredChecks = checks
			if checks.Set.Succeeded() {
				if err := CanStartTaskReview(route.Transition.Run, route.Transition.AssignmentID); err != nil {
					return result, err
				}
				result.ReviewReady = true
				return result, nil
			}
			// A failed mandatory set is not a retry of one command. Its bounded
			// feedback turn gives the executor a chance to correct the code; the
			// next implementation_ready starts another whole required set.
			result.Feedback = ImplementerCheckFeedback(checks)
			result.session = turnResult.Session
			continue
		}
		result.Feedback = ImplementerCheckFeedback(checks)
		// Keep the live session that produced this turn for the next iteration.
		result.ContinuationResponse = turnResult.Response
		result.session = turnResult.Session
	}
}

// CanStartTaskReview proves that the latest mandatory-check result is a
// successful complete set for the current code, current specification and
// configuration, and the current brief. It is intentionally read-only: a
// later reviewer correction changes the observed code and the next executor
// implementation_ready must create a new mandatory-check operation.
func CanStartTaskReview(run *implementationstate.Run, assignmentID implementationstate.AssignmentID) error {
	if run == nil || run.Status != implementationstate.RunActive {
		return ErrTaskReviewNotReady
	}
	var assignment *implementationstate.Assignment
	for index := range run.Assignments {
		if run.Assignments[index].ID == assignmentID {
			assignment = &run.Assignments[index]
			break
		}
	}
	if assignment == nil || assignment.Status != implementationstate.AssignmentActive || len(assignment.Briefs) == 0 {
		return ErrTaskReviewNotReady
	}
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	currentBrief := assignment.Briefs[len(assignment.Briefs)-1].ID
	for resultIndex := len(assignment.Results) - 1; resultIndex >= 0; resultIndex-- {
		result := assignment.Results[resultIndex]
		var operation *implementationstate.Operation
		for operationIndex := range assignment.Operations {
			if assignment.Operations[operationIndex].ID == result.OperationID {
				operation = &assignment.Operations[operationIndex]
				break
			}
		}
		if operation == nil || operation.Counter != implementationstate.CycleCounterMandatoryChecks {
			continue
		}
		if result.Status == implementationstate.ResultSucceeded && result.State == run.CurrentState && result.Basis == basis && operation.Basis == basis && operation.BriefID == currentBrief {
			return nil
		}
		return ErrTaskReviewNotReady
	}
	return ErrTaskReviewNotReady
}

func validateImplementerCheckRoute(route ImplementerCheckRoute) error {
	origin := route.OriginatingCall
	if origin.Session == nil || origin.Session.Role != ResponseRoleImplementer {
		return fmt.Errorf("%w: originating executor session is required", ErrInvalidImplementerRoute)
	}
	if err := validateExpectation(origin.Expectation); err != nil {
		return fmt.Errorf("%w: originating expectation: %v", ErrInvalidImplementerRoute, err)
	}
	if origin.Expectation.Role != ResponseRoleImplementer || origin.Expectation.State != ResponseStateImplementing || origin.Expectation.Scope != ResponseScopeAssignment {
		return fmt.Errorf("%w: originating call must be assignment executor work", ErrInvalidImplementerRoute)
	}
	if origin.Policy.Role != AgentRoleExecutor || origin.Policy.CallID != origin.Expectation.Binding.CallID {
		return fmt.Errorf("%w: originating executor policy is not bound to its expectation", ErrInvalidImplementerRoute)
	}
	if err := validateImplementerTransitionInput(route.Transition); err != nil {
		return fmt.Errorf("%w: transition: %v", ErrInvalidImplementerRoute, err)
	}
	if origin.Run != route.Transition.Run || origin.Journal != route.Transition.Journal || origin.StateStore != route.Transition.StateStore || origin.AssignmentID != route.Transition.AssignmentID || !sameImplementerBinding(origin.Expectation.Binding, implementerTransitionBinding(route.Transition)) {
		return fmt.Errorf("%w: transition is not bound to the exact originating executor call", ErrInvalidImplementerRoute)
	}
	if !isImplementerAgentOperation(origin.Run, origin.AssignmentID, origin.OperationID, origin.Expectation.Binding.BriefID) {
		return fmt.Errorf("%w: originating call requires a dedicated executor operation", ErrInvalidImplementerRoute)
	}
	return nil
}

func validateImplementerContinuation(continuation ControlledAgentCall, transition ImplementerTransitionInput, origin ControlledAgentCall) error {
	if continuation.Session != nil && continuation.Session != origin.Session {
		return fmt.Errorf("%w: continuation must use the originating executor session", ErrInvalidImplementerRoute)
	}
	if err := validateExpectation(continuation.Expectation); err != nil {
		return fmt.Errorf("%w: continuation expectation: %v", ErrInvalidImplementerRoute, err)
	}
	if continuation.Expectation.Role != ResponseRoleImplementer || continuation.Expectation.State != ResponseStateImplementing || continuation.Expectation.Scope != ResponseScopeAssignment || continuation.Expectation.Binding.CallID == origin.Expectation.Binding.CallID || !sameImplementerBinding(continuation.Expectation.Binding, origin.Expectation.Binding) {
		return fmt.Errorf("%w: continuation does not preserve executor ownership", ErrInvalidImplementerRoute)
	}
	if continuation.Policy.Role != AgentRoleExecutor || continuation.Policy.CallID != continuation.Expectation.Binding.CallID || continuation.Run != origin.Run || continuation.Journal != origin.Journal || continuation.StateStore != origin.StateStore || continuation.AssignmentID != origin.AssignmentID || continuation.OperationID == "" {
		return fmt.Errorf("%w: continuation lacks controller-owned executor state", ErrInvalidImplementerRoute)
	}
	if !isImplementerAgentOperation(origin.Run, origin.AssignmentID, origin.OperationID, origin.Expectation.Binding.BriefID) || !isImplementerAgentOperation(origin.Run, origin.AssignmentID, continuation.OperationID, continuation.Expectation.Binding.BriefID) {
		return fmt.Errorf("%w: executor calls require dedicated executor operations", ErrInvalidImplementerRoute)
	}
	if err := validateImplementerTransitionInput(transition); err != nil {
		return fmt.Errorf("%w: continuation transition: %v", ErrInvalidImplementerRoute, err)
	}
	if transition.Run != origin.Run || transition.StateStore != origin.StateStore || transition.Journal != origin.Journal || !sameImplementerBinding(continuation.Expectation.Binding, implementerTransitionBinding(transition)) {
		return fmt.Errorf("%w: continuation transition is not bound to the continuation call", ErrInvalidImplementerRoute)
	}
	return nil
}

func implementerRouteResponseValidator(input ImplementerTransitionInput, previous func(AgentResponse) error) func(AgentResponse) error {
	return func(response AgentResponse) error {
		if response.Kind != ResponseChecksRequested && response.Kind != ResponseImplementationReady {
			return fmt.Errorf("%w: executor response must be checks_requested or implementation_ready", ErrInvalidImplementerRoute)
		}
		if err := ValidateImplementerTransitionResponse(input.Selection, input.Run, input.AssignmentID, input.BriefID, response); err != nil {
			return err
		}
		if previous != nil {
			return previous(response)
		}
		return nil
	}
}

func implementerTransitionBinding(input ImplementerTransitionInput) ResponseBinding {
	return ResponseBinding{
		CallID: "", RunID: input.Run.Identity.ID, AssignmentID: input.AssignmentID, BriefID: input.BriefID,
		Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList,
	}
}

func sameImplementerBinding(left, right ResponseBinding) bool {
	return left.RunID == right.RunID && left.AssignmentID == right.AssignmentID && left.BriefID == right.BriefID && left.Specification == right.Specification && left.Configuration == right.Configuration && left.TaskList == right.TaskList
}

func isImplementerAgentOperation(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID, briefID implementationstate.BriefID) bool {
	if run == nil {
		return false
	}
	for _, assignment := range run.Assignments {
		if assignment.ID != assignmentID {
			continue
		}
		for _, operation := range assignment.Operations {
			if operation.ID == operationID {
				return operation.Kind == implementationstate.OperationAgent && operation.Counter == implementationstate.CycleCounterNone && operation.BriefID == briefID
			}
		}
	}
	return false
}

// ImplementerCheckFeedback formats only safe, bounded check information for
// the executor continuation. It deliberately does not serialize CheckSet or
// CheckSetResult: those can contain command environment and raw output.
func ImplementerCheckFeedback(transition ImplementerTransitionResult) string {
	var text strings.Builder
	text.WriteString("# Configured check results\n")
	for _, check := range transition.Set.Results {
		fmt.Fprintf(&text, "\n## %s\n\n- Status: %s\n", check.Name, check.Status)
		if check.Presentation == nil {
			continue
		}
		presentation := check.Presentation
		fmt.Fprintf(&text, "- Command: %s\n- Exit code: %d\n- Duration: %s\n", presentation.Command, presentation.ExitCode, presentation.Duration)
		writeEvidenceReference(&text, "Checked state", presentation.CheckedState.Reference)
		writeEvidenceReference(&text, "Stdout log", presentation.Stdout.Reference)
		writeEvidenceReference(&text, "Stderr log", presentation.Stderr.Reference)
		if presentation.Diagnostics != "" {
			fmt.Fprintf(&text, "\n### Diagnostics\n\n%s\n", presentation.Diagnostics)
		}
	}
	return strings.TrimSpace(text.String())
}

func writeEvidenceReference(text *strings.Builder, label string, reference implementationstate.EvidenceRef) {
	fmt.Fprintf(text, "- %s: %s (sha256 %s)\n", label, reference.ID, reference.Digest)
}
