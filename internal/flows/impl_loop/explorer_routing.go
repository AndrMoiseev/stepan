package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
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
	// ExplorerResultID makes a completed auxiliary result recoverable. When it
	// is supplied, RouteExplorer reads the durable response instead of
	// dispatching Explorer again after a process restart. Empty preserves the
	// lower-level route seam for callers that retain recovery themselves.
	ExplorerResultID implementationstate.ResultID
	// SourceContinuation is the controller-owned next turn in the exact
	// source session. It has its own operation and response expectation;
	// Explorer's result never chooses either.
	SourceContinuation ControlledAgentCall
	// ExplorerCharacters is the effective configured response limit. Zero uses
	// the documented default; a negative value is invalid.
	ExplorerCharacters int
	// PersistExplorerResponse is called after the Explorer response has passed
	// controller validation and before it can influence the source session. A
	// route that crosses a durable workflow boundary uses this to make research
	// evidence replayable before dispatching the continuation.
	PersistExplorerResponse func(context.Context, ControlledAgentCallResult) error
}

// ExplorerRouteResult retains both the Explorer outcome and the structured
// response produced by the controller-owned continuation in the still-open
// source session.
type ExplorerRouteResult struct {
	Response                   AgentResponse
	ContinuationMessage        string
	Attempts                   uint64
	SourceContinuationResponse AgentResponse
	SourceContinuationAttempts uint64
	// SourceContinuationSnapshot is the controller observation made after the
	// continued source turn. Run-scoped routes use it to keep a final review
	// bound to the exact state accepted by its required checks.
	SourceContinuationSnapshot gitsnapshot.Snapshot
	Paused                     bool
	PauseReason                string
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
	limit, err := effectiveExplorerCharacterLimit(route.ExplorerCharacters)
	if err != nil {
		return ExplorerRouteResult{}, err
	}
	call := route.ExplorerCall
	var response AgentResponse
	var attempts uint64
	if route.ExplorerResultID != "" {
		recovery, recoveryErr := RecoverAgentOperation(call.Journal, call.Run, explorerAssignmentID(route.SourceExpectation), call.OperationID, route.ExplorerResultID)
		if recoveryErr != nil {
			return ExplorerRouteResult{}, recoveryErr
		}
		if recovery.State == AgentOperationCompleted {
			response, attempts = recovery.Response, recovery.Attempts
			if err := validateRecoveredExplorerResponse(call.Expectation, response, limit); err != nil {
				return ExplorerRouteResult{}, err
			}
		} else if route.PersistExplorerResponse != nil {
			// An artifact may have reached durable storage immediately before the
			// process stopped, while its owning result event did not. Recover and
			// link that exact immutable response before deciding whether Explorer
			// must be called again.
			published, found, publishedErr := recoverPublishedExplorerResponse(call.Journal, route.ExplorerResultID)
			if publishedErr != nil {
				return ExplorerRouteResult{}, publishedErr
			}
			if found {
				if err := validateRecoveredExplorerResponse(call.Expectation, published, limit); err != nil {
					return ExplorerRouteResult{}, err
				}
				if err := route.PersistExplorerResponse(context.WithoutCancel(ctx), ControlledAgentCallResult{Response: published, Attempts: recovery.Attempts}); err != nil {
					return ExplorerRouteResult{Response: published, Attempts: recovery.Attempts}, err
				}
				response, attempts = published, recovery.Attempts
			}
		}
	}
	if response.Kind == "" {
		session, startErr := route.Owner.Explorer(ctx, start)
		if startErr != nil {
			return ExplorerRouteResult{}, fmt.Errorf("start Explorer session: %w", startErr)
		}
		defer func() { _ = session.Close() }()
		call.Session = session
		call.AssignmentID = explorerAssignmentID(route.SourceExpectation)
		call.ValidateResponse = func(value AgentResponse) error { return validateExplorerResponseSize(value, limit) }
		call.ContinueOnResponseRejection = true
		result, invokeErr := InvokeControlledAgentCall(ctx, call)
		if invokeErr != nil {
			return ExplorerRouteResult{Attempts: result.Attempts}, invokeErr
		}
		response, attempts = result.Response, result.Attempts
		if route.PersistExplorerResponse != nil {
			if persistErr := route.PersistExplorerResponse(context.WithoutCancel(ctx), result); persistErr != nil {
				return ExplorerRouteResult{Response: response, Attempts: attempts}, persistErr
			}
		}
	}
	if response.Kind == ResponseExecutionBlocked {
		if call.Run.Status == implementationstate.RunPaused {
			return ExplorerRouteResult{Response: response, Attempts: attempts, Paused: true, PauseReason: call.Run.PauseReason}, nil
		}
		reason, err := persistExplorerExecutionBlocked(ctx, call, response)
		if err != nil {
			return ExplorerRouteResult{Response: response, Attempts: attempts}, err
		}
		return ExplorerRouteResult{Response: response, Attempts: attempts, Paused: true, PauseReason: reason}, nil
	}
	if err := validateSourceContinuation(route); err != nil {
		return ExplorerRouteResult{Response: response, Attempts: attempts}, err
	}
	continuation := explorerContinuation(response)
	sourceCall := route.SourceContinuation
	sourceCall.Session = route.SourceSession
	sourceCall.Message = continuation
	sourceResult, err := InvokeControlledAgentCall(ctx, sourceCall)
	if err != nil {
		return ExplorerRouteResult{Response: response, ContinuationMessage: continuation, Attempts: attempts, SourceContinuationAttempts: sourceResult.Attempts}, err
	}
	return ExplorerRouteResult{
		Response: response, Attempts: attempts,
		ContinuationMessage:        explorerContinuation(response),
		SourceContinuationResponse: sourceResult.Response, SourceContinuationAttempts: sourceResult.Attempts,
		SourceContinuationSnapshot: sourceResult.Snapshot,
	}, nil
}

func recoverPublishedExplorerResponse(journal *runstore.Run, resultID implementationstate.ResultID) (AgentResponse, bool, error) {
	reference, err := journal.PublishedReference(implementationstate.EvidenceID(string(resultID) + "-response"))
	if errors.Is(err, runstore.ErrReferenceUnavailable) {
		return AgentResponse{}, false, nil
	}
	if err != nil {
		return AgentResponse{}, false, err
	}
	data, err := journal.Read(reference)
	if err != nil {
		return AgentResponse{}, false, err
	}
	var response AgentResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return AgentResponse{}, false, fmt.Errorf("decode published Explorer response: %w", err)
	}
	return response, true, nil
}

func validateRecoveredExplorerResponse(expectation ResponseExpectation, response AgentResponse, limit int) error {
	if err := validateExpectation(expectation); err != nil {
		return fmt.Errorf("validate recovered Explorer expectation: %w", err)
	}
	if response.Binding != expectation.Binding || !slices.Contains(responseKindsByRole[expectation.Role], response.Kind) || !responseAllowedInState(response.Kind, expectation) {
		return fmt.Errorf("recovered Explorer response is not bound to its durable request")
	}
	if err := validateResponseSemantics(response); err != nil {
		return fmt.Errorf("validate recovered Explorer response semantics: %w", err)
	}
	if err := validateExplorerResponseSize(response, limit); err != nil {
		return fmt.Errorf("validate recovered Explorer response: %w", err)
	}
	return nil
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

func persistExplorerExecutionBlocked(ctx context.Context, call ControlledAgentCall, response AgentResponse) (string, error) {
	block, err := ExecutionBlockFromResponse(response)
	if err != nil {
		return "", err
	}
	event, err := implementationstate.NewRunStateEvent(1, call.Run)
	if err != nil {
		return "", fmt.Errorf("clone Explorer execution-blocked state: %w", err)
	}
	candidate := *event.State
	if err := candidate.PauseExecutionBlocked(block); err != nil {
		return "", fmt.Errorf("pause Explorer execution-blocked run: %w", err)
	}
	written, err := call.StateStore.Record(ctx, &candidate)
	if written.Sequence != 0 {
		*call.Run = candidate
	}
	if err != nil {
		return "", fmt.Errorf("persist Explorer execution-blocked pause: %w", err)
	}
	return candidate.PauseReason, nil
}

func validateSourceContinuation(route ExplorerRoute) error {
	call := route.SourceContinuation
	if call.Session != nil && call.Session != route.SourceSession {
		return fmt.Errorf("%w: source continuation must use the originating session", ErrInvalidExplorerRoute)
	}
	if err := validateExpectation(call.Expectation); err != nil {
		return fmt.Errorf("%w: source continuation expectation: %v", ErrInvalidExplorerRoute, err)
	}
	if call.Expectation.Role != route.SourceExpectation.Role || call.Expectation.State != route.SourceExpectation.State || call.Expectation.Scope != route.SourceExpectation.Scope || call.Expectation.ExplorerSource != "" || !sameExplorerBinding(call.Expectation.Binding, route.SourceExpectation.Binding) {
		return fmt.Errorf("%w: source continuation does not preserve the source ownership", ErrInvalidExplorerRoute)
	}
	if call.Run != route.ExplorerCall.Run || call.Journal != route.ExplorerCall.Journal || call.StateStore != route.ExplorerCall.StateStore || call.AssignmentID != explorerAssignmentID(route.SourceExpectation) || call.OperationID == "" {
		return fmt.Errorf("%w: source continuation requires the same durable source state and its own operation", ErrInvalidExplorerRoute)
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

func effectiveExplorerCharacterLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultExplorerResponseCharacters, nil
	}
	if limit < 0 {
		return 0, fmt.Errorf("%w: Explorer character limit must be positive", ErrInvalidExplorerRoute)
	}
	return limit, nil
}

func validateExplorerResponseSize(response AgentResponse, limit int) error {
	// Clarification and execution-blocked are valid Explorer outcomes. They
	// are delivered to the source as an escalation, not retried as a malformed
	// or oversized research answer.
	if response.Kind != ResponseExplorationResult {
		return nil
	}
	text := explorerContinuation(response)
	if utf8.RuneCountInString(text) > limit {
		return fmt.Errorf("Explorer response exceeds %d Unicode characters; shorten the same research answer without omitting confirmed facts, unknowns, or references", limit)
	}
	return nil
}

func explorerContinuation(response AgentResponse) string {
	var text strings.Builder
	switch response.Kind {
	case ResponseExplorationResult:
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
	case ResponseClarificationNeeded:
		fmt.Fprintf(&text, "# Explorer requires clarification\n\n## Question\n\n%s\n\n## Context\n\n%s\n\n## Boundaries\n\n%s\n\n## References\n", *response.Question, *response.Context, *response.Boundaries)
		for _, reference := range response.References {
			fmt.Fprintf(&text, "- %s\n", reference)
		}
		if response.Recommendation != nil {
			fmt.Fprintf(&text, "\n## Recommendation\n\n%s\n", *response.Recommendation)
		}
	case ResponseExecutionBlocked:
		fmt.Fprintf(&text, "# Explorer execution blocked\n\n## Blocked action\n\n%s\n\n## Diagnostic\n\n%s\n\n## Attempts\n", *response.BlockedAction, *response.Diagnostic)
		for _, attempt := range response.Attempts {
			fmt.Fprintf(&text, "- %s\n", attempt)
		}
		fmt.Fprintf(&text, "\n## Required user action\n\n%s\n", *response.RequiredUserAction)
	}
	return strings.TrimSpace(text.String())
}
