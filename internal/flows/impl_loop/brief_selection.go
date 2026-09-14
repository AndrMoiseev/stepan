package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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
	if err := run.AddRunOperation(implementationstate.Operation{
		ID: operationID, Kind: implementationstate.OperationAgent, Basis: basis,
		Description: "select next assignment", Counter: implementationstate.CycleCounterNone,
	}); err != nil {
		return fmt.Errorf("%w: create briefer operation: %v", ErrBriefSelection, err)
	}
	if _, err := stateStore.Record(ctx, run); err != nil {
		return fmt.Errorf("%w: persist briefer operation: %v", ErrBriefSelection, err)
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

// ExecuteBriefSelection invokes the initial, run-scoped briefer response with
// the normal controlled-call retry semantics. Only after binding and prefix
// validation succeeds does it create and durably record the stable assignment.
func ExecuteBriefSelection(ctx context.Context, assignmentID implementationstate.AssignmentID, call ControlledAgentCall) (BriefSelectionResult, error) {
	if err := validateBriefSelectionCall(assignmentID, call); err != nil {
		return BriefSelectionResult{}, err
	}
	callerValidation := call.ValidateResponse
	call.ValidateResponse = func(response AgentResponse) error {
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
	if err := call.Run.StartAssignment(assignmentID, result.Response.TaskIDs); err != nil {
		return BriefSelectionResult{Call: result}, fmt.Errorf("%w: create assignment: %v", ErrBriefSelection, err)
	}
	if _, err := call.StateStore.Record(ctx, call.Run); err != nil {
		return BriefSelectionResult{Call: result}, fmt.Errorf("%w: persist assignment: %v", ErrBriefSelection, err)
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
