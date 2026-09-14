package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var ErrBriefRefinement = errors.New("invalid assignment brief refinement")

// recordBriefRefinementState is a narrow seam for preserving the durable-event
// boundary under tests; production always delegates directly to StateStore.
var recordBriefRefinementState = func(ctx context.Context, store *runstore.StateStore, state *implementationstate.Run) (implementationstate.Event, error) {
	return store.Record(ctx, state)
}

type BriefRefinementInput struct {
	Owner         *SessionOwner
	Run           *implementationstate.Run
	StateStore    *runstore.StateStore
	Journal       *runstore.Run
	Repository    string
	AssignmentID  implementationstate.AssignmentID
	RequesterRole ResponseRole
	Request       AgentResponse
	OperationID   implementationstate.OperationID
	ResultID      implementationstate.ResultID
	CallID        string
	Limits        implementationstate.CycleLimits
	Timeout       time.Duration
	Explorer      *BriefRefinementExplorer
}

// BriefRefinementExplorer contains controller-allocated identities for one
// Explorer episode and the continuing turn in the same briefer session.
type BriefRefinementExplorer struct {
	ExplorerOperationID     implementationstate.OperationID
	ExplorerCallID          string
	ContinuationOperationID implementationstate.OperationID
	ContinuationResultID    implementationstate.ResultID
	ExplorerCharacters      int
}

type BriefRefinementResult struct {
	Call        ControlledAgentCallResult
	Brief       *implementationstate.BriefVersion
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
	diff, err := assignmentDiff(ctx, input.Repository, assignmentDiffBase(input.Run, input.AssignmentID))
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

func refinementNeedsFreshSession(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID) bool {
	operation := assignmentOperation(run, assignmentID, operationID)
	if operation == nil || len(operation.Attempts) == 0 {
		return false
	}
	return operation.Attempts[len(operation.Attempts)-1].Outcome != implementationstate.AttemptSucceeded
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

func validateBriefRefinementRequest(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, briefID implementationstate.BriefID, request AgentResponse) error {
	if request.Kind != ResponseClarificationNeeded || request.Binding.RunID != run.Identity.ID || request.Binding.AssignmentID != assignmentID || request.Binding.BriefID != briefID || request.Binding.Specification != run.Identity.Specification || request.Binding.Configuration != run.Identity.Configuration || request.Binding.TaskList != run.Identity.TaskList || request.Question == nil || request.Context == nil || request.Boundaries == nil || len(request.References) == 0 {
		return fmt.Errorf("%w: clarification request is not bound to the current brief", ErrBriefRefinement)
	}
	return nil
}

func prepareBriefRefinementOperation(ctx context.Context, input BriefRefinementInput, briefID implementationstate.BriefID) error {
	if operation := assignmentOperation(input.Run, input.AssignmentID, input.OperationID); operation != nil {
		if operation.Kind != implementationstate.OperationAgent || operation.Counter != implementationstate.CycleCounterBriefRefinement || operation.BriefID != briefID {
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
	basis := implementationstate.AcceptanceBasis{Specification: candidate.Identity.Specification, Configuration: candidate.Identity.Configuration}
	if err := candidate.AddOperation(input.AssignmentID, implementationstate.Operation{ID: input.OperationID, Kind: implementationstate.OperationAgent, BriefID: briefID, Basis: basis, Description: "refine assignment brief", Counter: implementationstate.CycleCounterBriefRefinement}); err != nil {
		return fmt.Errorf("%w: create refinement operation: %v", ErrBriefRefinement, err)
	}
	return persistBriefRefinementCandidate(ctx, input.StateStore, input.Run, candidate)
}

func briefRefinementCall(input BriefRefinementInput, session *AgentSession, operationID implementationstate.OperationID, binding ResponseBinding, message string) ControlledAgentCall {
	call := ControlledAgentCall{Session: session, Repository: input.Repository, Policy: AgentCallPolicy{Role: AgentRoleBriefer, CallID: binding.CallID}, Run: input.Run, Journal: input.Journal, StateStore: input.StateStore, AssignmentID: input.AssignmentID, OperationID: operationID, Limits: input.Limits, Expectation: ResponseExpectation{Role: ResponseRoleBriefer, State: ResponseStateBriefRefinement, Scope: ResponseScopeAssignment, Binding: binding}, Message: message, Timeout: input.Timeout}
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

func persistBriefRefinementOutcome(ctx context.Context, input BriefRefinementInput, priorBriefID implementationstate.BriefID, operationID implementationstate.OperationID, resultID implementationstate.ResultID, response AgentResponse) (BriefRefinementResult, error) {
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
	result := implementationstate.OperationResult{ID: resultID, OperationID: operationID, Status: implementationstate.ResultSucceeded, State: candidate.CurrentState, Basis: operation.Basis, Evidence: []implementationstate.EvidenceRef{evidence}}
	var published *implementationstate.BriefVersion
	switch response.Kind {
	case ResponseBriefReady:
		brief, document, err := publishRefinedBriefCandidate(input.Journal, candidate, input.AssignmentID, response)
		if err != nil {
			return BriefRefinementResult{}, err
		}
		result.Evidence, published = append(result.Evidence, document), &brief
	case ResponseClarificationNeeded:
	case ResponseExecutionBlocked:
		result.Status = implementationstate.ResultFailed
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
		if err := candidate.Pause(briefExecutionBlockedPauseReason(response)); err != nil {
			return BriefRefinementResult{}, err
		}
	}
	if err := persistBriefRefinementCandidate(context.WithoutCancel(ctx), input.StateStore, input.Run, candidate); err != nil {
		return BriefRefinementResult{}, err
	}
	return BriefRefinementResult{Brief: published, Closed: response.Kind == ResponseClarificationNeeded, Paused: response.Kind == ResponseExecutionBlocked, PauseReason: candidate.PauseReason}, nil
}

func publishRefinedBriefCandidate(journal *runstore.Run, run *implementationstate.Run, assignmentID implementationstate.AssignmentID, response AgentResponse) (implementationstate.BriefVersion, implementationstate.EvidenceRef, error) {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil || !sameTaskIDs(run, assignmentID, response.TaskIDs) || response.Brief == nil {
		return implementationstate.BriefVersion{}, implementationstate.EvidenceRef{}, fmt.Errorf("%w: refined brief changes the assignment", ErrBriefRefinement)
	}
	number := len(assignment.Briefs) + 1
	briefID := implementationstate.BriefID(fmt.Sprintf("brief-%s-v%d", assignmentID, number))
	documentID := implementationstate.EvidenceID(fmt.Sprintf("brief-%s-v%d.md", assignmentID, number))
	document, err := journal.Publish(documentID, renderBriefDocument(assignmentID, number, assignment.TaskIDs, *response.Brief))
	if err != nil {
		return implementationstate.BriefVersion{}, implementationstate.EvidenceRef{}, err
	}
	if err := journal.VerifyReference(document); err != nil {
		return implementationstate.BriefVersion{}, implementationstate.EvidenceRef{}, err
	}
	brief := implementationstate.BriefVersion{ID: briefID, Number: number, Document: document}
	if err := run.AddBriefVersion(assignmentID, brief); err != nil {
		return implementationstate.BriefVersion{}, implementationstate.EvidenceRef{}, err
	}
	return brief, document, nil
}

func publishBriefRefinementResponse(journal *runstore.Run, resultID implementationstate.ResultID, response AgentResponse) (implementationstate.EvidenceRef, error) {
	data, err := json.Marshal(response)
	if err != nil {
		return implementationstate.EvidenceRef{}, err
	}
	return journal.Publish(implementationstate.EvidenceID(string(resultID)+"-response"), data)
}

func recoveredBriefRefinement(input BriefRefinementInput) (BriefRefinementResult, bool, error) {
	result := assignmentResultForOperation(input.Run, input.AssignmentID, input.OperationID)
	if result == nil {
		return BriefRefinementResult{}, false, nil
	}
	if result.ID != input.ResultID {
		return BriefRefinementResult{}, true, fmt.Errorf("%w: refinement operation already has another result", ErrBriefRefinement)
	}
	recovered, err := recoveredBriefRefinementResult(input, *result)
	return recovered, true, err
}

func recoveredBriefRefinementResult(input BriefRefinementInput, result implementationstate.OperationResult) (BriefRefinementResult, error) {
	if len(result.Evidence) == 0 {
		return BriefRefinementResult{}, fmt.Errorf("%w: refinement result lacks response evidence", ErrBriefRefinement)
	}
	data, err := input.Journal.Read(result.Evidence[0])
	if err != nil {
		return BriefRefinementResult{}, err
	}
	var response AgentResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return BriefRefinementResult{}, fmt.Errorf("%w: decode durable briefer response: %v", ErrBriefRefinement, err)
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

func cloneBriefRefinementRun(run *implementationstate.Run) (*implementationstate.Run, error) {
	event, err := implementationstate.NewRunStateEvent(1, run)
	if err != nil {
		return nil, fmt.Errorf("%w: clone run state: %v", ErrBriefRefinement, err)
	}
	return event.State, nil
}

func persistBriefRefinementCandidate(ctx context.Context, store *runstore.StateStore, current, candidate *implementationstate.Run) error {
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
	if input.Explorer == nil {
		return BriefRefinementResult{Call: turn}, fmt.Errorf("%w: Explorer request has no controller-owned route", ErrBriefRefinement)
	}
	if err := prepareBriefRefinementExplorerOperations(ctx, input, brief.ID); err != nil {
		return BriefRefinementResult{Call: turn}, err
	}
	value := input.Explorer
	explorerCall := ControlledAgentCall{Repository: input.Repository, Policy: AgentCallPolicy{Role: AgentRoleExplorer, CallID: value.ExplorerCallID}, Run: input.Run, Journal: input.Journal, StateStore: input.StateStore, AssignmentID: input.AssignmentID, OperationID: value.ExplorerOperationID, Limits: input.Limits, Expectation: ResponseExpectation{Role: ResponseRoleExplorer, State: ResponseStateExploring, Scope: ResponseScopeAssignment, ExplorerSource: ExplorerSourceBriefRefinement, Binding: briefRefinementBinding(input, brief.ID, value.ExplorerCallID)}}
	continuation := briefRefinementCall(input, turn.Session, value.ContinuationOperationID, source.Expectation.Binding, "")
	route := ExplorerRoute{Owner: input.Owner, SourceSession: turn.Session, SourceExpectation: source.Expectation, Request: turn.Response, ExplorerCall: explorerCall, SourceContinuation: continuation, ExplorerCharacters: value.ExplorerCharacters}
	routed, err := RouteExplorer(ctx, route)
	if err != nil {
		return BriefRefinementResult{Call: turn}, err
	}
	if routed.Paused {
		return BriefRefinementResult{Call: turn, Paused: true, PauseReason: routed.PauseReason}, nil
	}
	result, err := persistBriefRefinementOutcome(ctx, input, brief.ID, value.ContinuationOperationID, value.ContinuationResultID, routed.SourceContinuationResponse)
	result.Call = ControlledAgentCallResult{Response: routed.SourceContinuationResponse, Session: turn.Session, Attempts: routed.SourceContinuationAttempts}
	return result, err
}

func prepareBriefRefinementExplorerOperations(ctx context.Context, input BriefRefinementInput, briefID implementationstate.BriefID) error {
	value := input.Explorer
	if value == nil || value.ExplorerOperationID == "" || value.ContinuationOperationID == "" || value.ContinuationResultID == "" || strings.TrimSpace(value.ExplorerCallID) == "" {
		return fmt.Errorf("%w: Explorer route identities are required", ErrBriefRefinement)
	}
	candidate, err := cloneBriefRefinementRun(input.Run)
	if err != nil {
		return err
	}
	basis := implementationstate.AcceptanceBasis{Specification: candidate.Identity.Specification, Configuration: candidate.Identity.Configuration}
	for _, operation := range []implementationstate.Operation{{ID: value.ExplorerOperationID, Kind: implementationstate.OperationAgent, BriefID: briefID, Basis: basis, Description: "research for brief refinement", Counter: implementationstate.CycleCounterExplorer, Episode: "brief_refinement"}, {ID: value.ContinuationOperationID, Kind: implementationstate.OperationAgent, BriefID: briefID, Basis: basis, Description: "continue brief refinement after research"}} {
		if assignmentOperation(candidate, input.AssignmentID, operation.ID) == nil {
			if err := candidate.AddOperation(input.AssignmentID, operation); err != nil {
				return err
			}
		}
	}
	return persistBriefRefinementCandidate(ctx, input.StateStore, input.Run, candidate)
}

func briefRefinementBinding(input BriefRefinementInput, briefID implementationstate.BriefID, callID string) ResponseBinding {
	return ResponseBinding{CallID: callID, RunID: input.Run.Identity.ID, AssignmentID: input.AssignmentID, BriefID: briefID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
}

func assignmentOperation(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID) *implementationstate.Operation {
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

func assignmentResult(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, resultID implementationstate.ResultID) *implementationstate.OperationResult {
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

func assignmentResultForOperation(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID) *implementationstate.OperationResult {
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

func sameTaskIDs(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, taskIDs []implementationstate.TaskID) bool {
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

func briefExecutionBlockedPauseReason(response AgentResponse) string {
	return fmt.Sprintf("execution_blocked: brief refinement blocked action: %s; diagnostic: %s; attempts: %s; required user action: %s", *response.BlockedAction, *response.Diagnostic, strings.Join(response.Attempts, "; "), *response.RequiredUserAction)
}
