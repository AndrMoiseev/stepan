package impl_loop

import (
	"encoding/json"
	"errors"
	"fmt"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

var ErrAgentOperationRecovery = errors.New("invalid implementation agent operation recovery")

// AgentOperationRecoveryState states whether durable evidence proves that an
// agent operation has completed. An interrupted operation has no result, even
// if its attempt start was written before the process died; the next
// controlled call must therefore restart it under the existing operation and
// counter rather than inventing a new semantic round.
type AgentOperationRecoveryState string

const (
	AgentOperationCompleted   AgentOperationRecoveryState = "completed"
	AgentOperationInterrupted AgentOperationRecoveryState = "interrupted"
)

// AgentOperationRecovery is the provider-neutral recovery decision for one
// durable agent operation. Response is populated only when an accepted result
// was published before its state event. Callers can then continue their
// source role with that exact response instead of repeating auxiliary work.
type AgentOperationRecovery struct {
	State    AgentOperationRecoveryState
	Response AgentResponse
	Attempts uint64
}

// RecoverAgentOperation reads only the run projection and its published
// result artifact. It never inspects provider history. ResultID is assigned by
// the controller before dispatch, so a mismatching result is a corrupted or
// wrongly routed recovery request rather than permission to perform work
// again.
func RecoverAgentOperation(journal *runstore.Run, run *implstate.Run, assignmentID implstate.AssignmentID, operationID implstate.OperationID, resultID implstate.ResultID) (AgentOperationRecovery, error) {
	if journal == nil || run == nil || operationID == "" || resultID == "" {
		return AgentOperationRecovery{}, fmt.Errorf("%w: journal, run, operation ID, and result ID are required", ErrAgentOperationRecovery)
	}
	operation, result := recoveredAgentOperation(run, assignmentID, operationID)
	if operation == nil || operation.Kind != implstate.OperationAgent {
		return AgentOperationRecovery{}, fmt.Errorf("%w: agent operation %q is not present in its durable scope", ErrAgentOperationRecovery, operationID)
	}
	if result == nil {
		return AgentOperationRecovery{State: AgentOperationInterrupted, Attempts: uint64(len(operation.Attempts))}, nil
	}
	if result.ID != resultID {
		return AgentOperationRecovery{}, fmt.Errorf("%w: operation %q has result %q, not %q", ErrAgentOperationRecovery, operationID, result.ID, resultID)
	}
	if len(result.Evidence) == 0 {
		return AgentOperationRecovery{}, fmt.Errorf("%w: completed agent operation %q has no response evidence", ErrAgentOperationRecovery, operationID)
	}
	data, err := journal.Read(result.Evidence[0])
	if err != nil {
		return AgentOperationRecovery{}, fmt.Errorf("%w: read response evidence: %v", ErrAgentOperationRecovery, err)
	}
	var response AgentResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return AgentOperationRecovery{}, fmt.Errorf("%w: decode response evidence: %v", ErrAgentOperationRecovery, err)
	}
	return AgentOperationRecovery{State: AgentOperationCompleted, Response: response, Attempts: uint64(len(operation.Attempts))}, nil
}

func recoveredAgentOperation(run *implstate.Run, assignmentID implstate.AssignmentID, operationID implstate.OperationID) (*implstate.Operation, *implstate.OperationResult) {
	if assignmentID == "" {
		var operation *implstate.Operation
		for index := range run.RunOperations {
			if run.RunOperations[index].ID == operationID {
				operation = &run.RunOperations[index]
				break
			}
		}
		for index := range run.RunResults {
			if run.RunResults[index].OperationID == operationID {
				return operation, &run.RunResults[index]
			}
		}
		return operation, nil
	}
	for assignmentIndex := range run.Assignments {
		assignment := &run.Assignments[assignmentIndex]
		if assignment.ID != assignmentID {
			continue
		}
		var operation *implstate.Operation
		for index := range assignment.Operations {
			if assignment.Operations[index].ID == operationID {
				operation = &assignment.Operations[index]
				break
			}
		}
		for index := range assignment.Results {
			if assignment.Results[index].OperationID == operationID {
				return operation, &assignment.Results[index]
			}
		}
		return operation, nil
	}
	return nil, nil
}
