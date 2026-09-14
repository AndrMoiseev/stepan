package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// ErrBriefRefinement identifies an unsafe attempt to revise an assignment
// contract. A refinement never changes the stable selected task block.
var ErrBriefRefinement = errors.New("invalid assignment brief refinement")

// BriefRefinementInput contains controller-owned identities for one request
// to clarify the current assignment. RequesterRole is deliberately supplied by
// the caller instead of trusting an agent response to name its own authority.
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
	CallID        string
	Limits        implementationstate.CycleLimits
	Timeout       time.Duration
}

// BriefRefinementResult reports either the controller-published next brief or
// a terminal requirements escalation. It never changes the selected tasks.
type BriefRefinementResult struct {
	Call   ControlledAgentCallResult
	Brief  *implementationstate.BriefVersion
	Closed bool
}

// RefineBrief routes a validated assignment clarification to the same briefer
// session that issued its first version. The controller provides the current
// code diff and the complete original specification; it therefore permits an
// autonomous revision only when the briefer can point to that fixed source.
// A material question still missing there closes the run for the user rather
// than silently defining new scope.
func RefineBrief(ctx context.Context, input BriefRefinementInput) (BriefRefinementResult, error) {
	if err := validateBriefRefinementInput(input); err != nil {
		return BriefRefinementResult{}, err
	}
	brief, err := currentAssignmentBrief(input.Journal, input.Run, input.AssignmentID)
	if err != nil {
		return BriefRefinementResult{}, fmt.Errorf("%w: current brief: %v", ErrBriefRefinement, err)
	}
	if err := validateBriefRefinementRequest(input.Run, input.AssignmentID, brief.ID, input.Request); err != nil {
		return BriefRefinementResult{}, err
	}
	session, err := input.Owner.ExistingBriefer(input.AssignmentID)
	if err != nil {
		return BriefRefinementResult{}, fmt.Errorf("%w: existing briefer session: %v", ErrBriefRefinement, err)
	}
	// An acceptance for the old brief must not survive a new contract. Do this
	// before durable operation reservation, which requires an active assignment.
	if err := input.Run.BeginBriefRefinement(input.AssignmentID); err != nil {
		return BriefRefinementResult{}, fmt.Errorf("%w: reopen assignment: %v", ErrBriefRefinement, err)
	}
	basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	if err := input.Run.AddOperation(input.AssignmentID, implementationstate.Operation{
		ID: input.OperationID, Kind: implementationstate.OperationAgent, BriefID: brief.ID,
		Basis: basis, Description: "refine assignment brief", Counter: implementationstate.CycleCounterBriefRefinement,
	}); err != nil {
		return BriefRefinementResult{}, fmt.Errorf("%w: create refinement operation: %v", ErrBriefRefinement, err)
	}
	if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
		return BriefRefinementResult{}, fmt.Errorf("%w: persist refinement operation: %v", ErrBriefRefinement, err)
	}

	diff, err := assignmentDiff(ctx, input.Repository, assignmentDiffBase(input.Run, input.AssignmentID))
	if err != nil {
		return BriefRefinementResult{}, err
	}
	message := refinementMessage(brief.Text, diff, input.Request)
	binding := ResponseBinding{CallID: input.CallID, RunID: input.Run.Identity.ID, AssignmentID: input.AssignmentID, BriefID: brief.ID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
	call := ControlledAgentCall{
		Session: session, Repository: input.Repository, Policy: AgentCallPolicy{Role: AgentRoleBriefer, CallID: input.CallID},
		Run: input.Run, Journal: input.Journal, StateStore: input.StateStore, AssignmentID: input.AssignmentID,
		OperationID: input.OperationID, Limits: input.Limits,
		Expectation: ResponseExpectation{Role: ResponseRoleBriefer, State: ResponseStateBriefRefinement, Scope: ResponseScopeAssignment, Binding: binding},
		Message:     message, Timeout: input.Timeout,
	}
	call.ValidateResponse = func(response AgentResponse) error {
		if response.Kind == ResponseClarificationNeeded {
			return nil
		}
		if response.Kind != ResponseBriefReady || response.Binding != binding || response.Brief == nil || !sameTaskIDs(input.Run, input.AssignmentID, response.TaskIDs) {
			return fmt.Errorf("%w: briefer response must retain the current assignment task block", ErrBriefRefinement)
		}
		return nil
	}
	turn, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return BriefRefinementResult{Call: turn}, err
	}
	if turn.Response.Kind == ResponseClarificationNeeded {
		if err := input.Run.Close(briefClarificationCloseReason(turn.Response)); err != nil {
			return BriefRefinementResult{Call: turn}, fmt.Errorf("%w: close unresolved specification issue: %v", ErrBriefRefinement, err)
		}
		if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
			return BriefRefinementResult{Call: turn}, fmt.Errorf("%w: persist closed run: %v", ErrBriefRefinement, err)
		}
		return BriefRefinementResult{Call: turn, Closed: true}, nil
	}
	refined, err := PersistRefinedBriefVersion(ctx, input.Journal, input.StateStore, input.Run, call.Expectation, turn.Response)
	if err != nil {
		return BriefRefinementResult{Call: turn}, fmt.Errorf("%w: persist refined brief: %v", ErrBriefRefinement, err)
	}
	return BriefRefinementResult{Call: turn, Brief: &refined}, nil
}

func validateBriefRefinementInput(input BriefRefinementInput) error {
	if input.Owner == nil || input.Run == nil || input.StateStore == nil || input.Journal == nil || strings.TrimSpace(input.Repository) == "" || input.AssignmentID == "" || input.OperationID == "" || strings.TrimSpace(input.CallID) == "" || !validTransitionLimits(input.Limits) {
		return fmt.Errorf("%w: owner, durable state, assignment, operation, call ID, and limits are required", ErrBriefRefinement)
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
	return "unresolved material specification issue: " + strings.TrimSpace(*response.Question)
}
