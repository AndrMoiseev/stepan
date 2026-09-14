package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

// DefaultExplorerResponseCharacters is a Unicode-character limit, not a byte
// limit. Explorer must ask itself to shorten an overlong answer; the
// controller never silently truncates research evidence.
const DefaultExplorerResponseCharacters = 12000

var ErrInvalidExplorerRoute = errors.New("invalid Explorer route")

// ExplorerRoute is the controller-owned data needed to serve an already
// validated exploration_requested response. ExplorerCall must contain a new
// controller CallID and an Explorer response expectation; the route fixes all
// remaining Explorer details so the source agent cannot choose them.
type ExplorerRoute struct {
	Owner             *SessionOwner
	SourceSession     *AgentSession
	SourceExpectation ResponseExpectation
	Request           AgentResponse
	ExplorerCall      ControlledAgentCall
}

// ExplorerRouteResult is fed to the still-open source session by the next
// controller turn. The source session is intentionally not run here: its next
// expected state and operation remain a controller decision.
type ExplorerRouteResult struct {
	Response            AgentResponse
	ContinuationMessage string
	Attempts            uint64
}

// RouteExplorer starts one fresh Explorer session, preserves the source
// episode's durable counter, and returns the research result for the original
// source session. Explorer cannot request another Explorer because its schema
// and response-state binding admit exploration_result only.
func RouteExplorer(ctx context.Context, route ExplorerRoute) (ExplorerRouteResult, error) {
	if err := validateExplorerRoute(route); err != nil {
		return ExplorerRouteResult{}, err
	}
	start, err := BuildExplorerStartContext(ExplorerStartInput{
		Question: *route.Request.Question, Context: *route.Request.Context,
		Boundaries: *route.Request.Boundaries, KnownFacts: route.Request.KnownFacts,
	})
	if err != nil {
		return ExplorerRouteResult{}, err
	}
	session, err := route.Owner.Explorer(ctx, start)
	if err != nil {
		return ExplorerRouteResult{}, fmt.Errorf("start Explorer session: %w", err)
	}
	defer func() { _ = session.Close() }()

	call := route.ExplorerCall
	call.Session = session
	call.AssignmentID = explorerAssignmentID(route.SourceExpectation)
	call.ValidateResponse = validateExplorerResponseSize
	call.ContinueOnResponseRejection = true
	result, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return ExplorerRouteResult{Attempts: result.Attempts}, err
	}
	return ExplorerRouteResult{
		Response: result.Response, Attempts: result.Attempts,
		ContinuationMessage: explorerContinuation(result.Response),
	}, nil
}

func validateExplorerRoute(route ExplorerRoute) error {
	if route.Owner == nil || route.SourceSession == nil {
		return fmt.Errorf("%w: owner and source session are required", ErrInvalidExplorerRoute)
	}
	if err := validateExpectation(route.SourceExpectation); err != nil {
		return fmt.Errorf("%w: source expectation: %v", ErrInvalidExplorerRoute, err)
	}
	if route.SourceSession.Role != route.SourceExpectation.Role || route.Request.Kind != ResponseExplorationRequested {
		return fmt.Errorf("%w: source session and request must be an exploration request for the source role", ErrInvalidExplorerRoute)
	}
	if !responseAllowedInState(ResponseExplorationRequested, route.SourceExpectation) {
		return fmt.Errorf("%w: source role cannot request Explorer", ErrInvalidExplorerRoute)
	}
	if err := validateExplorerRequest(route.Request); err != nil {
		return err
	}
	if route.Request.Binding != route.SourceExpectation.Binding {
		return fmt.Errorf("%w: exploration request is not bound to its source", ErrInvalidExplorerRoute)
	}
	expectation := route.ExplorerCall.Expectation
	if err := validateExpectation(expectation); err != nil {
		return fmt.Errorf("%w: Explorer expectation: %v", ErrInvalidExplorerRoute, err)
	}
	if expectation.Role != ResponseRoleExplorer || expectation.State != ResponseStateExploring || expectation.ExplorerSource != explorerSourceFor(route.SourceExpectation) {
		return fmt.Errorf("%w: Explorer expectation does not bind the requesting source", ErrInvalidExplorerRoute)
	}
	if expectation.Scope != route.SourceExpectation.Scope || !sameExplorerBinding(expectation.Binding, route.SourceExpectation.Binding) {
		return fmt.Errorf("%w: Explorer expectation does not preserve source ownership", ErrInvalidExplorerRoute)
	}
	if route.ExplorerCall.Policy.Role != AgentRoleExplorer || route.ExplorerCall.Policy.AllowUnprotected || len(route.ExplorerCall.Policy.AllowedPaths) != 0 || len(route.ExplorerCall.Policy.AllowedRoots) != 0 {
		return fmt.Errorf("%w: Explorer must use a read-only Explorer call policy", ErrInvalidExplorerRoute)
	}
	if route.ExplorerCall.Policy.CallID != expectation.Binding.CallID {
		return fmt.Errorf("%w: Explorer policy and response binding must share a call ID", ErrInvalidExplorerRoute)
	}
	if route.ExplorerCall.Run == nil || route.ExplorerCall.Journal == nil || route.ExplorerCall.StateStore == nil {
		return fmt.Errorf("%w: Explorer requires durable run state", ErrInvalidExplorerRoute)
	}
	if route.ExplorerCall.OperationID == "" || route.ExplorerCall.Limits.Explorer <= 0 {
		return fmt.Errorf("%w: Explorer operation and positive exploration limit are required", ErrInvalidExplorerRoute)
	}
	if !isExplorerOperation(route.ExplorerCall.Run, explorerAssignmentID(route.SourceExpectation), route.ExplorerCall.OperationID) {
		return fmt.Errorf("%w: Explorer must use a dedicated Explorer operation", ErrInvalidExplorerRoute)
	}
	if route.SourceExpectation.Scope == ResponseScopeBootstrap {
		return fmt.Errorf("%w: bootstrap Explorer routing requires bootstrap durable state", ErrInvalidExplorerRoute)
	}
	return nil
}

func isExplorerOperation(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID) bool {
	if assignmentID == "" {
		for _, operation := range run.RunOperations {
			if operation.ID == operationID {
				return operation.Kind == implementationstate.OperationAgent && operation.Counter == implementationstate.CycleCounterExplorer && strings.TrimSpace(operation.Episode) != ""
			}
		}
		return false
	}
	for _, assignment := range run.Assignments {
		if assignment.ID != assignmentID {
			continue
		}
		for _, operation := range assignment.Operations {
			if operation.ID == operationID {
				return operation.Kind == implementationstate.OperationAgent && operation.Counter == implementationstate.CycleCounterExplorer && strings.TrimSpace(operation.Episode) != ""
			}
		}
	}
	return false
}

func validateExplorerRequest(response AgentResponse) error {
	if response.Question == nil || response.Context == nil || response.Boundaries == nil || len(response.KnownFacts) == 0 {
		return fmt.Errorf("%w: question, context, boundaries, and known facts are required", ErrInvalidExplorerRoute)
	}
	for _, text := range append([]string{*response.Question, *response.Context, *response.Boundaries}, response.KnownFacts...) {
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("%w: Explorer request fields must not be empty", ErrInvalidExplorerRoute)
		}
	}
	return nil
}

func explorerSourceFor(expectation ResponseExpectation) ExplorerSource {
	switch expectation.Role {
	case ResponseRoleBriefer:
		if expectation.State == ResponseStateInitialBriefing {
			return ExplorerSourceInitialBriefing
		}
		return ExplorerSourceBriefRefinement
	case ResponseRoleImplementer:
		return ExplorerSourceImplementer
	case ResponseRoleTaskReviewer:
		return ExplorerSourceTaskReviewer
	case ResponseRoleFinalReviewer:
		return ExplorerSourceFinalReviewer
	case ResponseRoleBootstrapper:
		return ExplorerSourceBootstrapper
	default:
		return ""
	}
}

func explorerAssignmentID(expectation ResponseExpectation) implementationstate.AssignmentID {
	if expectation.Scope == ResponseScopeAssignment {
		return expectation.Binding.AssignmentID
	}
	return ""
}

func sameExplorerBinding(explorer, source ResponseBinding) bool {
	return explorer.RunID == source.RunID && explorer.AssignmentID == source.AssignmentID && explorer.BriefID == source.BriefID && explorer.Specification == source.Specification && explorer.Configuration == source.Configuration && explorer.TaskList == source.TaskList
}

func validateExplorerResponseSize(response AgentResponse) error {
	if response.Message == nil {
		return fmt.Errorf("Explorer response has no message")
	}
	if utf8.RuneCountInString(*response.Message) > DefaultExplorerResponseCharacters {
		return fmt.Errorf("Explorer response exceeds %d Unicode characters; shorten the same research answer without omitting confirmed facts, unknowns, or references", DefaultExplorerResponseCharacters)
	}
	return nil
}

func explorerContinuation(response AgentResponse) string {
	var text strings.Builder
	text.WriteString("# Explorer result\n\n")
	text.WriteString(*response.Message)
	text.WriteString("\n\n## Confirmed facts\n")
	for _, fact := range response.KnownFacts {
		fmt.Fprintf(&text, "- %s\n", fact)
	}
	text.WriteString("\n## Unknowns\n")
	for _, unknown := range response.Unknowns {
		fmt.Fprintf(&text, "- %s\n", unknown)
	}
	text.WriteString("\n## References\n")
	for _, reference := range response.References {
		fmt.Fprintf(&text, "- %s\n", reference)
	}
	return strings.TrimSpace(text.String())
}
