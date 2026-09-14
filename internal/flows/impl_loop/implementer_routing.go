package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

var ErrInvalidImplementerRoute = errors.New("invalid implementer check route")

// ImplementerCheckRoute is the closed controller route from an executor
// checks_requested response, through configured checks, back to a following
// turn in that exact executor session. The agent never selects the check
// operation, the continuation call ID, or its session/thread.
type ImplementerCheckRoute struct {
	OriginatingCall ControlledAgentCall
	Transition      ImplementerTransitionInput
	Continuation    ControlledAgentCall
}

// ImplementerCheckRouteResult keeps the initial response, durable check
// result, and continuation outcome. Feedback is suitable for the next agent
// turn and contains only bounded presentations and durable references.
type ImplementerCheckRouteResult struct {
	Response             AgentResponse
	ResponseAttempts     uint64
	Checks               ImplementerTransitionResult
	Feedback             string
	ContinuationResponse AgentResponse
	ContinuationAttempts uint64
}

// RouteImplementerChecks dispatches the originating executor turn, validates
// that its response is bound to that exact call, runs controller-selected
// checks, then dispatches the controller-owned continuation on the same
// AgentSession. implementation_ready is intentionally not routed here: it
// begins mandatory acceptance, whose correction/review path belongs to later
// lifecycle work.
func RouteImplementerChecks(ctx context.Context, route ImplementerCheckRoute) (ImplementerCheckRouteResult, error) {
	if err := validateImplementerCheckRoute(route); err != nil {
		return ImplementerCheckRouteResult{}, err
	}
	origin := route.OriginatingCall
	previousValidation := origin.ValidateResponse
	origin.ValidateResponse = func(response AgentResponse) error {
		if response.Kind != ResponseChecksRequested {
			return fmt.Errorf("%w: originating executor response must be checks_requested", ErrInvalidImplementerRoute)
		}
		if err := ValidateImplementerTransitionResponse(route.Transition.Selection, route.Transition.Run, route.Transition.AssignmentID, route.Transition.BriefID, response); err != nil {
			return err
		}
		if previousValidation != nil {
			return previousValidation(response)
		}
		return nil
	}

	originResult, err := InvokeControlledAgentCall(ctx, origin)
	if err != nil {
		return ImplementerCheckRouteResult{ResponseAttempts: originResult.Attempts}, err
	}
	checks, err := ApplyImplementerTransition(ctx, route.Transition, originResult.Response)
	if err != nil {
		return ImplementerCheckRouteResult{Response: originResult.Response, ResponseAttempts: originResult.Attempts}, err
	}
	feedback := ImplementerCheckFeedback(checks)
	continuation := route.Continuation
	continuation.Session = originResult.Session
	continuation.Message = feedback
	continuationResult, err := InvokeControlledAgentCall(ctx, continuation)
	if err != nil {
		return ImplementerCheckRouteResult{Response: originResult.Response, ResponseAttempts: originResult.Attempts, Checks: checks, Feedback: feedback, ContinuationAttempts: continuationResult.Attempts}, err
	}
	return ImplementerCheckRouteResult{
		Response: originResult.Response, ResponseAttempts: originResult.Attempts,
		Checks: checks, Feedback: feedback,
		ContinuationResponse: continuationResult.Response, ContinuationAttempts: continuationResult.Attempts,
	}, nil
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

	continuation := route.Continuation
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
	return nil
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
