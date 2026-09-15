package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func implementationReadyReceiptIDs(operationID implementationstate.OperationID) (implementationstate.ResultID, implementationstate.EvidenceID, implementationstate.EvidenceID) {
	stem := string(operationID) + "-implementation-ready"
	return implementationstate.ResultID(stem + "-result"), implementationstate.EvidenceID(stem + "-response"), implementationstate.EvidenceID(stem + "-workspace")
}

// persistImplementationReadyReceipt makes the validated provider answer and
// its post-turn workspace durable before mandatory checks begin. Publication
// deliberately precedes the state event, allowing recovery to finish this
// exact transition without repeating a completed provider turn.
func persistImplementationReadyReceipt(ctx context.Context, input RestartContinuationInput, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID, response AgentResponse, snapshot gitsnapshot.Snapshot) error {
	if response.Kind != ResponseImplementationReady || response.Message == nil || strings.TrimSpace(*response.Message) == "" {
		return errors.New("validated implementation_ready response with a commit message is required")
	}
	_, responseID, workspaceID := implementationReadyReceiptIDs(operationID)
	responseData, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode implementation_ready response: %w", err)
	}
	responseRef, err := input.Journal.Publish(responseID, responseData)
	if err != nil {
		return fmt.Errorf("publish implementation_ready response: %w", err)
	}
	workspaceData, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode implementation_ready workspace: %w", err)
	}
	workspaceRef, err := input.Journal.Publish(workspaceID, workspaceData)
	if err != nil {
		return fmt.Errorf("publish implementation_ready workspace: %w", err)
	}
	return recordImplementationReadyReceipt(ctx, input, assignmentID, operationID, responseRef, workspaceRef)
}

// recoverPublishedImplementationReady completes the publication-before-event
// boundary. A partial publication is ambiguous and must be paused by the
// caller; it is never permission to issue a second semantic request.
func recoverPublishedImplementationReady(ctx context.Context, input RestartContinuationInput, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID) (bool, error) {
	resultID, responseID, workspaceID := implementationReadyReceiptIDs(operationID)
	if result := assignmentResultForOperation(input.Run, assignmentID, operationID); result != nil {
		if result.ID != resultID {
			return true, fmt.Errorf("implementation operation %s has an unexpected result %s", operationID, result.ID)
		}
		_, err := implementationReadyResponse(input.Journal, input.Run, assignmentID, *result)
		return true, err
	}
	responseRef, responseErr := input.Journal.PublishedReference(responseID)
	workspaceRef, workspaceErr := input.Journal.PublishedReference(workspaceID)
	if errors.Is(responseErr, runstore.ErrReferenceUnavailable) && errors.Is(workspaceErr, runstore.ErrReferenceUnavailable) {
		return false, nil
	}
	if responseErr != nil || workspaceErr != nil {
		return true, fmt.Errorf("implementation_ready receipt is incomplete: response=%v workspace=%v", responseErr, workspaceErr)
	}
	if err := recordImplementationReadyReceipt(ctx, input, assignmentID, operationID, responseRef, workspaceRef); err != nil {
		return true, err
	}
	return true, nil
}

func recordImplementationReadyReceipt(ctx context.Context, input RestartContinuationInput, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID, responseRef, workspaceRef implementationstate.EvidenceRef) error {
	operation := assignmentOperation(input.Run, assignmentID, operationID)
	if operation == nil || operation.Kind != implementationstate.OperationAgent || operation.Counter != implementationstate.CycleCounterNone || operation.BriefID == "" {
		return fmt.Errorf("implementation_ready receipt lacks its implementation operation %s", operationID)
	}
	responseData, err := input.Journal.Read(responseRef)
	if err != nil {
		return err
	}
	var response AgentResponse
	if err := json.Unmarshal(responseData, &response); err != nil {
		return fmt.Errorf("decode implementation_ready response: %w", err)
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
	if err := candidate.AddResult(assignmentID, implementationstate.OperationResult{
		ID: resultID, OperationID: operationID, Status: implementationstate.ResultSucceeded,
		State: workspaceRef, Basis: operation.Basis, Evidence: []implementationstate.EvidenceRef{responseRef, workspaceRef},
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

func validateImplementationReadyReceiptBinding(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, operation implementationstate.Operation, response AgentResponse) error {
	if run == nil || response.Kind != ResponseImplementationReady || response.Message == nil || strings.TrimSpace(*response.Message) == "" || strings.TrimSpace(response.Binding.CallID) == "" || response.Binding.RunID != run.Identity.ID || response.Binding.AssignmentID != assignmentID || response.Binding.BriefID != operation.BriefID || response.Binding.Specification != operation.Basis.Specification || response.Binding.Configuration != operation.Basis.Configuration || response.Binding.TaskList != run.Identity.TaskList {
		return errors.New("implementation_ready receipt is not bound to its durable operation")
	}
	return nil
}

func implementationReadyResponse(journal *runstore.Run, run *implementationstate.Run, assignmentID implementationstate.AssignmentID, result implementationstate.OperationResult) (AgentResponse, error) {
	operation := assignmentOperation(run, assignmentID, result.OperationID)
	if operation == nil || result.Status != implementationstate.ResultSucceeded || len(result.Evidence) < 2 {
		return AgentResponse{}, errors.New("durable implementation_ready result is incomplete")
	}
	data, err := journal.Read(result.Evidence[0])
	if err != nil {
		return AgentResponse{}, err
	}
	var response AgentResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return AgentResponse{}, fmt.Errorf("decode durable implementation_ready response: %w", err)
	}
	if err := validateImplementationReadyReceiptBinding(run, assignmentID, *operation, response); err != nil {
		return AgentResponse{}, err
	}
	if _, err := savedWorkspaceSnapshot(journal, result.Evidence[1]); err != nil {
		return AgentResponse{}, fmt.Errorf("verify durable implementation_ready workspace: %w", err)
	}
	return response, nil
}

func latestImplementationReadyResponse(journal *runstore.Run, run *implementationstate.Run, assignmentID implementationstate.AssignmentID) (AgentResponse, error) {
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil {
		return AgentResponse{}, errors.New("implementation_ready assignment is missing")
	}
	results := make(map[implementationstate.OperationID]implementationstate.OperationResult, len(assignment.Results))
	for _, result := range assignment.Results {
		results[result.OperationID] = result
	}
	for index := len(assignment.Operations) - 1; index >= 0; index-- {
		operation := assignment.Operations[index]
		if operation.Kind != implementationstate.OperationAgent || operation.Counter != implementationstate.CycleCounterNone || operation.Episode != "" {
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

func rebindImplementationReadyResponse(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, response AgentResponse) AgentResponse {
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
