package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// ErrBriefSelection identifies a response or controller boundary that cannot
// safely create the next assignment.
var ErrBriefSelection = errors.New("invalid briefer assignment selection")

// PrepareBriefSelection records the run-level operation that asks a briefer to
// choose the next assignment. The operation is deliberately run-scoped: there
// is no assignment to bind until the controller has accepted the response.
// Its durable attempt is subsequently reserved by ExecuteBriefSelection.
func PrepareBriefSelection(ctx context.Context, stateStore *runstore.StateStore, run *implementationstate.Run, operationID implementationstate.OperationID) error {
	if stateStore == nil || run == nil || strings.TrimSpace(string(operationID)) == "" {
		return fmt.Errorf("%w: state, run, and operation are required", ErrBriefSelection)
	}
	if run.Status != implementationstate.RunActive || run.TaskExtractionPending || len(run.PendingLeafTasks()) == 0 || hasOpenBriefSelection(run) {
		return fmt.Errorf("%w: no next assignment can be selected", ErrBriefSelection)
	}
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	existing := finalRunOperation(run, operationID)
	if existing == nil {
		if err := run.AddRunOperation(implementationstate.Operation{
			ID: operationID, Kind: implementationstate.OperationAgent, Basis: basis,
			Description: "select next assignment", Counter: implementationstate.CycleCounterNone,
		}); err != nil {
			return fmt.Errorf("%w: create briefer operation: %v", ErrBriefSelection, err)
		}
		if _, err := stateStore.Record(ctx, run); err != nil {
			return fmt.Errorf("%w: persist briefer operation: %v", ErrBriefSelection, err)
		}
	} else if existing.Kind != implementationstate.OperationAgent || existing.Basis != basis || existing.Description != "select next assignment" || existing.Counter != implementationstate.CycleCounterNone || finalRunResultForOperation(run, existing.ID) != nil {
		return fmt.Errorf("%w: briefer selection operation cannot be resumed", ErrBriefSelection)
	}
	return nil
}

// BriefSelectionResult exposes the controller-generated identity and the
// immutable task relationship that was persisted after accepting a briefer
// response. It intentionally does not persist the brief document; that is a
// separate versioning boundary.
type BriefSelectionResult struct {
	Call         ControlledAgentCallResult
	AssignmentID implementationstate.AssignmentID
	TaskIDs      []implementationstate.TaskID
}

// BriefSelectionCall is an initial briefer invocation constructed by the
// controller. Its controlled call is private so ordinary production callers
// cannot run selection with a hand-built session or message.
type BriefSelectionCall struct {
	assignmentID implementationstate.AssignmentID
	call         ControlledAgentCall
}

// BriefSelectionCallInput names the controller-owned values for a selection
// operation. Session and message are deliberately absent: NewBriefSelectionCall
// obtains the former from the typed briefer bootstrap and fixes the latter.
type BriefSelectionCallInput struct {
	// AssignmentID is allocated by the controller before the briefer session
	// starts. It binds selection, the eventual durable assignment, and later
	// assignment-scoped briefer refinements to one lifecycle.
	AssignmentID     implementationstate.AssignmentID
	Repository       string
	Workspace        WorkspaceControl
	Policy           AgentCallPolicy
	Run              *implementationstate.Run
	Journal          *runstore.Run
	StateStore       *runstore.StateStore
	OperationID      implementationstate.OperationID
	Limits           implementationstate.CycleLimits
	Expectation      ResponseExpectation
	Timeout          time.Duration
	ValidateResponse func(AgentResponse) error
}

const briefSelectionMessage = "Select the next assignment from the controller-supplied complete specification, machine task list, statuses, and progress. Return a self-contained brief with the selected non-empty contiguous prefix of pending leaf tasks."

// NewBriefSelectionCall builds the only production call path for initial
// selection. It first constructs the complete, evidence-backed briefer
// context, then starts the typed briefer session with that context.
func NewBriefSelectionCall(ctx context.Context, owner *SessionOwner, input BriefSelectionCallInput) (BriefSelectionCall, error) {
	if owner == nil {
		return BriefSelectionCall{}, fmt.Errorf("%w: session owner is required", ErrBriefSelection)
	}
	if strings.TrimSpace(string(input.AssignmentID)) == "" {
		return BriefSelectionCall{}, fmt.Errorf("%w: assignment ID is required before starting briefer", ErrBriefSelection)
	}
	if input.Run == nil || hasOpenBriefSelection(input.Run) {
		return BriefSelectionCall{}, fmt.Errorf("%w: no next assignment can be selected", ErrBriefSelection)
	}
	for _, assignment := range input.Run.Assignments {
		if assignment.ID == input.AssignmentID {
			return BriefSelectionCall{}, fmt.Errorf("%w: assignment identity already exists", ErrBriefSelection)
		}
	}
	start, err := BuildBrieferStartContext(input.Journal, input.Run, input.AssignmentID)
	if err != nil {
		return BriefSelectionCall{}, fmt.Errorf("%w: build briefer start context: %v", ErrBriefSelection, err)
	}
	session, err := owner.Briefer(ctx, input.AssignmentID, start)
	if err != nil {
		return BriefSelectionCall{}, fmt.Errorf("%w: start briefer session: %v", ErrBriefSelection, err)
	}
	call := ControlledAgentCall{
		Session: session, Repository: input.Repository, Workspace: input.Workspace, Policy: input.Policy, Run: input.Run,
		Journal: input.Journal, StateStore: input.StateStore, OperationID: input.OperationID,
		Limits: input.Limits, Expectation: input.Expectation, Message: briefSelectionMessage,
		Timeout: input.Timeout, ValidateResponse: input.ValidateResponse,
	}
	if err := validateBriefSelectionCall(input.AssignmentID, call); err != nil {
		return BriefSelectionCall{}, err
	}
	return BriefSelectionCall{assignmentID: input.AssignmentID, call: call}, nil
}

// ExecuteBriefSelection invokes the initial, run-scoped briefer response with
// the normal controlled-call retry semantics. Only after binding and prefix
// validation succeeds does it create and durably record the stable assignment.
func ExecuteBriefSelection(ctx context.Context, selection BriefSelectionCall) (BriefSelectionResult, error) {
	assignmentID := selection.assignmentID
	call := selection.call
	if err := validateBriefSelectionCall(assignmentID, call); err != nil {
		return BriefSelectionResult{}, err
	}
	callerValidation := call.ValidateResponse
	call.ValidateResponse = func(response AgentResponse) error {
		if response.Kind == ResponseExecutionBlocked {
			_, err := ExecutionBlockFromResponse(response)
			return err
		}
		if err := validateBriefSelectionResponse(call.Run, call.Expectation, response); err != nil {
			return err
		}
		if callerValidation != nil {
			return callerValidation(response)
		}
		return nil
	}
	result, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return BriefSelectionResult{Call: result}, err
	}
	if result.Response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(result.Response)
		if err != nil {
			return BriefSelectionResult{Call: result}, err
		}
		if err := PersistExecutionBlock(ctx, call.StateStore, call.Run, block); err != nil {
			return BriefSelectionResult{Call: result}, err
		}
		return BriefSelectionResult{Call: result}, nil
	}
	if err := call.Run.StartAssignment(assignmentID, result.Response.TaskIDs); err != nil {
		return BriefSelectionResult{Call: result}, fmt.Errorf("%w: create assignment: %v", ErrBriefSelection, err)
	}
	if _, err := PersistBriefVersion(ctx, call.Journal, call.StateStore, call.Run, assignmentID, result.Response.TaskIDs, *result.Response.Brief); err != nil {
		return BriefSelectionResult{Call: result}, fmt.Errorf("%w: persist initial brief: %v", ErrBriefSelection, err)
	}
	return BriefSelectionResult{Call: result, AssignmentID: assignmentID, TaskIDs: slices.Clone(result.Response.TaskIDs)}, nil
}

func validateBriefSelectionCall(assignmentID implementationstate.AssignmentID, call ControlledAgentCall) error {
	if strings.TrimSpace(string(assignmentID)) == "" || call.Run == nil || call.AssignmentID != "" || call.Expectation.Role != ResponseRoleBriefer || call.Expectation.State != ResponseStateInitialBriefing || call.Expectation.Scope != ResponseScopeRun || hasOpenBriefSelection(call.Run) {
		return fmt.Errorf("%w: invalid initial briefer call", ErrBriefSelection)
	}
	if call.Policy.Role != AgentRoleBriefer {
		return fmt.Errorf("%w: briefer must use its read-only policy", ErrBriefSelection)
	}
	for _, assignment := range call.Run.Assignments {
		if assignment.ID == assignmentID {
			return fmt.Errorf("%w: assignment identity already exists", ErrBriefSelection)
		}
	}
	return nil
}

func validateBriefSelectionResponse(run *implementationstate.Run, expectation ResponseExpectation, response AgentResponse) error {
	if run == nil || response.Kind != ResponseBriefReady || response.Binding != expectation.Binding || response.Binding.RunID != run.Identity.ID || response.Binding.AssignmentID != "" || response.Binding.BriefID != "" || response.Binding.Specification != run.Identity.Specification || response.Binding.Configuration != run.Identity.Configuration || response.Binding.TaskList != run.Identity.TaskList {
		return fmt.Errorf("%w: response is not bound to the current run inputs", ErrBriefSelection)
	}
	if hasOpenBriefSelection(run) {
		return fmt.Errorf("%w: another assignment is already active", ErrBriefSelection)
	}
	pending := run.PendingLeafTasks()
	if len(response.TaskIDs) == 0 || len(response.TaskIDs) > len(pending) || !slices.Equal(response.TaskIDs, pending[:len(response.TaskIDs)]) {
		return fmt.Errorf("%w: task IDs must be a non-empty contiguous prefix of pending leaf tasks", ErrBriefSelection)
	}
	return nil
}

func hasOpenBriefSelection(run *implementationstate.Run) bool {
	if run == nil {
		return true
	}
	for _, assignment := range run.Assignments {
		if assignment.Status != implementationstate.AssignmentCommitted {
			return true
		}
	}
	return false
}
