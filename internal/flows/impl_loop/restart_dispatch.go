package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/openspec"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var ErrRestartContinuation = errors.New("implementation restart continuation failed")

// RestartContinuationInput contains the fresh process-owned resources and the
// inputs established by the successful /resume gate. No provider history is
// accepted. Dispatch reconstructs role contexts from durable evidence and
// executes one real controller transition; it never leaves an active run idle
// merely because provider threads were successfully created.
type RestartContinuationInput struct {
	Owner          *SessionOwner
	Journal        *runstore.Run
	StateStore     *runstore.StateStore
	Run            *implementationstate.Run
	Repository     string
	Workspace      WorkspaceControl
	Runner         CheckRunner
	UserControl    *UserRunControl
	Configuration  implementationconfig.Configuration
	Checks         implementationconfig.CheckSelection
	Rules          implementationconfig.RulesFileValidation
	ProtectedPaths []string
}

// DispatchRestartContinuation restores the orchestrator plus every applicable
// assignment conversation and then runs the next deterministic durable route.
// States whose continuation cannot be reconstructed without an unavailable
// controller fact are paused with an actionable diagnostic instead of being
// reported as resumed while doing no work.
func DispatchRestartContinuation(ctx context.Context, input RestartContinuationInput) error {
	if input.Owner == nil || input.Journal == nil || input.StateStore == nil || input.Run == nil || strings.TrimSpace(input.Repository) == "" || input.Runner == nil {
		return fmt.Errorf("%w: owner, journal, state store, run, repository, and runner are required", ErrRestartContinuation)
	}
	limitsConfig, err := input.Configuration.ResolveLimits()
	if err != nil {
		return pauseRestartContinuation(ctx, input, "resolve controller limits", err)
	}
	limits := CycleLimits(limitsConfig)
	rules, err := BuildRulesIndex(input.Repository, input.Rules)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "rebuild current rules context", err)
	}
	catalog := restartCheckCatalog(input.Checks)
	pkg, orchestrator, err := restoreRestartOrchestrator(ctx, input)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "restore orchestrator session", err)
	}

	switch {
	case input.Run.TaskExtractionPending:
		return runRestartTaskExtraction(ctx, input, orchestrator, pkg, limits)
	case input.Run.InitialBaselineStatus == implementationstate.InitialBaselinePending:
		operation, result := nextRestartIDs(input.Run, "initial-required-checks")
		_, err := RunInitialRequiredChecks(ctx, InitialRequiredChecks{
			Run: input.Run, Workspace: input.Workspace, StateStore: input.StateStore, Journal: input.Journal,
			Repository: input.Repository, Selection: input.Checks, Runner: input.Runner, UserControl: input.UserControl,
			MaxCycles: limitsConfig.RequiredCheckAttempts, ProtectedPaths: input.ProtectedPaths, Operation: operation, Result: result,
		})
		return restartRouteError(ctx, input, "run initial required checks", err)
	case pendingCommitAssignment(input.Run) != "":
		assignment := pendingCommitAssignment(input.Run)
		reconciliation, err := ReconcilePendingCommit(ctx, ReconcilePendingCommitInput{Run: input.Run, StateStore: input.StateStore, Repository: input.Repository, AssignmentID: assignment})
		if err != nil {
			return restartRouteError(ctx, input, "reconcile pending assignment commit", err)
		}
		if reconciliation.Adopted {
			return nil
		}
		return pauseRestartContinuation(ctx, input, "continue pending assignment commit", errors.New("the durable commit intent is safe to retry, but the accepted implementation response needed to authorize a new Git commit is not retained"))
	case activeAssignment(input.Run) != "":
		return runRestartAssignment(ctx, input, activeAssignment(input.Run), rules, catalog, limits, time.Duration(limitsConfig.AgentTimeoutSeconds)*time.Second)
	case len(input.Run.PendingLeafTasks()) != 0:
		return runRestartBriefSelection(ctx, input, limits, time.Duration(limitsConfig.AgentTimeoutSeconds)*time.Second)
	default:
		return pauseRestartContinuation(ctx, input, "dispatch final acceptance", errors.New("the saved state requires the final-acceptance coordinator, which has no complete durable round identity at this boundary"))
	}
}

func restoreRestartOrchestrator(ctx context.Context, input RestartContinuationInput) (openspec.Package, *AgentSession, error) {
	pkg, err := openspec.Load(input.Repository, input.Run.Identity.Change)
	if err != nil {
		return openspec.Package{}, nil, err
	}
	tasks, err := json.Marshal(input.Run.Tasks)
	if err != nil {
		return openspec.Package{}, nil, err
	}
	state, err := json.Marshal(input.Run)
	if err != nil {
		return openspec.Package{}, nil, err
	}
	start, err := BuildOrchestratorStartContext(OrchestratorStartInput{OpenSpecPackage: pkg.CompleteSpecification(), MachineTaskList: string(tasks), RunState: string(state)})
	if err != nil {
		return openspec.Package{}, nil, err
	}
	session, err := input.Owner.Restore(ctx, SessionRestore{Role: ResponseRoleOrchestrator, Start: start})
	return pkg, session, err
}

func runRestartTaskExtraction(ctx context.Context, input RestartContinuationInput, session *AgentSession, _ openspec.Package, limits implementationstate.CycleLimits) error {
	operation := latestIncompleteRunAgentOperation(input.Run)
	if operation == nil {
		return pauseRestartContinuation(ctx, input, "continue task extraction", errors.New("pending task extraction has no durable unfinished orchestrator operation"))
	}
	callID := restartOperationCallID(operation)
	binding := ResponseBinding{CallID: callID, RunID: input.Run.Identity.ID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
	_, err := ExecuteInitialTaskExtraction(ctx, ControlledAgentCall{
		Session: session, UserControl: input.UserControl, Repository: input.Repository, Workspace: input.Workspace,
		Policy: AgentCallPolicy{Role: AgentRoleOrchestrator, CallID: callID}, Run: input.Run, Journal: input.Journal,
		StateStore: input.StateStore, OperationID: operation.ID, Limits: limits,
		Expectation: ResponseExpectation{Role: ResponseRoleOrchestrator, State: ResponseStateExtractingTasks, Scope: ResponseScopeRun, Binding: binding},
		Message:     "Continue the interrupted task extraction from the complete durable run context and return the formal ordered task hierarchy.",
	})
	return restartRouteError(ctx, input, "continue task extraction", err)
}

func runRestartBriefSelection(ctx context.Context, input RestartContinuationInput, limits implementationstate.CycleLimits, timeout time.Duration) error {
	operation, _ := nextRestartIDs(input.Run, "select-assignment")
	assignmentID := nextRestartAssignmentID(input.Run)
	if err := PrepareBriefSelection(ctx, input.StateStore, input.Run, operation); err != nil {
		return restartRouteError(ctx, input, "prepare next assignment selection", err)
	}
	callID := string(operation) + "-call-1"
	binding := ResponseBinding{CallID: callID, RunID: input.Run.Identity.ID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
	selection, err := NewBriefSelectionCall(ctx, input.Owner, BriefSelectionCallInput{
		AssignmentID: assignmentID, Repository: input.Repository, Workspace: input.Workspace,
		Policy: AgentCallPolicy{Role: AgentRoleBriefer, CallID: callID}, Run: input.Run, Journal: input.Journal,
		StateStore: input.StateStore, OperationID: operation, Limits: limits, Timeout: timeout,
		Expectation: ResponseExpectation{Role: ResponseRoleBriefer, State: ResponseStateInitialBriefing, Scope: ResponseScopeRun, Binding: binding},
	})
	if err != nil {
		return pauseRestartContinuation(ctx, input, "restore briefer for next assignment", err)
	}
	_, err = ExecuteBriefSelection(ctx, selection)
	return restartRouteError(ctx, input, "select next assignment", err)
}

func runRestartAssignment(ctx context.Context, input RestartContinuationInput, assignmentID implementationstate.AssignmentID, rules RulesIndex, catalog []CheckCatalogEntry, limits implementationstate.CycleLimits, timeout time.Duration) error {
	brieferStart, err := BuildBrieferStartContext(input.Journal, input.Run, assignmentID)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "rebuild active assignment briefer context", err)
	}
	if _, err := input.Owner.Restore(ctx, SessionRestore{Role: ResponseRoleBriefer, AssignmentID: assignmentID, Start: brieferStart.roleStartContext()}); err != nil {
		return pauseRestartContinuation(ctx, input, "restore active assignment briefer", err)
	}
	implementerStart, _, err := BuildCurrentTaskRoleContexts(input.Journal, input.Run, assignmentID, rules, catalog)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "rebuild active assignment role contexts", err)
	}
	implementer, err := input.Owner.Restore(ctx, SessionRestore{Role: ResponseRoleImplementer, AssignmentID: assignmentID, Start: implementerStart})
	if err != nil {
		return pauseRestartContinuation(ctx, input, "restore active assignment implementer", err)
	}

	action, interruptedOperation, err := classifyRestartAssignmentAction(input.Run, assignmentID)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "classify active assignment continuation", err)
	}
	if action == restartAssignmentAwaitingAcceptance {
		return pauseRestartContinuation(ctx, input, "continue reviewed assignment acceptance", errors.New("the passed task review is durable, but the accepted implementation response needed by reflection and commit is not retained at this restart boundary"))
	}
	if action == restartAssignmentReview {
		brief, err := currentAssignmentBrief(input.Journal, input.Run, assignmentID)
		if err != nil {
			return pauseRestartContinuation(ctx, input, "rebuild active assignment review brief", err)
		}
		diff, err := effectiveWorkspaceControl(input.Workspace).AssignmentDiff(ctx, input.Repository, assignmentDiffBase(input.Run, assignmentID))
		if err != nil {
			return pauseRestartContinuation(ctx, input, "rebuild active assignment review diff", err)
		}
		checkEvidence, err := currentRequiredCheckEvidence(input.Journal, input.Run, assignmentID)
		if err != nil {
			return pauseRestartContinuation(ctx, input, "rebuild active assignment check evidence", err)
		}
		reviewerStart, err := BuildTaskReviewerReviewContext(TaskReviewerStartInput{
			TaskRoleStartInput: TaskRoleStartInput{AssignmentID: assignmentID, BriefID: brief.ID, Brief: brief.Text, Rules: rules, Checks: catalog},
			AssignmentDiff:     diff, RequiredCheckResults: checkEvidence, PreviousDiscussion: renderTaskReviewHistory(input.Run, assignmentID),
		})
		if err != nil {
			return pauseRestartContinuation(ctx, input, "rebuild active assignment reviewer context", err)
		}
		if _, err := input.Owner.Restore(ctx, SessionRestore{Role: ResponseRoleTaskReviewer, AssignmentID: assignmentID, Start: reviewerStart}); err != nil {
			return pauseRestartContinuation(ctx, input, "restore active assignment task reviewer", err)
		}
		operation, result := nextRestartIDs(input.Run, "task-review")
		_, err = StartTaskReview(ctx, TaskReviewInput{
			Owner: input.Owner, Workspace: input.Workspace, Run: input.Run, StateStore: input.StateStore, Journal: input.Journal,
			Repository: input.Repository, AssignmentID: assignmentID, Rules: rules, Checks: catalog,
			OperationID: operation, ResultID: result, CallID: string(operation) + "-call-1", Limits: limits, Timeout: timeout,
		})
		return restartRouteError(ctx, input, "review active assignment", err)
	}

	assignment := assignmentByID(input.Run, assignmentID)
	if assignment == nil || len(assignment.Briefs) == 0 {
		return pauseRestartContinuation(ctx, input, "continue active assignment", errors.New("active assignment has no durable current brief"))
	}
	briefID := assignment.Briefs[len(assignment.Briefs)-1].ID
	operation, _ := nextRestartIDs(input.Run, "implement")
	basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	if interruptedOperation != nil {
		operation = interruptedOperation.ID
	} else {
		if err := input.Run.AddOperation(assignmentID, implementationstate.Operation{ID: operation, Kind: implementationstate.OperationAgent, BriefID: briefID, Basis: basis, Description: "continue implementation after restart"}); err != nil {
			return pauseRestartContinuation(ctx, input, "prepare active assignment implementation", err)
		}
		if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
			return pauseRestartContinuation(ctx, input, "persist active assignment implementation operation", err)
		}
	}
	currentOperation := startupAssignmentOperation(input.Run, assignmentID, operation)
	callID := restartOperationCallID(currentOperation)
	binding := ResponseBinding{CallID: callID, RunID: input.Run.Identity.ID, AssignmentID: assignmentID, BriefID: briefID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
	turn, err := InvokeControlledAgentCall(ctx, ControlledAgentCall{
		Session: implementer, UserControl: input.UserControl, Repository: input.Repository, Workspace: input.Workspace,
		Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: callID}, Run: input.Run, Journal: input.Journal, StateStore: input.StateStore,
		AssignmentID: assignmentID, OperationID: operation, Limits: limits, Timeout: timeout,
		Expectation: ResponseExpectation{Role: ResponseRoleImplementer, State: ResponseStateImplementing, Scope: ResponseScopeAssignment, Binding: binding},
		Message:     restartImplementerMessage(input, assignment),
		ValidateResponse: func(response AgentResponse) error {
			if response.Kind == ResponseExecutionBlocked {
				_, err := ExecutionBlockFromResponse(response)
				return err
			}
			return ValidateImplementerTransitionResponse(input.Checks, input.Run, assignmentID, briefID, response)
		},
	})
	if err != nil {
		return restartRouteError(ctx, input, "continue active assignment implementation", err)
	}
	if turn.Response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(turn.Response)
		if err == nil {
			err = PersistExecutionBlock(ctx, input.StateStore, input.Run, block)
		}
		return restartRouteError(ctx, input, "continue active assignment implementation", err)
	}
	checkOperation, checkResult := nextRestartIDs(input.Run, "implementer-checks")
	_, err = ApplyImplementerTransition(ctx, ImplementerTransitionInput{
		Run: input.Run, Workspace: input.Workspace, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository,
		AssignmentID: assignmentID, BriefID: briefID, Selection: input.Checks, Runner: input.Runner, UserControl: input.UserControl,
		Limits: limits, ProtectedPaths: input.ProtectedPaths, OperationID: checkOperation, ResultID: checkResult,
	}, turn.Response)
	return restartRouteError(ctx, input, "apply active assignment response", err)
}

type restartAssignmentAction uint8

const (
	restartAssignmentImplement restartAssignmentAction = iota
	restartAssignmentReview
	restartAssignmentAwaitingAcceptance
)

// classifyRestartAssignmentAction uses operation/result order, not merely the
// existence of some historical successful check. A failed review routes back
// to the implementer, a newly successful mandatory check routes to review,
// and an interrupted executor operation consumes its next technical attempt
// under the same durable operation identity.
func classifyRestartAssignmentAction(run *implementationstate.Run, assignmentID implementationstate.AssignmentID) (restartAssignmentAction, *implementationstate.Operation, error) {
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil {
		return 0, nil, errors.New("active assignment is missing")
	}
	results := make(map[implementationstate.OperationID]*implementationstate.OperationResult, len(assignment.Results))
	for index := range assignment.Results {
		result := &assignment.Results[index]
		results[result.OperationID] = result
	}
	for index := len(assignment.Operations) - 1; index >= 0; index-- {
		operation := &assignment.Operations[index]
		result := results[operation.ID]
		if result == nil {
			if operation.Kind == implementationstate.OperationAgent && operation.Counter == implementationstate.CycleCounterNone && operation.BriefID != "" && operation.Episode == "" {
				if len(operation.Attempts) != 0 && operation.Attempts[len(operation.Attempts)-1].Outcome == implementationstate.AttemptSucceeded {
					// The provider response was accepted but no later route was made
					// durable. Start a new semantic continuation from the resulting
					// workspace instead of spending another technical retry on the
					// already completed operation.
					return restartAssignmentImplement, nil, nil
				}
				return restartAssignmentImplement, operation, nil
			}
			return 0, nil, fmt.Errorf("operation %s (%s) was interrupted without a safely reconstructable route", operation.ID, operation.Kind)
		}
		switch operation.Kind {
		case implementationstate.OperationReview:
			if result.Status == implementationstate.ResultSucceeded {
				return restartAssignmentAwaitingAcceptance, nil, nil
			}
			return restartAssignmentImplement, nil, nil
		case implementationstate.OperationCheck:
			if operation.Counter == implementationstate.CycleCounterMandatoryChecks && result.Status == implementationstate.ResultSucceeded && result.State == run.CurrentState {
				return restartAssignmentReview, nil, nil
			}
			return restartAssignmentImplement, nil, nil
		case implementationstate.OperationAgent:
			return restartAssignmentImplement, nil, nil
		}
	}
	return restartAssignmentImplement, nil, nil
}

func restartImplementerMessage(input RestartContinuationInput, assignment *implementationstate.Assignment) string {
	message := "Continue the active assignment from the complete durable brief and current workspace. Return the next structured implementation action."
	if assignment == nil || len(assignment.Results) == 0 {
		return message
	}
	latest := assignment.Results[len(assignment.Results)-1]
	if len(latest.Evidence) == 0 {
		return message
	}
	evidence, err := input.Journal.Read(latest.Evidence[0])
	if err != nil || len(evidence) == 0 {
		return message
	}
	return message + "\n\n# Latest durable assignment result\n\n" + string(evidence)
}

func restartRouteError(ctx context.Context, input RestartContinuationInput, action string, err error) error {
	if err == nil || input.Run.Status != implementationstate.RunActive || errors.Is(err, ErrUserOperationInterrupted) {
		return err
	}
	return pauseRestartContinuation(ctx, input, action, err)
}

func pauseRestartContinuation(ctx context.Context, input RestartContinuationInput, action string, cause error) error {
	if input.Run == nil || input.StateStore == nil || input.Run.Status != implementationstate.RunActive {
		return errors.Join(ErrRestartContinuation, cause)
	}
	block, err := ExecutionBlockForUserRemediation(action, cause.Error(), []string{"reconstructed restart state and attempted the next durable controller route"}, "inspect the diagnostic and durable run state, repair the reported condition, then explicitly resume or close the run")
	if err == nil {
		err = PersistExecutionBlock(context.WithoutCancel(ctx), input.StateStore, input.Run, block)
	}
	return errors.Join(ErrRestartContinuation, cause, err)
}

func restartCheckCatalog(selection implementationconfig.CheckSelection) []CheckCatalogEntry {
	names := make([]string, 0, len(selection.Checks))
	for name := range selection.Checks {
		names = append(names, name)
	}
	sort.Strings(names)
	required := make(map[string]bool, len(selection.Required))
	for _, name := range selection.Required {
		required[name] = true
	}
	result := make([]CheckCatalogEntry, 0, len(names))
	for _, name := range names {
		check := selection.Checks[name]
		result = append(result, CheckCatalogEntry{Name: name, Kind: check.Kind, Required: required[name]})
	}
	return result
}

func latestIncompleteRunAgentOperation(run *implementationstate.Run) *implementationstate.Operation {
	if run == nil {
		return nil
	}
	results := make(map[implementationstate.OperationID]bool, len(run.RunResults))
	for _, result := range run.RunResults {
		results[result.OperationID] = true
	}
	for index := len(run.RunOperations) - 1; index >= 0; index-- {
		operation := &run.RunOperations[index]
		if operation.Kind == implementationstate.OperationAgent && !results[operation.ID] {
			return operation
		}
	}
	return nil
}

func nextRestartIDs(run *implementationstate.Run, stem string) (implementationstate.OperationID, implementationstate.ResultID) {
	for number := 1; ; number++ {
		operation := implementationstate.OperationID(fmt.Sprintf("restart-%s-%d", stem, number))
		result := implementationstate.ResultID(fmt.Sprintf("restart-%s-%d-result", stem, number))
		if !restartOperationExists(run, operation) && !restartResultExists(run, result) {
			return operation, result
		}
	}
}

func restartOperationExists(run *implementationstate.Run, id implementationstate.OperationID) bool {
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

func restartResultExists(run *implementationstate.Run, id implementationstate.ResultID) bool {
	for _, result := range run.RunResults {
		if result.ID == id {
			return true
		}
	}
	for _, assignment := range run.Assignments {
		for _, result := range assignment.Results {
			if result.ID == id {
				return true
			}
		}
	}
	return false
}

func restartOperationCallID(operation *implementationstate.Operation) string {
	if operation == nil {
		return "restart-call-invalid"
	}
	return fmt.Sprintf("%s-call-%d", operation.ID, len(operation.Attempts)+1)
}

func nextRestartAssignmentID(run *implementationstate.Run) implementationstate.AssignmentID {
	for number := len(run.Assignments) + 1; ; number++ {
		candidate := implementationstate.AssignmentID(fmt.Sprintf("assignment-%d", number))
		if assignmentByID(run, candidate) == nil {
			return candidate
		}
	}
}
