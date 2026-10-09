package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/git"
)

func implementationReadyReceiptIDs(operationID implstate.OperationID) (implstate.ResultID, implstate.EvidenceID, implstate.EvidenceID) {
	receiptID, workspaceID := controlledAgentSuccessReceiptIDs(operationID)
	return implstate.ResultID(string(operationID) + "-implementation-ready-result"), receiptID, workspaceID
}

// persistImplementationReadyReceipt links the shared controlled-call receipt
// into the implementation state before mandatory checks begin.
func persistImplementationReadyReceipt(ctx context.Context, input RestartContinuationInput, assignmentID implstate.AssignmentID, operationID implstate.OperationID, response AgentResponse, snapshot git.Snapshot) error {
	if response.Kind != ResponseImplementationReady || response.Message == nil || strings.TrimSpace(*response.Message) == "" {
		return errors.New("validated implementation_ready response with a commit message is required")
	}
	durable, durableSnapshot, responseRef, workspaceRef, found, err := readControlledAgentSuccessReceipt(input.Journal, operationID)
	if err != nil || !found {
		return errors.Join(errors.New("shared controlled-call receipt is missing"), err)
	}
	if !reflect.DeepEqual(durable, response) || !reflect.DeepEqual(durableSnapshot, snapshot) {
		return errors.New("shared controlled-call receipt differs from the returned implementation_ready turn")
	}
	return recordImplementationReadyReceipt(ctx, input, assignmentID, operationID, responseRef, workspaceRef)
}

func recordImplementationReadyReceipt(ctx context.Context, input RestartContinuationInput, assignmentID implstate.AssignmentID, operationID implstate.OperationID, responseRef, workspaceRef implstate.EvidenceRef) error {
	operation := assignmentOperation(input.Run, assignmentID, operationID)
	if operation == nil || operation.Kind != implstate.OperationAgent || operation.Counter != implstate.CycleCounterNone || operation.BriefID == "" {
		return fmt.Errorf("implementation_ready receipt lacks its implementation operation %s", operationID)
	}
	response, _, durableResponseRef, durableWorkspaceRef, found, err := readControlledAgentSuccessReceipt(input.Journal, operationID)
	if err != nil || !found {
		return errors.Join(errors.New("read shared controlled-call receipt"), err)
	}
	if durableResponseRef != responseRef || durableWorkspaceRef != workspaceRef {
		return errors.New("implementation_ready evidence differs from the shared controlled-call receipt")
	}
	if err := validateImplementationReadyReceiptBinding(input.Run, assignmentID, *operation, response); err != nil {
		return err
	}
	if _, err := savedWorkspaceSnapshot(input.Journal, workspaceRef); err != nil {
		return fmt.Errorf("verify implementation_ready workspace: %w", err)
	}
	candidate, err := resumeCandidate(input.Run)
	if err != nil {
		return err
	}
	if err := candidate.ObserveCodeState(workspaceRef); err != nil {
		return fmt.Errorf("record implementation_ready workspace: %w", err)
	}
	resultID, _, _ := implementationReadyReceiptIDs(operationID)
	if err := candidate.AddResult(assignmentID, implstate.OperationResult{
		ID: resultID, OperationID: operationID, Status: implstate.ResultSucceeded,
		State: workspaceRef, Basis: operation.Basis, Evidence: []implstate.EvidenceRef{responseRef, workspaceRef},
	}); err != nil {
		return fmt.Errorf("record implementation_ready result: %w", err)
	}
	event, err := input.StateStore.Record(context.WithoutCancel(ctx), candidate)
	if event.Sequence != 0 {
		*input.Run = *candidate
	}
	if err != nil {
		return fmt.Errorf("persist implementation_ready receipt: %w", err)
	}
	return nil
}

func validateImplementationReadyReceiptBinding(run *implstate.Run, assignmentID implstate.AssignmentID, operation implstate.Operation, response AgentResponse) error {
	if run == nil || response.Kind != ResponseImplementationReady || response.Message == nil || strings.TrimSpace(*response.Message) == "" || strings.TrimSpace(response.Binding.CallID) == "" || response.Binding.RunID != run.Identity.ID || response.Binding.AssignmentID != assignmentID || response.Binding.BriefID != operation.BriefID || response.Binding.Specification != operation.Basis.Specification || response.Binding.Configuration != operation.Basis.Configuration || response.Binding.TaskList != run.Identity.TaskList {
		return errors.New("implementation_ready receipt is not bound to its durable operation")
	}
	return nil
}

func implementationReadyResponse(journal *runstore.Run, run *implstate.Run, assignmentID implstate.AssignmentID, result implstate.OperationResult) (AgentResponse, error) {
	operation := assignmentOperation(run, assignmentID, result.OperationID)
	if operation == nil || result.Status != implstate.ResultSucceeded || len(result.Evidence) < 2 {
		return AgentResponse{}, errors.New("durable implementation_ready result is incomplete")
	}
	response, _, responseRef, workspaceRef, found, err := readControlledAgentSuccessReceipt(journal, result.OperationID)
	if err != nil || !found {
		return AgentResponse{}, errors.Join(errors.New("read durable implementation_ready response"), err)
	}
	if result.Evidence[0] != responseRef || result.Evidence[1] != workspaceRef {
		return AgentResponse{}, errors.New("durable implementation_ready result does not link the accepted controlled call")
	}
	if err := validateImplementationReadyReceiptBinding(run, assignmentID, *operation, response); err != nil {
		return AgentResponse{}, err
	}
	if _, err := savedWorkspaceSnapshot(journal, result.Evidence[1]); err != nil {
		return AgentResponse{}, fmt.Errorf("verify durable implementation_ready workspace: %w", err)
	}
	return response, nil
}

func latestImplementationReadyResponse(journal *runstore.Run, run *implstate.Run, assignmentID implstate.AssignmentID) (AgentResponse, error) {
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil {
		return AgentResponse{}, errors.New("implementation_ready assignment is missing")
	}
	results := make(map[implstate.OperationID]implstate.OperationResult, len(assignment.Results))
	for _, result := range assignment.Results {
		results[result.OperationID] = result
	}
	for index := len(assignment.Operations) - 1; index >= 0; index-- {
		operation := assignment.Operations[index]
		if operation.Kind != implstate.OperationAgent || operation.Counter != implstate.CycleCounterNone || operation.Episode != "" {
			continue
		}
		result, ok := results[operation.ID]
		if !ok || len(result.Evidence) < 2 {
			continue
		}
		return implementationReadyResponse(journal, run, assignmentID, result)
	}
	return AgentResponse{}, errors.New("durable implementation_ready response is missing")
}

func rebindImplementationReadyResponse(run *implstate.Run, assignmentID implstate.AssignmentID, response AgentResponse) AgentResponse {
	assignment := assignmentByID(run, assignmentID)
	response.Binding.RunID = run.Identity.ID
	response.Binding.AssignmentID = assignmentID
	response.Binding.Specification = run.Identity.Specification
	response.Binding.Configuration = run.Identity.Configuration
	response.Binding.TaskList = run.Identity.TaskList
	if assignment != nil && len(assignment.Briefs) != 0 {
		response.Binding.BriefID = assignment.Briefs[len(assignment.Briefs)-1].ID
	}
	return response
}
