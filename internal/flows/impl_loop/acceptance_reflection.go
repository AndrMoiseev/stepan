package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/git"
)

// ErrAcceptanceReflection identifies an invalid controller transition from a
// reviewed assignment to the pre-commit state, or its informational task-list
// reflection. The Markdown file is deliberately not an input to acceptance.
var ErrAcceptanceReflection = errors.New("invalid accepted-progress reflection")

// AcceptanceReflectionInput contains the controller-owned identities for the
// durable acceptance boundary and the following orchestrator turn. TasksPath
// is an exact repository-relative path: the orchestrator gets no authority
// beyond that one informational file.
type AcceptanceReflectionInput struct {
	Run        *implstate.Run
	StateStore *runstore.StateStore
	Journal    *runstore.Run
	Repository string
	Workspace  WorkspaceControl
	Session    *AgentSession

	AssignmentID implstate.AssignmentID
	Acceptance   implstate.AcceptanceEvidence
	TasksPath    string

	ReflectionOperationID implstate.OperationID
	ReflectionResultID    implstate.ResultID
	ReflectionCallID      string
	Limits                implstate.CycleLimits
	Timeout               time.Duration
}

// AcceptanceReflectionResult reports the controlled orchestrator turn. Its
// response is a receipt for an informational edit only; it never changes task
// status, acceptance evidence, or the accepted code state.
type AcceptanceReflectionResult struct {
	Call ControlledAgentCallResult
}

// AcceptAssignmentAndReflectProgress first makes the accepted-awaiting-commit
// transition durable, then asks the orchestrator to edit tasks.md directly.
// A crash or an orchestrator failure after the first record therefore leaves a
// recoverable accepted assignment, not an inferred Markdown state.
func AcceptAssignmentAndReflectProgress(ctx context.Context, input AcceptanceReflectionInput) (AcceptanceReflectionResult, error) {
	if err := validateAcceptanceReflectionInput(input); err != nil {
		return AcceptanceReflectionResult{}, err
	}
	basis := implstate.AcceptanceBasis{
		Specification: input.Run.Identity.Specification,
		Configuration: input.Run.Identity.Configuration,
	}
	assignment := assignmentByID(input.Run, input.AssignmentID)
	if assignment == nil {
		return AcceptanceReflectionResult{}, fmt.Errorf("%w: assignment is missing", ErrAcceptanceReflection)
	}
	operation := finalRunOperation(input.Run, input.ReflectionOperationID)
	if assignment.Status == implstate.AssignmentActive {
		if operation != nil {
			return AcceptanceReflectionResult{}, fmt.Errorf("%w: reflection operation already exists before acceptance", ErrAcceptanceReflection)
		}
		if err := input.Run.AcceptAssignment(input.AssignmentID, input.Acceptance); err != nil {
			return AcceptanceReflectionResult{}, fmt.Errorf("%w: accept assignment: %v", ErrAcceptanceReflection, err)
		}
	} else if assignment.Status != implstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil {
		return AcceptanceReflectionResult{}, fmt.Errorf("%w: assignment is neither review-ready nor accepted", ErrAcceptanceReflection)
	} else if !reflect.DeepEqual(*assignment.Acceptance, input.Acceptance) {
		return AcceptanceReflectionResult{}, fmt.Errorf("%w: supplied acceptance differs from durable assignment acceptance", ErrAcceptanceReflection)
	}
	if operation == nil {
		if err := input.Run.AddRunOperation(implstate.Operation{
			ID: input.ReflectionOperationID, Kind: implstate.OperationAgent,
			Basis: basis, Description: "reflect accepted task progress in tasks.md",
		}); err != nil {
			return AcceptanceReflectionResult{}, fmt.Errorf("%w: create reflection operation: %v", ErrAcceptanceReflection, err)
		}
		// This is intentionally one record before an external agent call. It has
		// both the accepted state and the reserved reflection operation, so
		// recovery can resume without treating a checkbox as proof of completion.
		if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
			return AcceptanceReflectionResult{}, fmt.Errorf("%w: persist accepted assignment: %v", ErrAcceptanceReflection, err)
		}
	} else if operation.Kind != implstate.OperationAgent || operation.Basis != basis || operation.Description != "reflect accepted task progress in tasks.md" || finalRunResultForOperation(input.Run, operation.ID) != nil {
		return AcceptanceReflectionResult{}, fmt.Errorf("%w: reflection operation cannot be resumed", ErrAcceptanceReflection)
	}

	binding := ResponseBinding{
		CallID: input.ReflectionCallID, RunID: input.Run.Identity.ID,
		Specification: input.Run.Identity.Specification,
		Configuration: input.Run.Identity.Configuration,
		TaskList:      input.Run.Identity.TaskList,
	}
	call := ControlledAgentCall{
		Session: input.Session, Repository: input.Repository, Workspace: input.Workspace,
		Policy: AgentCallPolicy{Role: AgentRoleOrchestrator, CallID: input.ReflectionCallID, AllowedPaths: []string{input.TasksPath}},
		Run:    input.Run, Journal: input.Journal, StateStore: input.StateStore,
		OperationID: input.ReflectionOperationID, Limits: input.Limits,
		Expectation: ResponseExpectation{Role: ResponseRoleOrchestrator, State: ResponseStateReflectingTasks, Scope: ResponseScopeRun, Binding: binding},
		Message:     renderAcceptedProgressReflection(input.Run, input.AssignmentID, input.TasksPath), Timeout: input.Timeout,
		ValidateResponse: func(response AgentResponse) error {
			if response.Kind == ResponseExecutionBlocked {
				_, err := ExecutionBlockFromResponse(response)
				return err
			}
			return validateProgressReflectionResponse(input.Run, response)
		},
	}
	result, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return AcceptanceReflectionResult{Call: result}, err
	}
	if result.Response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(result.Response)
		if err != nil {
			return AcceptanceReflectionResult{Call: result}, err
		}
		return AcceptanceReflectionResult{Call: result}, PersistExecutionBlock(context.WithoutCancel(ctx), input.StateStore, input.Run, block)
	}
	reflectionState, err := publishReflectionWorkspace(input.Journal, input.ReflectionResultID, result.Snapshot)
	if err != nil {
		return AcceptanceReflectionResult{Call: result}, fmt.Errorf("%w: preserve reflected tasks workspace: %v", ErrAcceptanceReflection, err)
	}
	// Do not call ObserveCodeState here. The permitted tasks.md edit is not a
	// code-state change and must not stale acceptance or require another review.
	if err := input.Run.AddRunResult(implstate.OperationResult{
		ID: input.ReflectionResultID, OperationID: input.ReflectionOperationID,
		Status: implstate.ResultSucceeded, State: input.Run.CurrentState, Basis: basis,
		Evidence: []implstate.EvidenceRef{reflectionState},
	}); err != nil {
		return AcceptanceReflectionResult{Call: result}, fmt.Errorf("%w: record reflection result: %v", ErrAcceptanceReflection, err)
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return AcceptanceReflectionResult{Call: result}, fmt.Errorf("%w: persist reflection result: %v", ErrAcceptanceReflection, err)
	}
	return AcceptanceReflectionResult{Call: result}, nil
}

func finalRunResultForOperation(run *implstate.Run, operationID implstate.OperationID) *implstate.OperationResult {
	if run == nil {
		return nil
	}
	for index := range run.RunResults {
		if run.RunResults[index].OperationID == operationID {
			return &run.RunResults[index]
		}
	}
	return nil
}

func publishReflectionWorkspace(journal *runstore.Run, resultID implstate.ResultID, snapshot git.Snapshot) (implstate.EvidenceRef, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return implstate.EvidenceRef{}, err
	}
	return journal.Publish(implstate.EvidenceID(string(resultID)+"-workspace"), data)
}

func validateAcceptanceReflectionInput(input AcceptanceReflectionInput) error {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || input.Session == nil || strings.TrimSpace(input.Repository) == "" || input.AssignmentID == "" || input.ReflectionOperationID == "" || input.ReflectionResultID == "" || strings.TrimSpace(input.ReflectionCallID) == "" || !validTransitionLimits(input.Limits) {
		return fmt.Errorf("%w: state, orchestrator, and controller identities are required", ErrAcceptanceReflection)
	}
	if input.Session.Role != ResponseRoleOrchestrator {
		return fmt.Errorf("%w: progress reflection requires an orchestrator session", ErrAcceptanceReflection)
	}
	path, err := validateCallPath(input.TasksPath)
	expected, expectedErr := selectedChangeTasksPath(input.Run.Identity.Change)
	if err != nil || expectedErr != nil || path != expected {
		return fmt.Errorf("%w: tasks path must name the selected OpenSpec tasks.md", ErrAcceptanceReflection)
	}
	return nil
}

// selectedChangeTasksPath derives the sole file the orchestrator may edit.
// Change is persisted input, so it is validated again here before it becomes a
// workspace path rather than trusting a previously parsed OpenSpec name.
func selectedChangeTasksPath(change string) (string, error) {
	if change == "" || change != strings.TrimSpace(change) || change == "." || change == ".." || strings.ContainsAny(change, "/\\") || filepath.IsAbs(change) || filepath.VolumeName(change) != "" || strings.ContainsRune(change, 0) {
		return "", errors.New("change must be one safe path component")
	}
	return "openspec/changes/" + change + "/tasks.md", nil
}

func validateProgressReflectionResponse(run *implstate.Run, response AgentResponse) error {
	if run == nil || response.Kind != ResponseProgressReflected || response.Binding.RunID != run.Identity.ID || response.Binding.AssignmentID != "" || response.Binding.BriefID != "" || response.Binding.Specification != run.Identity.Specification || response.Binding.Configuration != run.Identity.Configuration || response.Binding.TaskList != run.Identity.TaskList {
		return fmt.Errorf("%w: reflection response is not bound to the run", ErrAcceptanceReflection)
	}
	// The response is deliberately not compared to assignment task IDs or to
	// tasks.md. Its task_ids are only an agent receipt; machine status remains
	// controller-owned until a later Git commit transition.
	return nil
}

func reflectionOperationExists(run *implstate.Run, id implstate.OperationID) bool {
	for _, operation := range run.RunOperations {
		if operation.ID == id {
			return true
		}
	}
	for _, assignment := range run.Assignments {
		for _, operation := range assignment.Operations {
			if operation.ID == id {
				return true
			}
		}
	}
	return false
}

func renderAcceptedProgressReflection(run *implstate.Run, assignmentID implstate.AssignmentID, tasksPath string) string {
	var taskIDs []string
	for _, assignment := range run.Assignments {
		if assignment.ID != assignmentID {
			continue
		}
		for _, taskID := range assignment.TaskIDs {
			taskIDs = append(taskIDs, string(taskID))
		}
		break
	}
	return "Update only " + tasksPath + " to reflect the controller-accepted progress for task IDs: " + strings.Join(taskIDs, ", ") + ". Return progress_reflected when done. This edit is informational; do not infer, alter, or validate machine task status from Markdown checkboxes."
}
