package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
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
	Run        *implementationstate.Run
	StateStore *runstore.StateStore
	Journal    *runstore.Run
	Repository string
	Workspace  WorkspaceControl
	Session    *AgentSession

	AssignmentID implementationstate.AssignmentID
	Acceptance   implementationstate.AcceptanceEvidence
	TasksPath    string

	ReflectionOperationID implementationstate.OperationID
	ReflectionResultID    implementationstate.ResultID
	ReflectionCallID      string
	Limits                implementationstate.CycleLimits
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
	basis := implementationstate.AcceptanceBasis{
		Specification: input.Run.Identity.Specification,
		Configuration: input.Run.Identity.Configuration,
	}
	if reflectionOperationExists(input.Run, input.ReflectionOperationID) {
		return AcceptanceReflectionResult{}, fmt.Errorf("%w: reflection operation already exists", ErrAcceptanceReflection)
	}
	if err := input.Run.AcceptAssignment(input.AssignmentID, input.Acceptance); err != nil {
		return AcceptanceReflectionResult{}, fmt.Errorf("%w: accept assignment: %v", ErrAcceptanceReflection, err)
	}
	if err := input.Run.AddRunOperation(implementationstate.Operation{
		ID: input.ReflectionOperationID, Kind: implementationstate.OperationAgent,
		Basis: basis, Description: "reflect accepted task progress in tasks.md",
	}); err != nil {
		return AcceptanceReflectionResult{}, fmt.Errorf("%w: create reflection operation: %v", ErrAcceptanceReflection, err)
	}
	// This is intentionally one record before an external agent call. It has
	// both the pending commit intent and the reserved reflection operation, so
	// recovery can resume without treating a checkbox as proof of completion.
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return AcceptanceReflectionResult{}, fmt.Errorf("%w: persist accepted assignment: %v", ErrAcceptanceReflection, err)
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
	// Do not call ObserveCodeState here. The permitted tasks.md edit is not a
	// code-state change and must not stale acceptance or require another review.
	if err := input.Run.AddRunResult(implementationstate.OperationResult{
		ID: input.ReflectionResultID, OperationID: input.ReflectionOperationID,
		Status: implementationstate.ResultSucceeded, State: input.Run.CurrentState, Basis: basis,
	}); err != nil {
		return AcceptanceReflectionResult{Call: result}, fmt.Errorf("%w: record reflection result: %v", ErrAcceptanceReflection, err)
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return AcceptanceReflectionResult{Call: result}, fmt.Errorf("%w: persist reflection result: %v", ErrAcceptanceReflection, err)
	}
	return AcceptanceReflectionResult{Call: result}, nil
}

func validateAcceptanceReflectionInput(input AcceptanceReflectionInput) error {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || input.Session == nil || strings.TrimSpace(input.Repository) == "" || input.AssignmentID == "" || input.ReflectionOperationID == "" || input.ReflectionResultID == "" || strings.TrimSpace(input.ReflectionCallID) == "" || !validTransitionLimits(input.Limits) {
		return fmt.Errorf("%w: state, orchestrator, and controller identities are required", ErrAcceptanceReflection)
	}
	if input.Session.Role != ResponseRoleOrchestrator {
		return fmt.Errorf("%w: progress reflection requires an orchestrator session", ErrAcceptanceReflection)
	}
	path, err := validateCallPath(input.TasksPath)
	if err != nil || filepath.Base(filepath.FromSlash(path)) != "tasks.md" || !strings.HasPrefix(path, "openspec/changes/") {
		return fmt.Errorf("%w: tasks path must name the selected OpenSpec tasks.md", ErrAcceptanceReflection)
	}
	return nil
}

func validateProgressReflectionResponse(run *implementationstate.Run, response AgentResponse) error {
	if run == nil || response.Kind != ResponseProgressReflected || response.Binding.RunID != run.Identity.ID || response.Binding.AssignmentID != "" || response.Binding.BriefID != "" || response.Binding.Specification != run.Identity.Specification || response.Binding.Configuration != run.Identity.Configuration || response.Binding.TaskList != run.Identity.TaskList {
		return fmt.Errorf("%w: reflection response is not bound to the run", ErrAcceptanceReflection)
	}
	// The response is deliberately not compared to assignment task IDs or to
	// tasks.md. Its task_ids are only an agent receipt; machine status remains
	// controller-owned until a later Git commit transition.
	return nil
}

func reflectionOperationExists(run *implementationstate.Run, id implementationstate.OperationID) bool {
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

func renderAcceptedProgressReflection(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, tasksPath string) string {
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
