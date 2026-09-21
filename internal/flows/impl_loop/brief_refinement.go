package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

var ErrBriefRefinement = errors.New("invalid assignment brief refinement")

// recordBriefRefinementState is a narrow seam for preserving the durable-event
// boundary under tests; production always delegates directly to StateStore.
var recordBriefRefinementState = func(ctx context.Context, store *runstore.StateStore, state *implstate.Run) (implstate.Event, error) {
	return store.Record(ctx, state)
}

type BriefRefinementInput struct {
	Owner         *SessionOwner
	Workspace     WorkspaceControl
	Run           *implstate.Run
	StateStore    *runstore.StateStore
	Journal       *runstore.Run
	Repository    string
	AssignmentID  implstate.AssignmentID
	RequesterRole ResponseRole
	Request       AgentResponse
	OperationID   implstate.OperationID
	ResultID      implstate.ResultID
	CallID        string
	Limits        implstate.CycleLimits
	Timeout       time.Duration
	Explorer      *BriefRefinementExplorer
}

// BriefRefinementExplorer contains controller-allocated identities for one
// Explorer episode and the continuing turn in the same briefer session.
type BriefRefinementExplorer struct {
	ExplorerOperationID     implstate.OperationID
	ExplorerResultID        implstate.ResultID
	ExplorerCallID          string
	ContinuationOperationID implstate.OperationID
	ContinuationResultID    implstate.ResultID
	ExplorerCharacters      int
	// Additional holds controller-allocated episodes for a consecutive
	// exploration request from the continuing briefer turn.
	Additional []BriefRefinementExplorer
}

type BriefRefinementResult struct {
	Call        ControlledAgentCallResult
	Brief       *implstate.BriefVersion
	Closed      bool
	Paused      bool
	PauseReason string
}

// RefineBrief persists each accepted response as an operation result before it
// changes the brief or run lifecycle. The fixed result ID makes a repeated
// call after an event/projection failure return its durable outcome instead of
// asking the briefer to decide again.
func RefineBrief(ctx context.Context, input BriefRefinementInput) (BriefRefinementResult, error) {
	if err := validateBriefRefinementInput(input); err != nil {
		return BriefRefinementResult{}, err
	}
	if recovered, ok, err := recoveredBriefRefinement(input); err != nil || ok {
		return recovered, err
	}
	brief, err := currentAssignmentBrief(input.Journal, input.Run, input.AssignmentID)
	if err != nil {
		return BriefRefinementResult{}, fmt.Errorf("%w: current brief: %v", ErrBriefRefinement, err)
	}
	if err := validateBriefRefinementRequest(input.Run, input.AssignmentID, brief.ID, input.Request); err != nil {
		return BriefRefinementResult{}, err
	}
	session, err := input.Owner.ExistingBriefer(input.AssignmentID)
	if errors.Is(err, ErrSessionMissing) && refinementNeedsFreshSession(input.Run, input.AssignmentID, input.OperationID) {
		start, startErr := BuildBrieferStartContext(input.Journal, input.Run, input.AssignmentID)
		if startErr != nil {
			return BriefRefinementResult{}, fmt.Errorf("%w: rebuild briefer context: %v", ErrBriefRefinement, startErr)
		}
		session, err = input.Owner.Briefer(ctx, input.AssignmentID, start)
	}
	if err != nil {
		return BriefRefinementResult{}, fmt.Errorf("%w: existing briefer session: %v", ErrBriefRefinement, err)
	}
	if err := prepareBriefRefinementOperation(ctx, input, brief.ID); err != nil {
		return BriefRefinementResult{}, err
	}
	diff, err := effectiveWorkspaceControl(input.Workspace).AssignmentDiff(ctx, input.Repository, assignmentDiffBase(input.Run, input.AssignmentID))
	if err != nil {
		return BriefRefinementResult{}, err
	}
	binding := briefRefinementBinding(input, brief.ID, input.CallID)
	call := briefRefinementCall(input, session, input.OperationID, binding, refinementMessage(brief.Text, diff, input.Request))
	turn, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return BriefRefinementResult{Call: turn}, err
	}
	if turn.Response.Kind == ResponseExplorationRequested {
		if _, err := persistBriefRefinementOutcome(ctx, input, brief.ID, input.OperationID, input.ResultID, turn.Response); err != nil {
			return BriefRefinementResult{Call: turn}, err
		}
		return routeBriefRefinementExplorer(ctx, input, brief, turn, call)
	}
	result, err := persistBriefRefinementOutcome(ctx, input, brief.ID, input.OperationID, input.ResultID, turn.Response)
	result.Call = turn
	return result, err
}

func refinementNeedsFreshSession(run *implstate.Run, assignmentID implstate.AssignmentID, operationID implstate.OperationID) bool {
	operation := assignmentOperation(run, assignmentID, operationID)
	if operation == nil || len(operation.Attempts) == 0 {
		return false
	}
	return operation.Attempts[len(operation.Attempts)-1].Outcome != implstate.AttemptSucceeded
}

func validateBriefRefinementInput(input BriefRefinementInput) error {
	if input.Owner == nil || input.Run == nil || input.StateStore == nil || input.Journal == nil || strings.TrimSpace(input.Repository) == "" || input.AssignmentID == "" || input.OperationID == "" || input.ResultID == "" || strings.TrimSpace(input.CallID) == "" || !validTransitionLimits(input.Limits) {
		return fmt.Errorf("%w: owner, durable state, assignment, operation, result, call ID, and limits are required", ErrBriefRefinement)
	}
	if input.RequesterRole != ResponseRoleImplementer && input.RequesterRole != ResponseRoleTaskReviewer {
		return fmt.Errorf("%w: only executor or task reviewer may request brief refinement", ErrBriefRefinement)
	}
	return nil
}

func validateBriefRefinementRequest(run *implstate.Run, assignmentID implstate.AssignmentID, briefID implstate.BriefID, request AgentResponse) error {
	if request.Kind != ResponseClarificationNeeded || request.Binding.RunID != run.Identity.ID || request.Binding.AssignmentID != assignmentID || request.Binding.BriefID != briefID || request.Binding.Specification != run.Identity.Specification || request.Binding.Configuration != run.Identity.Configuration || request.Binding.TaskList != run.Identity.TaskList || request.Question == nil || request.Context == nil || request.Boundaries == nil || len(request.References) == 0 {
		return fmt.Errorf("%w: clarification request is not bound to the current brief", ErrBriefRefinement)
	}
	return nil
}

func prepareBriefRefinementOperation(ctx context.Context, input BriefRefinementInput, briefID implstate.BriefID) error {
	if operation := assignmentOperation(input.Run, input.AssignmentID, input.OperationID); operation != nil {
		if operation.Kind != implstate.OperationAgent || operation.Counter != implstate.CycleCounterBriefRefinement || operation.BriefID != briefID {
			return fmt.Errorf("%w: existing refinement operation has a different binding", ErrBriefRefinement)
		}
		return nil
	}
	candidate, err := cloneBriefRefinementRun(input.Run)
	if err != nil {
		return err
	}
	if err := candidate.BeginBriefRefinement(input.AssignmentID); err != nil {
		return fmt.Errorf("%w: reopen assignment: %v", ErrBriefRefinement, err)
	}
	basis := implstate.AcceptanceBasis{Specification: candidate.Identity.Specification, Configuration: candidate.Identity.Configuration}
	if err := candidate.AddOperation(input.AssignmentID, implstate.Operation{ID: input.OperationID, Kind: implstate.OperationAgent, BriefID: briefID, Basis: basis, Description: "refine assignment brief", Counter: implstate.CycleCounterBriefRefinement}); err != nil {
		return fmt.Errorf("%w: create refinement operation: %v", ErrBriefRefinement, err)
	}
	return persistBriefRefinementCandidate(ctx, input.StateStore, input.Run, candidate)
}

func briefRefinementCall(input BriefRefinementInput, session *AgentSession, operationID implstate.OperationID, binding ResponseBinding, message string) ControlledAgentCall {
	call := ControlledAgentCall{Session: session, Repository: input.Repository, Workspace: input.Workspace, Policy: AgentCallPolicy{Role: AgentRoleBriefer, CallID: binding.CallID}, Run: input.Run, Journal: input.Journal, StateStore: input.StateStore, AssignmentID: input.AssignmentID, OperationID: operationID, Limits: input.Limits, Expectation: ResponseExpectation{Role: ResponseRoleBriefer, State: ResponseStateBriefRefinement, Scope: ResponseScopeAssignment, Binding: binding}, Message: message, Timeout: input.Timeout}
	call.ValidateResponse = func(response AgentResponse) error {
		switch response.Kind {
		case ResponseClarificationNeeded, ResponseExplorationRequested, ResponseExecutionBlocked:
			return nil
		case ResponseBriefReady:
			if response.Binding == binding && response.Brief != nil && sameTaskIDs(input.Run, input.AssignmentID, response.TaskIDs) {
				return nil
			}
		}
		return fmt.Errorf("%w: briefer response cannot safely refine the current assignment", ErrBriefRefinement)
	}
	return call
}

func persistBriefRefinementOutcome(ctx context.Context, input BriefRefinementInput, priorBriefID implstate.BriefID, operationID implstate.OperationID, resultID implstate.ResultID, response AgentResponse) (BriefRefinementResult, error) {
	if existing := assignmentResult(input.Run, input.AssignmentID, resultID); existing != nil {
		return recoveredBriefRefinementResult(input, *existing)
	}
	evidence, err := publishBriefRefinementResponse(input.Journal, resultID, response)
	if err != nil {
		return BriefRefinementResult{}, err
	}
	candidate, err := cloneBriefRefinementRun(input.Run)
	if err != nil {
		return BriefRefinementResult{}, err
	}
	operation := assignmentOperation(candidate, input.AssignmentID, operationID)
	if operation == nil || operation.BriefID != priorBriefID {
		return BriefRefinementResult{}, fmt.Errorf("%w: outcome lacks its original refinement operation", ErrBriefRefinement)
	}
	result := implstate.OperationResult{ID: resultID, OperationID: operationID, Status: implstate.ResultSucceeded, State: candidate.CurrentState, Basis: operation.Basis, Evidence: []implstate.EvidenceRef{evidence}}
	var published *implstate.BriefVersion
	switch response.Kind {
	case ResponseBriefReady:
		brief, document, err := publishRefinedBriefCandidate(input.Journal, candidate, input.AssignmentID, response)
		if err != nil {
			return BriefRefinementResult{}, err
		}
		result.Evidence, published = append(result.Evidence, document), &brief
	case ResponseClarificationNeeded:
	case ResponseExecutionBlocked:
		result.Status = implstate.ResultFailed
	case ResponseExplorationRequested:
	default:
		return BriefRefinementResult{}, fmt.Errorf("%w: unsupported persisted briefer response %q", ErrBriefRefinement, response.Kind)
	}
	if err := candidate.AddResult(input.AssignmentID, result); err != nil {
		return BriefRefinementResult{}, err
	}
	if response.Kind == ResponseClarificationNeeded {
		if err := candidate.Close(briefClarificationCloseReason(response)); err != nil {
			return BriefRefinementResult{}, err
		}
	}
	if response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(response)
		if err != nil {
			return BriefRefinementResult{}, err
		}
		if err := candidate.PauseExecutionBlocked(block); err != nil {
			return BriefRefinementResult{}, err
		}
	}
	if err := persistBriefRefinementCandidate(context.WithoutCancel(ctx), input.StateStore, input.Run, candidate); err != nil {
		return BriefRefinementResult{}, err
	}
	return BriefRefinementResult{Brief: published, Closed: response.Kind == ResponseClarificationNeeded, Paused: response.Kind == ResponseExecutionBlocked, PauseReason: candidate.PauseReason}, nil
}

func publishRefinedBriefCandidate(journal *runstore.Run, run *implstate.Run, assignmentID implstate.AssignmentID, response AgentResponse) (implstate.BriefVersion, implstate.EvidenceRef, error) {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil || !sameTaskIDs(run, assignmentID, response.TaskIDs) || response.Brief == nil {
		return implstate.BriefVersion{}, implstate.EvidenceRef{}, fmt.Errorf("%w: refined brief changes the assignment", ErrBriefRefinement)
	}
	number := len(assignment.Briefs) + 1
	briefID := implstate.BriefID(fmt.Sprintf("brief-%s-v%d", assignmentID, number))
	documentID := implstate.EvidenceID(fmt.Sprintf("brief-%s-v%d.md", assignmentID, number))
	document, err := journal.Publish(documentID, renderBriefDocument(assignmentID, number, assignment.TaskIDs, *response.Brief))
	if err != nil {
		return implstate.BriefVersion{}, implstate.EvidenceRef{}, err
	}
	if err := journal.VerifyReference(document); err != nil {
		return implstate.BriefVersion{}, implstate.EvidenceRef{}, err
	}
	brief := implstate.BriefVersion{ID: briefID, Number: number, Document: document}
	if err := run.AddBriefVersion(assignmentID, brief); err != nil {
		return implstate.BriefVersion{}, implstate.EvidenceRef{}, err
	}
	return brief, document, nil
}

func publishBriefRefinementResponse(journal *runstore.Run, resultID implstate.ResultID, response AgentResponse) (implstate.EvidenceRef, error) {
	data, err := json.Marshal(response)
	if err != nil {
		return implstate.EvidenceRef{}, err
	}
	return journal.Publish(implstate.EvidenceID(string(resultID)+"-response"), data)
}

func recoveredBriefRefinement(input BriefRefinementInput) (BriefRefinementResult, bool, error) {
	result := assignmentResultForOperation(input.Run, input.AssignmentID, input.OperationID)
	if result == nil {
		// Publishing an accepted answer precedes recording its result. If the
		// event could not be written at all, recover that exact accepted answer
		// rather than asking the provider a second time.
		reference, err := input.Journal.PublishedReference(implstate.EvidenceID(string(input.ResultID) + "-response"))
		if err != nil {
			if errors.Is(err, runstore.ErrReferenceUnavailable) {
				return BriefRefinementResult{}, false, nil
			}
			return BriefRefinementResult{}, true, err
		}
		data, err := input.Journal.Read(reference)
		if err != nil {
			return BriefRefinementResult{}, true, err
		}
		var response AgentResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return BriefRefinementResult{}, true, fmt.Errorf("%w: decode published briefer response: %v", ErrBriefRefinement, err)
		}
		operation := assignmentOperation(input.Run, input.AssignmentID, input.OperationID)
		if operation == nil {
			return BriefRefinementResult{}, true, fmt.Errorf("%w: published response has no refinement operation", ErrBriefRefinement)
		}
		recovered, err := persistBriefRefinementOutcome(context.Background(), input, operation.BriefID, input.OperationID, input.ResultID, response)
		if err != nil || response.Kind != ResponseExplorationRequested {
			return recovered, true, err
		}
		return recoverBriefRefinementExplorer(input, response)
	}
	if result.ID != input.ResultID {
		return BriefRefinementResult{}, true, fmt.Errorf("%w: refinement operation already has another result", ErrBriefRefinement)
	}
	if response, err := durableBriefRefinementResponse(input, *result); err != nil {
		return BriefRefinementResult{}, true, err
	} else if response.Kind == ResponseExplorationRequested {
		return recoverBriefRefinementExplorer(input, response)
	}
	recovered, err := recoveredBriefRefinementResult(input, *result)
	return recovered, true, err
}

// recoverBriefRefinementExplorer resumes a durable source request instead of
// asking the briefer to repeat it. It is used both after a fully recorded
// request and after the narrower publication-before-state-event boundary.
func recoverBriefRefinementExplorer(input BriefRefinementInput, request AgentResponse) (BriefRefinementResult, bool, error) {
	brief, err := currentAssignmentBrief(input.Journal, input.Run, input.AssignmentID)
	if err != nil {
		return BriefRefinementResult{}, true, err
	}
	session, err := recoveryBrieferSession(context.Background(), input, brief, input.Request, request)
	if err != nil {
		return BriefRefinementResult{}, true, err
	}
	expectation := briefRefinementCall(input, session, input.OperationID, request.Binding, "").Expectation
	recovered, err := continueBriefRefinementExplorer(context.Background(), input, brief, session, expectation, request, 0)
	return recovered, true, err
}

func recoveredBriefRefinementResult(input BriefRefinementInput, result implstate.OperationResult) (BriefRefinementResult, error) {
	response, err := durableBriefRefinementResponse(input, result)
	if err != nil {
		return BriefRefinementResult{}, err
	}
	answer := BriefRefinementResult{Closed: response.Kind == ResponseClarificationNeeded, Paused: response.Kind == ResponseExecutionBlocked, PauseReason: input.Run.PauseReason}
	if response.Kind == ResponseBriefReady {
		if len(result.Evidence) < 2 {
			return BriefRefinementResult{}, fmt.Errorf("%w: brief response result lacks published brief", ErrBriefRefinement)
		}
		assignment := assignmentForReview(input.Run, input.AssignmentID)
		for index := range assignment.Briefs {
			if assignment.Briefs[index].Document == result.Evidence[1] {
				brief := assignment.Briefs[index]
				answer.Brief = &brief
				return answer, nil
			}
		}
		return BriefRefinementResult{}, fmt.Errorf("%w: durable brief result is not linked to a brief version", ErrBriefRefinement)
	}
	return answer, nil
}

func durableBriefRefinementResponse(input BriefRefinementInput, result implstate.OperationResult) (AgentResponse, error) {
	if len(result.Evidence) == 0 {
		return AgentResponse{}, fmt.Errorf("%w: refinement result lacks response evidence", ErrBriefRefinement)
	}
	data, err := input.Journal.Read(result.Evidence[0])
	if err != nil {
		return AgentResponse{}, err
	}
	var response AgentResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return AgentResponse{}, fmt.Errorf("%w: decode durable briefer response: %v", ErrBriefRefinement, err)
	}
	return response, nil
}

func cloneBriefRefinementRun(run *implstate.Run) (*implstate.Run, error) {
	event, err := implstate.NewRunStateEvent(1, run)
	if err != nil {
		return nil, fmt.Errorf("%w: clone run state: %v", ErrBriefRefinement, err)
	}
	return event.State, nil
}

func persistBriefRefinementCandidate(ctx context.Context, store *runstore.StateStore, current, candidate *implstate.Run) error {
	written, err := recordBriefRefinementState(ctx, store, candidate)
	if written.Sequence != 0 {
		*current = *candidate
	}
	if err != nil {
		return fmt.Errorf("%w: persist refinement state: %v", ErrBriefRefinement, err)
	}
	return nil
}

func routeBriefRefinementExplorer(ctx context.Context, input BriefRefinementInput, brief assignmentBrief, turn ControlledAgentCallResult, source ControlledAgentCall) (BriefRefinementResult, error) {
	result, err := continueBriefRefinementExplorer(ctx, input, brief, turn.Session, source.Expectation, turn.Response, 0)
	if result.Call.Session == nil {
		result.Call = turn
	}
	return result, err
}

func continueBriefRefinementExplorer(ctx context.Context, input BriefRefinementInput, brief assignmentBrief, sourceSession *AgentSession, sourceExpectation ResponseExpectation, request AgentResponse, episode int) (BriefRefinementResult, error) {
	value := briefRefinementExplorerAt(input.Explorer, episode)
	if value == nil {
		return BriefRefinementResult{}, fmt.Errorf("%w: Explorer request has no controller-owned route", ErrBriefRefinement)
	}
	if err := prepareBriefRefinementExplorerOperations(ctx, input, brief.ID, value); err != nil {
		return BriefRefinementResult{}, err
	}
	if durable := assignmentResult(input.Run, input.AssignmentID, value.ExplorerResultID); durable != nil {
		response, err := durableBriefRefinementResponse(input, *durable)
		if err != nil {
			return BriefRefinementResult{}, err
		}
		if response.Kind == ResponseExecutionBlocked {
			return BriefRefinementResult{Paused: true, PauseReason: input.Run.PauseReason}, nil
		}
		return continueBriefRefinementAfterExplorer(ctx, input, brief, sourceSession, sourceExpectation, request, value, response, episode)
	}
	if reference, err := input.Journal.PublishedReference(implstate.EvidenceID(string(value.ExplorerResultID) + "-response")); err == nil {
		data, readErr := input.Journal.Read(reference)
		if readErr != nil {
			return BriefRefinementResult{}, readErr
		}
		var response AgentResponse
		if decodeErr := json.Unmarshal(data, &response); decodeErr != nil {
			return BriefRefinementResult{}, fmt.Errorf("%w: decode published Explorer response: %v", ErrBriefRefinement, decodeErr)
		}
		if persistErr := persistBriefExplorerOutcome(ctx, input, brief.ID, value, response); persistErr != nil {
			return BriefRefinementResult{}, persistErr
		}
		if response.Kind == ResponseExecutionBlocked {
			return BriefRefinementResult{Paused: true, PauseReason: input.Run.PauseReason}, nil
		}
		return continueBriefRefinementAfterExplorer(ctx, input, brief, sourceSession, sourceExpectation, request, value, response, episode)
	} else if !errors.Is(err, runstore.ErrReferenceUnavailable) {
		return BriefRefinementResult{}, err
	}
	explorerCall := ControlledAgentCall{Repository: input.Repository, Workspace: input.Workspace, Policy: AgentCallPolicy{Role: AgentRoleExplorer, CallID: value.ExplorerCallID}, Run: input.Run, Journal: input.Journal, StateStore: input.StateStore, AssignmentID: input.AssignmentID, OperationID: value.ExplorerOperationID, Limits: input.Limits, Expectation: ResponseExpectation{Role: ResponseRoleExplorer, State: ResponseStateExploring, Scope: ResponseScopeAssignment, ExplorerSource: ExplorerSourceBriefRefinement, Binding: briefRefinementBinding(input, brief.ID, value.ExplorerCallID)}}
	continuation := briefRefinementCall(input, sourceSession, value.ContinuationOperationID, sourceExpectation.Binding, "")
	route := ExplorerRoute{
		Owner: input.Owner, SourceSession: sourceSession, SourceExpectation: sourceExpectation, Request: request, ExplorerCall: explorerCall, ExplorerResultID: value.ExplorerResultID, SourceContinuation: continuation, ExplorerCharacters: value.ExplorerCharacters,
		PersistExplorerResponse: func(persistCtx context.Context, call ControlledAgentCallResult) error {
			return persistBriefExplorerOutcome(persistCtx, input, brief.ID, value, call.Response)
		},
	}
	routed, err := RouteExplorer(ctx, route)
	if err != nil {
		return BriefRefinementResult{}, err
	}
	if routed.Paused {
		return BriefRefinementResult{Paused: true, PauseReason: routed.PauseReason}, nil
	}
	return persistBriefRefinementContinuation(ctx, input, brief, sourceSession, sourceExpectation, request, value, routed.Response, routed.SourceContinuationResponse, routed.SourceContinuationAttempts, episode)
}

func continueBriefRefinementAfterExplorer(ctx context.Context, input BriefRefinementInput, brief assignmentBrief, sourceSession *AgentSession, sourceExpectation ResponseExpectation, request AgentResponse, value *BriefRefinementExplorer, explorerResponse AgentResponse, episode int) (BriefRefinementResult, error) {
	if durable := assignmentResult(input.Run, input.AssignmentID, value.ContinuationResultID); durable != nil {
		response, err := durableBriefRefinementResponse(input, *durable)
		if err != nil {
			return BriefRefinementResult{}, err
		}
		if response.Kind == ResponseExplorationRequested {
			continuation := briefRefinementCall(input, sourceSession, value.ContinuationOperationID, sourceExpectation.Binding, "")
			return continueBriefRefinementExplorer(ctx, input, brief, sourceSession, continuation.Expectation, response, episode+1)
		}
		return recoveredBriefRefinementResult(input, *durable)
	}
	// The source response is also published before its state event. A failure
	// at that boundary must continue from the accepted response, not redispatch
	// the same briefer turn.
	if reference, err := input.Journal.PublishedReference(implstate.EvidenceID(string(value.ContinuationResultID) + "-response")); err == nil {
		data, readErr := input.Journal.Read(reference)
		if readErr != nil {
			return BriefRefinementResult{}, readErr
		}
		var response AgentResponse
		if decodeErr := json.Unmarshal(data, &response); decodeErr != nil {
			return BriefRefinementResult{}, fmt.Errorf("%w: decode published continuation response: %v", ErrBriefRefinement, decodeErr)
		}
		return persistBriefRefinementContinuation(ctx, input, brief, sourceSession, sourceExpectation, request, value, explorerResponse, response, 0, episode)
	} else if !errors.Is(err, runstore.ErrReferenceUnavailable) {
		return BriefRefinementResult{}, err
	}
	continuation := briefRefinementCall(input, sourceSession, value.ContinuationOperationID, sourceExpectation.Binding, explorerContinuation(explorerResponse))
	turn, err := InvokeControlledAgentCall(ctx, continuation)
	if err != nil {
		return BriefRefinementResult{Call: turn}, err
	}
	return persistBriefRefinementContinuation(ctx, input, brief, sourceSession, sourceExpectation, request, value, explorerResponse, turn.Response, turn.Attempts, episode)
}

func persistBriefRefinementContinuation(ctx context.Context, input BriefRefinementInput, brief assignmentBrief, sourceSession *AgentSession, sourceExpectation ResponseExpectation, request AgentResponse, value *BriefRefinementExplorer, explorerResponse, response AgentResponse, attempts uint64, episode int) (BriefRefinementResult, error) {
	result, err := persistBriefRefinementOutcome(ctx, input, brief.ID, value.ContinuationOperationID, value.ContinuationResultID, response)
	if err != nil {
		return result, err
	}
	result.Call = ControlledAgentCallResult{Response: response, Session: sourceSession, Attempts: attempts}
	if response.Kind != ResponseExplorationRequested {
		return result, nil
	}
	continuation := briefRefinementCall(input, sourceSession, value.ContinuationOperationID, sourceExpectation.Binding, "")
	next, err := continueBriefRefinementExplorer(ctx, input, brief, sourceSession, continuation.Expectation, response, episode+1)
	if next.Call.Session == nil {
		next.Call = result.Call
	}
	return next, err
}

func persistBriefExplorerOutcome(ctx context.Context, input BriefRefinementInput, briefID implstate.BriefID, value *BriefRefinementExplorer, response AgentResponse) error {
	if existing := assignmentResult(input.Run, input.AssignmentID, value.ExplorerResultID); existing != nil {
		return nil
	}
	evidence, err := publishBriefRefinementResponse(input.Journal, value.ExplorerResultID, response)
	if err != nil {
		return err
	}
	candidate, err := cloneBriefRefinementRun(input.Run)
	if err != nil {
		return err
	}
	operation := assignmentOperation(candidate, input.AssignmentID, value.ExplorerOperationID)
	if operation == nil || operation.BriefID != briefID {
		return fmt.Errorf("%w: Explorer outcome lacks its operation", ErrBriefRefinement)
	}
	status := implstate.ResultSucceeded
	if response.Kind == ResponseExecutionBlocked {
		status = implstate.ResultFailed
	}
	if err := candidate.AddResult(input.AssignmentID, implstate.OperationResult{ID: value.ExplorerResultID, OperationID: value.ExplorerOperationID, Status: status, State: candidate.CurrentState, Basis: operation.Basis, Evidence: []implstate.EvidenceRef{evidence}}); err != nil {
		return err
	}
	if response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(response)
		if err != nil {
			return err
		}
		if err := candidate.PauseExecutionBlocked(block); err != nil {
			return err
		}
	}
	return persistBriefRefinementCandidate(ctx, input.StateStore, input.Run, candidate)
}

func prepareBriefRefinementExplorerOperations(ctx context.Context, input BriefRefinementInput, briefID implstate.BriefID, value *BriefRefinementExplorer) error {
	if value == nil || value.ExplorerOperationID == "" || value.ExplorerResultID == "" || value.ContinuationOperationID == "" || value.ContinuationResultID == "" || strings.TrimSpace(value.ExplorerCallID) == "" {
		return fmt.Errorf("%w: Explorer route identities are required", ErrBriefRefinement)
	}
	candidate, err := cloneBriefRefinementRun(input.Run)
	if err != nil {
		return err
	}
	basis := implstate.AcceptanceBasis{Specification: candidate.Identity.Specification, Configuration: candidate.Identity.Configuration}
	for _, operation := range []implstate.Operation{{ID: value.ExplorerOperationID, Kind: implstate.OperationAgent, BriefID: briefID, Basis: basis, Description: "research for brief refinement", Counter: implstate.CycleCounterExplorer, Episode: "brief_refinement"}, {ID: value.ContinuationOperationID, Kind: implstate.OperationAgent, BriefID: briefID, Basis: basis, Description: "continue brief refinement after research"}} {
		if assignmentOperation(candidate, input.AssignmentID, operation.ID) == nil {
			if err := candidate.AddOperation(input.AssignmentID, operation); err != nil {
				return err
			}
		}
	}
	return persistBriefRefinementCandidate(ctx, input.StateStore, input.Run, candidate)
}

func briefRefinementExplorerAt(value *BriefRefinementExplorer, index int) *BriefRefinementExplorer {
	if value == nil {
		return nil
	}
	if index == 0 {
		return value
	}
	if index > len(value.Additional) {
		return nil
	}
	return &value.Additional[index-1]
}

func recoveryBrieferSession(ctx context.Context, input BriefRefinementInput, brief assignmentBrief, reportedGap, explorerRequest AgentResponse) (*AgentSession, error) {
	session, err := input.Owner.ExistingBriefer(input.AssignmentID)
	if !errors.Is(err, ErrSessionMissing) {
		return session, err
	}
	start, startErr := buildBriefRefinementRecoveryStartContext(ctx, input, brief, reportedGap, explorerRequest)
	if startErr != nil {
		return nil, fmt.Errorf("%w: rebuild briefer context: %v", ErrBriefRefinement, startErr)
	}
	return input.Owner.Briefer(ctx, input.AssignmentID, start)
}

// buildBriefRefinementRecoveryStartContext gives a replacement briefer the
// active refinement packet before a durable Explorer result is continued.
// Provider conversation history is not recoverable, so the controller must
// put every decision-relevant durable input in this first message.
func buildBriefRefinementRecoveryStartContext(ctx context.Context, input BriefRefinementInput, brief assignmentBrief, reportedGap, explorerRequest AgentResponse) (BrieferStartContext, error) {
	start, err := BuildBrieferStartContext(input.Journal, input.Run, input.AssignmentID)
	if err != nil {
		return BrieferStartContext{}, err
	}
	diff, err := effectiveWorkspaceControl(input.Workspace).AssignmentDiff(ctx, input.Repository, assignmentDiffBase(input.Run, input.AssignmentID))
	if err != nil {
		return BrieferStartContext{}, err
	}
	history, err := durableBriefRefinementExplorerHistory(input)
	if err != nil {
		return BrieferStartContext{}, err
	}

	data := strings.Builder{}
	data.WriteString("# Active brief refinement recovery\n\n")
	data.WriteString(refinementMessage(brief.Text, diff, reportedGap))
	if explorerRequest.Kind == ResponseExplorationRequested {
		data.WriteString("\n\n## Durable Explorer request\n\n")
		data.WriteString(explorerRequestMessage(explorerRequest))
	}
	data.WriteString("\n\n## Durable Explorer history\n\n")
	if len(history) == 0 {
		data.WriteString("No Explorer result has been durably recorded for this refinement.\n")
	} else {
		for index, response := range history {
			encoded, marshalErr := json.MarshalIndent(response, "", "  ")
			if marshalErr != nil {
				return BrieferStartContext{}, fmt.Errorf("%w: encode durable Explorer history: %v", ErrBriefRefinement, marshalErr)
			}
			fmt.Fprintf(&data, "### Explorer result %d\n\n```json\n%s\n```\n\n", index+1, encoded)
		}
	}
	start.start.StartMessage += "\n\n" + data.String()
	return start, nil
}

func explorerRequestMessage(request AgentResponse) string {
	return fmt.Sprintf("Question: %s\n\nContext: %s\n\nBoundaries: %s\n\nKnown facts:\n%s", *request.Question, *request.Context, *request.Boundaries, markdownList(request.KnownFacts))
}

func durableBriefRefinementExplorerHistory(input BriefRefinementInput) ([]AgentResponse, error) {
	var history []AgentResponse
	for episode := 0; ; episode++ {
		value := briefRefinementExplorerAt(input.Explorer, episode)
		if value == nil {
			return history, nil
		}
		result := assignmentResult(input.Run, input.AssignmentID, value.ExplorerResultID)
		if result == nil {
			continue
		}
		response, err := durableBriefRefinementResponse(input, *result)
		if err != nil {
			return nil, err
		}
		if response.Kind == ResponseExplorationResult {
			history = append(history, response)
		}
	}
}

func briefRefinementBinding(input BriefRefinementInput, briefID implstate.BriefID, callID string) ResponseBinding {
	return ResponseBinding{CallID: callID, RunID: input.Run.Identity.ID, AssignmentID: input.AssignmentID, BriefID: briefID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
}

func assignmentOperation(run *implstate.Run, assignmentID implstate.AssignmentID, operationID implstate.OperationID) *implstate.Operation {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil {
		return nil
	}
	for index := range assignment.Operations {
		if assignment.Operations[index].ID == operationID {
			return &assignment.Operations[index]
		}
	}
	return nil
}

func assignmentResult(run *implstate.Run, assignmentID implstate.AssignmentID, resultID implstate.ResultID) *implstate.OperationResult {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil {
		return nil
	}
	for index := range assignment.Results {
		if assignment.Results[index].ID == resultID {
			return &assignment.Results[index]
		}
	}
	return nil
}

func assignmentResultForOperation(run *implstate.Run, assignmentID implstate.AssignmentID, operationID implstate.OperationID) *implstate.OperationResult {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil {
		return nil
	}
	for index := range assignment.Results {
		if assignment.Results[index].OperationID == operationID {
			return &assignment.Results[index]
		}
	}
	return nil
}

func sameTaskIDs(run *implstate.Run, assignmentID implstate.AssignmentID, taskIDs []implstate.TaskID) bool {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil || len(taskIDs) != len(assignment.TaskIDs) {
		return false
	}
	for index := range taskIDs {
		if taskIDs[index] != assignment.TaskIDs[index] {
			return false
		}
	}
	return true
}

func refinementMessage(brief, diff string, request AgentResponse) string {
	return fmt.Sprintf("# Brief refinement request\n\nThe executor or task reviewer reports a gap in the current brief. Re-evaluate the current code and issue a new self-contained brief only if the complete specification already determines the answer. Keep exactly the same task IDs. If it requires a material user choice not resolved by that specification, return clarification_required instead.\n\n## Current brief\n\n%s\n\n## Current assignment code diff\n\n%s\n\n## Reported gap\n\nQuestion: %s\n\nContext: %s\n\nBoundaries: %s\n\nReferences:\n%s", brief, diff, *request.Question, *request.Context, *request.Boundaries, markdownList(request.References))
}

func briefClarificationCloseReason(response AgentResponse) string {
	parts := []string{"unresolved material specification issue", "question: " + strings.TrimSpace(*response.Question), "context: " + strings.TrimSpace(*response.Context), "boundaries: " + strings.TrimSpace(*response.Boundaries), "references: " + strings.Join(response.References, "; ")}
	if len(response.Options) != 0 {
		parts = append(parts, "options: "+strings.Join(response.Options, "; "))
	}
	if response.Recommendation != nil {
		parts = append(parts, "recommendation: "+strings.TrimSpace(*response.Recommendation))
	}
	return strings.Join(parts, "; ")
}
