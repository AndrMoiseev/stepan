package impl_loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
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
	CommitControl  CommitControl
	CommitObserver CommitObserver
	// AfterAgentSuccessReceipt is a crash-injection seam passed to the shared
	// controlled-call boundary. Production callers leave it nil.
	AfterAgentSuccessReceipt func() error
	// AfterAgentAttemptSucceeded injects a crash after the shared controlled
	// call has durably marked success but before response-kind routing.
	AfterAgentAttemptSucceeded func() error
}

// DispatchRestartContinuation restores the orchestrator and drives durable
// controller routes until the run reaches a real pause, terminal state, or
// user interruption. Every successful step must change the durable projection;
// repeated/no-progress state is failed closed rather than spinning active-idle.
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
	const maxRestartTransitions = 10000
	seen := make(map[string]struct{})
	for transition := 0; transition < maxRestartTransitions; transition++ {
		if input.Run.Status != implementationstate.RunActive {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(ErrUserOperationInterrupted, err)
		}
		before, err := json.Marshal(input.Run)
		if err != nil {
			return pauseRestartContinuation(ctx, input, "checkpoint restart continuation", err)
		}
		if _, repeated := seen[string(before)]; repeated {
			return pauseRestartContinuation(ctx, input, "continue implementation run", errors.New("restart controller revisited the same durable state"))
		}
		seen[string(before)] = struct{}{}
		if err := dispatchRestartStep(ctx, input, orchestrator, pkg, rules, catalog, limits, limitsConfig); err != nil {
			return err
		}
		if input.Run.Status != implementationstate.RunActive {
			return nil
		}
		after, err := json.Marshal(input.Run)
		if err != nil {
			return pauseRestartContinuation(ctx, input, "checkpoint restart continuation", err)
		}
		if bytes.Equal(before, after) {
			return pauseRestartContinuation(ctx, input, "continue implementation run", errors.New("controller route completed without durable progress"))
		}
	}
	return pauseRestartContinuation(ctx, input, "continue implementation run", errors.New("restart continuation exceeded its defensive transition bound"))
}

func dispatchRestartStep(ctx context.Context, input RestartContinuationInput, orchestrator *AgentSession, pkg openspec.Package, rules RulesIndex, catalog []CheckCatalogEntry, limits implementationstate.CycleLimits, limitsConfig implementationconfig.LoopLimits) error {
	switch {
	case input.Run.TaskExtractionPending:
		return runRestartTaskExtraction(ctx, input, orchestrator, pkg, limits)
	case input.Run.InitialBaselineStatus == implementationstate.InitialBaselinePending:
		operation, result := nextRestartIDs(input.Run, "initial-required-checks")
		if interrupted := latestIncompleteRunOperation(input.Run, "initial required checks"); interrupted != nil {
			operation = interrupted.ID
		}
		_, err := RunInitialRequiredChecks(ctx, InitialRequiredChecks{
			Run: input.Run, Workspace: input.Workspace, StateStore: input.StateStore, Journal: input.Journal,
			Repository: input.Repository, Selection: input.Checks, Runner: input.Runner, UserControl: input.UserControl,
			MaxCycles: limitsConfig.RequiredCheckAttempts, ProtectedPaths: input.ProtectedPaths, Operation: operation, Result: result,
		})
		return restartRouteError(ctx, input, "run initial required checks", err)
	case pendingCommitAssignment(input.Run) != "":
		return runRestartAcceptedAssignment(ctx, input, orchestrator, pendingCommitAssignment(input.Run), limits, time.Duration(limitsConfig.AgentTimeoutSeconds)*time.Second)
	case activeAssignment(input.Run) != "":
		return runRestartAssignment(ctx, input, orchestrator, activeAssignment(input.Run), rules, catalog, limits, time.Duration(limitsConfig.AgentTimeoutSeconds)*time.Second)
	case len(input.Run.PendingLeafTasks()) != 0:
		return runRestartBriefSelection(ctx, input, limits, time.Duration(limitsConfig.AgentTimeoutSeconds)*time.Second)
	default:
		return runRestartFinalAcceptance(ctx, input, orchestrator, rules, limits, limitsConfig)
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
	var err error
	operation, err = supersedeStaleRunOperation(ctx, input, operation, "extract-tasks")
	if err != nil {
		return pauseRestartContinuation(ctx, input, "refresh interrupted task extraction basis", err)
	}
	callID := restartOperationCallID(operation)
	binding := ResponseBinding{CallID: callID, RunID: input.Run.Identity.ID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
	_, err = ExecuteInitialTaskExtraction(ctx, ControlledAgentCall{
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
	var interrupted *implementationstate.Operation
	if interrupted = latestIncompleteRunOperation(input.Run, "select next assignment"); interrupted != nil {
		var err error
		interrupted, err = supersedeStaleRunOperation(ctx, input, interrupted, "select-assignment")
		if err != nil {
			return pauseRestartContinuation(ctx, input, "refresh interrupted assignment selection basis", err)
		}
		operation = interrupted.ID
	}
	assignmentID := nextRestartAssignmentID(input.Run)
	if err := PrepareBriefSelection(ctx, input.StateStore, input.Run, operation); err != nil {
		return restartRouteError(ctx, input, "prepare next assignment selection", err)
	}
	callID := restartCallID(operation, interrupted)
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

func runRestartAcceptedAssignment(ctx context.Context, input RestartContinuationInput, orchestrator *AgentSession, assignmentID implementationstate.AssignmentID, limits implementationstate.CycleLimits, timeout time.Duration) error {
	assignment := assignmentByID(input.Run, assignmentID)
	if assignment == nil || assignment.Acceptance == nil || len(assignment.Briefs) == 0 {
		return pauseRestartContinuation(ctx, input, "continue accepted assignment", errors.New("accepted assignment lacks durable acceptance or brief evidence"))
	}
	if intent, pending := pendingCommitIntent(input.Run, assignmentID); pending {
		_, err := CommitAcceptedAssignment(ctx, CommitAcceptedAssignmentInput{
			Run: input.Run, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository,
			AssignmentID: assignmentID, OperationID: intent.OperationID,
			Preparation: CommitPreparation{ParentCommit: intent.ParentCommit, Tree: intent.Tree},
			Control:     input.CommitControl, Observer: input.CommitObserver,
		})
		return restartRouteError(ctx, input, "finish pending assignment commit", err)
	}

	reflection := latestRunOperation(input.Run, "reflect accepted task progress in tasks.md")
	var reflectionResult *implementationstate.OperationResult
	if reflection != nil {
		reflectionResult = finalRunResultForOperation(input.Run, reflection.ID)
	}
	reflectionCurrent := reflectionResult != nil && reflectionResult.Status == implementationstate.ResultSucceeded && reflectionResult.State == assignment.Acceptance.State && reflectionResult.Basis == assignment.Acceptance.Basis
	if reflection == nil || !reflectionCurrent {
		operation, result := nextRestartIDs(input.Run, "reflect-progress")
		if reflection != nil && reflectionResult == nil {
			operation = reflection.ID
		}
		tasksPath, err := selectedChangeTasksPath(input.Run.Identity.Change)
		if err != nil {
			return pauseRestartContinuation(ctx, input, "locate progress task list", err)
		}
		_, err = AcceptAssignmentAndReflectProgress(ctx, AcceptanceReflectionInput{
			Run: input.Run, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository, Workspace: input.Workspace,
			Session: orchestrator, AssignmentID: assignmentID, Acceptance: *assignment.Acceptance, TasksPath: tasksPath,
			ReflectionOperationID: operation, ReflectionResultID: result, ReflectionCallID: restartCallID(operation, reflection), Limits: limits, Timeout: timeout,
		})
		return restartRouteError(ctx, input, "reflect accepted assignment progress", err)
	}

	snapshot, err := restartReflectionSnapshot(input.Journal, reflectionResult)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "recover reflected assignment workspace", err)
	}
	preparation, err := CommitPreparationFromSnapshot(snapshot)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "prepare accepted assignment commit", err)
	}
	operation, _ := nextRestartIDs(input.Run, "assignment-commit")
	response, err := latestImplementationReadyResponse(input.Journal, input.Run, assignmentID)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "recover accepted assignment implementation response", err)
	}
	response = rebindImplementationReadyResponse(input.Run, assignmentID, response)
	_, err = CommitAcceptedAssignment(ctx, CommitAcceptedAssignmentInput{
		Run: input.Run, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository,
		AssignmentID: assignmentID, OperationID: operation, Response: response, Preparation: preparation,
		Control: input.CommitControl, Observer: input.CommitObserver,
	})
	return restartRouteError(ctx, input, "commit accepted assignment", err)
}

func runRestartFinalAcceptance(ctx context.Context, input RestartContinuationInput, orchestrator *AgentSession, rules RulesIndex, limits implementationstate.CycleLimits, limitsConfig implementationconfig.LoopLimits) error {
	timeout := time.Duration(limitsConfig.AgentTimeoutSeconds) * time.Second
	checkResult, checkOperationIndex := latestCurrentFinalCheck(input.Run)
	if checkResult == nil {
		operation, result := nextRestartIDs(input.Run, "final-required-checks")
		basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
		if interrupted := latestIncompleteRunOperation(input.Run, "final required checks"); interrupted != nil && interrupted.Basis == basis {
			operation = interrupted.ID
		}
		_, err := RunFinalRequiredChecks(ctx, FinalRequiredChecks{
			Run: input.Run, Workspace: input.Workspace, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository,
			Selection: input.Checks, Runner: input.Runner, UserControl: input.UserControl, MaxCycles: limitsConfig.RequiredCheckAttempts,
			ProtectedPaths: input.ProtectedPaths, Operation: operation, Result: result,
		})
		return restartRouteError(ctx, input, "run final required checks", err)
	}

	if helper := latestIncompleteRunOperationAfter(input.Run, "append tasks for final review findings", checkOperationIndex); helper != nil {
		return runRestartFinalFindingTasks(ctx, input, orchestrator, helper, limits, timeout)
	}
	reviewOperation, reviewResult := latestFinalReviewAfter(input.Run, checkOperationIndex)
	if reviewOperation == nil || reviewResult == nil {
		operation, result := nextRestartIDs(input.Run, "final-review")
		if reviewOperation != nil && reviewOperation.Description == "independent final review" {
			operation = reviewOperation.ID
		}
		review, err := StartFinalReview(ctx, FinalReviewInput{
			Owner: input.Owner, Workspace: input.Workspace, Run: input.Run, StateStore: input.StateStore, Journal: input.Journal,
			Repository: input.Repository, Rules: rules, CheckResult: checkResult.ID, OperationID: operation, ResultID: result,
			CallID: restartCallID(operation, reviewOperation), RoundID: "restart-" + string(operation), Limits: limits, Timeout: timeout,
		})
		if err != nil {
			return restartRouteError(ctx, input, "run final review", err)
		}
		if input.Run.Status != implementationstate.RunActive || review.Response.Kind != ResponseReviewPassed {
			return nil
		}
		return restartRouteError(ctx, input, "complete final acceptance", CompleteFinalAcceptance(ctx, input.Run, input.StateStore, input.Journal, input.Workspace, input.Repository, checkResult.ID, review))
	}
	response, err := restartFinalReviewResponse(input.Journal, reviewResult)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "recover final review result", err)
	}
	switch response.Kind {
	case ResponseReviewPassed:
		return restartRouteError(ctx, input, "complete final acceptance", CompleteFinalAcceptance(ctx, input.Run, input.StateStore, input.Journal, input.Workspace, input.Repository, checkResult.ID, FinalReviewResult{Response: response, ResultID: reviewResult.ID}))
	case ResponseChangesRequested:
		return runRestartFinalFindingTasks(ctx, input, orchestrator, nil, limits, timeout)
	case ResponseExplorationRequested:
		// The source request is durable, but the provider conversation is not.
		// Restart an independent final-review round while retaining the consumed
		// source/helper counters; this is deterministic and cannot reuse an
		// unproven Explorer answer.
		operation, result := nextRestartIDs(input.Run, "final-review")
		review, reviewErr := StartFinalReview(ctx, FinalReviewInput{
			Owner: input.Owner, Workspace: input.Workspace, Run: input.Run, StateStore: input.StateStore, Journal: input.Journal,
			Repository: input.Repository, Rules: rules, CheckResult: checkResult.ID, OperationID: operation, ResultID: result,
			CallID: string(operation) + "-call-1", RoundID: "restart-" + string(operation), Limits: limits, Timeout: timeout,
		})
		if reviewErr != nil {
			return restartRouteError(ctx, input, "restart interrupted final-review helper", reviewErr)
		}
		if input.Run.Status != implementationstate.RunActive || review.Response.Kind != ResponseReviewPassed {
			return nil
		}
		return restartRouteError(ctx, input, "complete final acceptance", CompleteFinalAcceptance(ctx, input.Run, input.StateStore, input.Journal, input.Workspace, input.Repository, checkResult.ID, review))
	default:
		return pauseRestartContinuation(ctx, input, "continue final acceptance", fmt.Errorf("durable final review ended with %s while the run remained active", response.Kind))
	}
}

func runRestartFinalFindingTasks(ctx context.Context, input RestartContinuationInput, orchestrator *AgentSession, interrupted *implementationstate.Operation, limits implementationstate.CycleLimits, timeout time.Duration) error {
	_, reviewResult := latestFinalReviewAfter(input.Run, -1)
	if reviewResult == nil {
		return pauseRestartContinuation(ctx, input, "append final finding tasks", errors.New("final-finding helper has no durable final review"))
	}
	response, err := restartFinalReviewResponse(input.Journal, reviewResult)
	if err != nil || response.Kind != ResponseChangesRequested {
		return pauseRestartContinuation(ctx, input, "recover final findings", errors.Join(err, fmt.Errorf("durable review kind is %s", response.Kind)))
	}
	operation, result := nextRestartIDs(input.Run, "add-final-tasks")
	if interrupted != nil {
		operation = interrupted.ID
	}
	tasksPath, err := selectedChangeTasksPath(input.Run.Identity.Change)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "locate final finding task list", err)
	}
	_, err = AddFinalFindingTasks(ctx, FinalFindingTasksInput{
		Run: input.Run, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository, Workspace: input.Workspace,
		Session: orchestrator, Review: FinalReviewResult{Response: response, ResultID: reviewResult.ID}, TasksPath: tasksPath,
		OperationID: operation, ResultID: result, CallID: restartCallID(operation, interrupted), Limits: limits, Timeout: timeout,
	})
	return restartRouteError(ctx, input, "append final finding tasks", err)
}

func runRestartAssignment(ctx context.Context, input RestartContinuationInput, orchestrator *AgentSession, assignmentID implementationstate.AssignmentID, rules RulesIndex, catalog []CheckCatalogEntry, limits implementationstate.CycleLimits, timeout time.Duration) error {
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

	if operation := latestSucceededImplementationWithoutResult(input.Run, assignmentID); operation != nil {
		_, _, _, _, found, receiptErr := readControlledAgentSuccessReceipt(input.Journal, operation.ID)
		if receiptErr != nil {
			return pauseRestartContinuation(ctx, input, "recover completed implementer response", receiptErr)
		}
		if !found {
			return pauseRestartContinuation(ctx, input, "recover completed implementer response", fmt.Errorf("implementation operation %s succeeded without a durable accepted-turn receipt", operation.ID))
		}
	}
	action, interruptedOperation, err := classifyRestartAssignmentAction(input.Run, assignmentID)
	if err != nil {
		return pauseRestartContinuation(ctx, input, "classify active assignment continuation", err)
	}
	if action == restartAssignmentAwaitingAcceptance {
		acceptance, err := restartAcceptanceEvidence(input.Run, assignmentID)
		if err != nil {
			return pauseRestartContinuation(ctx, input, "rebuild reviewed assignment acceptance", err)
		}
		operation, result := nextRestartIDs(input.Run, "reflect-progress")
		tasksPath, err := selectedChangeTasksPath(input.Run.Identity.Change)
		if err != nil {
			return pauseRestartContinuation(ctx, input, "locate progress task list", err)
		}
		_, err = AcceptAssignmentAndReflectProgress(ctx, AcceptanceReflectionInput{
			Run: input.Run, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository, Workspace: input.Workspace,
			Session: orchestrator, AssignmentID: assignmentID, Acceptance: acceptance, TasksPath: tasksPath,
			ReflectionOperationID: operation, ReflectionResultID: result, ReflectionCallID: string(operation) + "-call-1", Limits: limits, Timeout: timeout,
		})
		return restartRouteError(ctx, input, "accept and reflect reviewed assignment", err)
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
		if interruptedOperation != nil {
			operation = interruptedOperation.ID
		}
		_, err = StartTaskReview(ctx, TaskReviewInput{
			Owner: input.Owner, Workspace: input.Workspace, Run: input.Run, StateStore: input.StateStore, Journal: input.Journal,
			Repository: input.Repository, AssignmentID: assignmentID, Rules: rules, Checks: catalog,
			OperationID: operation, ResultID: result, CallID: restartCallID(operation, interruptedOperation), Limits: limits, Timeout: timeout,
		})
		return restartRouteError(ctx, input, "review active assignment", err)
	}
	if action == restartAssignmentChecks {
		assignment := assignmentByID(input.Run, assignmentID)
		if assignment == nil || len(assignment.Briefs) == 0 {
			return pauseRestartContinuation(ctx, input, "continue assignment checks", errors.New("required-check route lacks its durable assignment binding"))
		}
		briefID := assignment.Briefs[len(assignment.Briefs)-1].ID
		operation, result := nextRestartIDs(input.Run, "implementer-checks")
		if interruptedOperation != nil {
			operation = interruptedOperation.ID
		}
		response, err := latestImplementationReadyResponse(input.Journal, input.Run, assignmentID)
		if err != nil {
			return pauseRestartContinuation(ctx, input, "recover implementation_ready response for checks", err)
		}
		response = rebindImplementationReadyResponse(input.Run, assignmentID, response)
		_, err = ApplyImplementerTransition(ctx, ImplementerTransitionInput{
			Run: input.Run, Workspace: input.Workspace, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository,
			AssignmentID: assignmentID, BriefID: briefID, Selection: input.Checks, Runner: input.Runner, UserControl: input.UserControl,
			Limits: limits, ProtectedPaths: input.ProtectedPaths, OperationID: operation, ResultID: result,
		}, response)
		return restartRouteError(ctx, input, "continue assignment checks", err)
	}
	if action == restartAssignmentRefineBrief {
		assignment := assignmentByID(input.Run, assignmentID)
		if assignment == nil || len(assignment.Briefs) == 0 || interruptedOperation == nil {
			return pauseRestartContinuation(ctx, input, "continue interrupted brief refinement", errors.New("refinement operation lacks its durable assignment binding"))
		}
		briefID := assignment.Briefs[len(assignment.Briefs)-1].ID
		operation, result := nextRestartIDs(input.Run, "brief-refinement")
		var resumed *implementationstate.Operation
		if interruptedOperation.Counter == implementationstate.CycleCounterBriefRefinement && interruptedOperation.Description == "refine assignment brief" {
			operation, resumed = interruptedOperation.ID, interruptedOperation
		}
		question, contextText, boundaries := "Continue the interrupted brief refinement.", "The durable controller state records an unfinished refinement for this assignment.", "Preserve the selected task IDs and approved OpenSpec scope."
		request := AgentResponse{Kind: ResponseClarificationNeeded, Question: &question, Context: &contextText, Boundaries: &boundaries, References: []string{"durable refinement operation " + string(interruptedOperation.ID)}, Binding: ResponseBinding{CallID: string(interruptedOperation.ID) + "-source", RunID: input.Run.Identity.ID, AssignmentID: assignmentID, BriefID: briefID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}}
		_, err := RefineBrief(ctx, BriefRefinementInput{
			Owner: input.Owner, Workspace: input.Workspace, Run: input.Run, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository,
			AssignmentID: assignmentID, RequesterRole: ResponseRoleImplementer, Request: request, OperationID: operation,
			ResultID: result, CallID: restartCallID(operation, resumed), Limits: limits, Timeout: timeout,
		})
		return restartRouteError(ctx, input, "continue interrupted brief refinement", err)
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
		AfterSuccessReceipt:   input.AfterAgentSuccessReceipt,
		AfterAttemptSucceeded: input.AfterAgentAttemptSucceeded,
	})
	if err != nil {
		return restartRouteError(ctx, input, "continue active assignment implementation", err)
	}
	return routeRestartImplementerResponse(ctx, input, assignmentID, briefID, operation, turn, limits)
}

func routeRestartImplementerResponse(ctx context.Context, input RestartContinuationInput, assignmentID implementationstate.AssignmentID, briefID implementationstate.BriefID, operation implementationstate.OperationID, turn ControlledAgentCallResult, limits implementationstate.CycleLimits) error {
	if turn.Response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(turn.Response)
		if err == nil {
			err = PersistExecutionBlock(ctx, input.StateStore, input.Run, block)
		}
		return restartRouteError(ctx, input, "continue active assignment implementation", err)
	}
	if turn.Response.Kind == ResponseImplementationReady {
		if err := persistImplementationReadyReceipt(ctx, input, assignmentID, operation, turn.Response, turn.Snapshot); err != nil {
			return restartRouteError(ctx, input, "persist implementation_ready response", err)
		}
	}
	checkOperation, checkResult := nextRestartIDs(input.Run, "implementer-checks")
	_, err := ApplyImplementerTransition(ctx, ImplementerTransitionInput{
		Run: input.Run, Workspace: input.Workspace, StateStore: input.StateStore, Journal: input.Journal, Repository: input.Repository,
		AssignmentID: assignmentID, BriefID: briefID, Selection: input.Checks, Runner: input.Runner, UserControl: input.UserControl,
		Limits: limits, ProtectedPaths: input.ProtectedPaths, OperationID: checkOperation, ResultID: checkResult,
	}, turn.Response)
	return restartRouteError(ctx, input, "apply active assignment response", err)
}

type restartAssignmentAction uint8

const (
	restartAssignmentImplement restartAssignmentAction = iota
	restartAssignmentChecks
	restartAssignmentReview
	restartAssignmentAwaitingAcceptance
	restartAssignmentRefineBrief
)

// classifyRestartAssignmentAction considers only evidence on the current
// acceptance basis. Stale checks and reviews are ignored in favor of a fresh
// mandatory-check route backed by the durable implementation_ready receipt.
func classifyRestartAssignmentAction(run *implementationstate.Run, assignmentID implementationstate.AssignmentID) (restartAssignmentAction, *implementationstate.Operation, error) {
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil || len(assignment.Briefs) == 0 {
		return 0, nil, errors.New("active assignment is missing")
	}
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	briefID := assignment.Briefs[len(assignment.Briefs)-1].ID
	results := make(map[implementationstate.OperationID]*implementationstate.OperationResult, len(assignment.Results))
	for index := range assignment.Results {
		result := &assignment.Results[index]
		results[result.OperationID] = result
	}
	for index := len(assignment.Operations) - 1; index >= 0; index-- {
		operation := &assignment.Operations[index]
		if operation.BriefID != briefID {
			continue
		}
		result := results[operation.ID]
		if result == nil {
			switch {
			case operation.Kind == implementationstate.OperationCheck && operation.Counter == implementationstate.CycleCounterMandatoryChecks && operation.Basis == basis:
				return restartAssignmentChecks, operation, nil
			case operation.Kind == implementationstate.OperationReview && operation.Counter == implementationstate.CycleCounterAssignmentReview && operation.Basis == basis:
				return restartAssignmentReview, operation, nil
			case operation.Kind == implementationstate.OperationAgent && (operation.Counter == implementationstate.CycleCounterBriefRefinement || operation.Episode == "brief_refinement" || strings.Contains(operation.Description, "brief refinement")):
				return restartAssignmentRefineBrief, operation, nil
			case operation.Kind == implementationstate.OperationAgent && operation.Counter == implementationstate.CycleCounterNone && operation.BriefID != "" && operation.Episode == "":
				if len(operation.Attempts) != 0 && operation.Attempts[len(operation.Attempts)-1].Outcome == implementationstate.AttemptSucceeded {
					return restartAssignmentImplement, operation, nil
				}
				if operation.Basis != basis {
					continue
				}
				return restartAssignmentImplement, operation, nil
			case operation.Basis == basis:
				return 0, nil, fmt.Errorf("operation %s (%s) was interrupted without a safely reconstructable route", operation.ID, operation.Kind)
			default:
				continue
			}
		}
		if operation.Basis != basis || result.Basis != basis || result.State != run.CurrentState {
			continue
		}
		switch operation.Kind {
		case implementationstate.OperationReview:
			if operation.Counter == implementationstate.CycleCounterAssignmentReview && result.Status == implementationstate.ResultSucceeded {
				return restartAssignmentAwaitingAcceptance, nil, nil
			}
			return restartAssignmentImplement, nil, nil
		case implementationstate.OperationCheck:
			if operation.Counter == implementationstate.CycleCounterMandatoryChecks && result.Status == implementationstate.ResultSucceeded {
				return restartAssignmentReview, nil, nil
			}
			return restartAssignmentImplement, nil, nil
		}
	}
	for index := len(assignment.Operations) - 1; index >= 0; index-- {
		operation := assignment.Operations[index]
		result := results[operation.ID]
		if operation.Kind == implementationstate.OperationAgent && operation.Counter == implementationstate.CycleCounterNone && operation.Episode == "" && operation.BriefID == briefID && result != nil && result.Status == implementationstate.ResultSucceeded && result.State.Digest == run.CurrentState.Digest && len(result.Evidence) >= 2 {
			return restartAssignmentChecks, nil, nil
		}
	}
	return restartAssignmentImplement, nil, nil
}

func latestSucceededImplementationWithoutResult(run *implementationstate.Run, assignmentID implementationstate.AssignmentID) *implementationstate.Operation {
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil || len(assignment.Operations) == 0 {
		return nil
	}
	operation := &assignment.Operations[len(assignment.Operations)-1]
	if operation.Kind != implementationstate.OperationAgent || operation.Counter != implementationstate.CycleCounterNone || operation.Episode != "" || assignmentResultForOperation(run, assignmentID, operation.ID) != nil || len(operation.Attempts) == 0 || operation.Attempts[len(operation.Attempts)-1].Outcome != implementationstate.AttemptSucceeded {
		return nil
	}
	return operation
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

func restartAcceptanceEvidence(run *implementationstate.Run, assignmentID implementationstate.AssignmentID) (implementationstate.AcceptanceEvidence, error) {
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil || assignment.Status != implementationstate.AssignmentActive || len(assignment.Briefs) == 0 {
		return implementationstate.AcceptanceEvidence{}, errors.New("assignment is not active with a durable brief")
	}
	briefID := assignment.Briefs[len(assignment.Briefs)-1].ID
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	results := make(map[implementationstate.OperationID]implementationstate.OperationResult, len(assignment.Results))
	for _, result := range assignment.Results {
		results[result.OperationID] = result
	}
	var review *implementationstate.OperationResult
	var reviewIndex int
	for index := len(assignment.Operations) - 1; index >= 0; index-- {
		operation := assignment.Operations[index]
		result, ok := results[operation.ID]
		if ok && operation.Kind == implementationstate.OperationReview && operation.BriefID == briefID && operation.Basis == basis && result.Status == implementationstate.ResultSucceeded && result.State == run.CurrentState && result.Basis == basis {
			copy := result
			review, reviewIndex = &copy, index
			break
		}
	}
	if review == nil {
		return implementationstate.AcceptanceEvidence{}, errors.New("current successful task review is missing")
	}
	var checks *implementationstate.OperationResult
	for index := reviewIndex - 1; index >= 0; index-- {
		operation := assignment.Operations[index]
		result, ok := results[operation.ID]
		if ok && operation.Kind == implementationstate.OperationCheck && operation.Counter == implementationstate.CycleCounterMandatoryChecks && operation.BriefID == briefID && operation.Basis == basis && result.Status == implementationstate.ResultSucceeded && result.State == run.CurrentState && result.Basis == basis {
			copy := result
			checks = &copy
			break
		}
	}
	if checks == nil {
		return implementationstate.AcceptanceEvidence{}, errors.New("current successful mandatory checks are missing")
	}
	return implementationstate.AcceptanceEvidence{BriefID: briefID, State: run.CurrentState, Basis: basis, CheckResultIDs: []implementationstate.ResultID{checks.ID}, ReviewResultID: review.ID}, nil
}

func restartReflectionSnapshot(journal *runstore.Run, result *implementationstate.OperationResult) (gitsnapshot.Snapshot, error) {
	if journal == nil || result == nil || result.Status != implementationstate.ResultSucceeded || len(result.Evidence) == 0 {
		return gitsnapshot.Snapshot{}, errors.New("successful reflection workspace evidence is missing")
	}
	data, err := journal.Read(result.Evidence[0])
	if err != nil {
		return gitsnapshot.Snapshot{}, err
	}
	var snapshot gitsnapshot.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return gitsnapshot.Snapshot{}, err
	}
	if strings.TrimSpace(snapshot.HeadOID) == "" || strings.TrimSpace(snapshot.TreeOID) == "" {
		return gitsnapshot.Snapshot{}, errors.New("reflection workspace lacks Git parent or tree")
	}
	return snapshot, nil
}

func restartFinalReviewResponse(journal *runstore.Run, result *implementationstate.OperationResult) (AgentResponse, error) {
	if journal == nil || result == nil || len(result.Evidence) == 0 {
		return AgentResponse{}, errors.New("final review response evidence is missing")
	}
	data, err := journal.Read(result.Evidence[0])
	if err != nil {
		return AgentResponse{}, err
	}
	var receipt struct {
		Response AgentResponse `json:"response"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		return AgentResponse{}, err
	}
	if err := validateFinalReviewerResponse(receipt.Response); err != nil {
		return AgentResponse{}, err
	}
	return receipt.Response, nil
}

func latestCurrentFinalCheck(run *implementationstate.Run) (*implementationstate.OperationResult, int) {
	if run == nil {
		return nil, -1
	}
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	for index := len(run.RunOperations) - 1; index >= 0; index-- {
		operation := &run.RunOperations[index]
		if operation.Kind != implementationstate.OperationCheck || operation.Description != "final required checks" {
			continue
		}
		result := finalRunResultForOperation(run, operation.ID)
		if result != nil && result.Status == implementationstate.ResultSucceeded && result.State == run.CurrentState && result.Basis == basis && operation.Basis == basis {
			return result, index
		}
		return nil, index
	}
	return nil, -1
}

func latestFinalReviewAfter(run *implementationstate.Run, after int) (*implementationstate.Operation, *implementationstate.OperationResult) {
	if run == nil {
		return nil, nil
	}
	for index := len(run.RunOperations) - 1; index > after; index-- {
		operation := &run.RunOperations[index]
		if operation.Kind == implementationstate.OperationReview && operation.Counter == implementationstate.CycleCounterFinalReview && operation.Description == "independent final review" {
			return operation, finalRunResultForOperation(run, operation.ID)
		}
	}
	return nil, nil
}

func latestRunOperation(run *implementationstate.Run, description string) *implementationstate.Operation {
	if run == nil {
		return nil
	}
	for index := len(run.RunOperations) - 1; index >= 0; index-- {
		if run.RunOperations[index].Description == description {
			return &run.RunOperations[index]
		}
	}
	return nil
}

func latestIncompleteRunOperation(run *implementationstate.Run, description string) *implementationstate.Operation {
	return latestIncompleteRunOperationAfter(run, description, -1)
}

func latestIncompleteRunOperationAfter(run *implementationstate.Run, description string, after int) *implementationstate.Operation {
	if run == nil {
		return nil
	}
	for index := len(run.RunOperations) - 1; index > after; index-- {
		operation := &run.RunOperations[index]
		if operation.Description != description {
			continue
		}
		if finalRunResultForOperation(run, operation.ID) == nil {
			return operation
		}
		return nil
	}
	return nil
}

func supersedeStaleRunOperation(ctx context.Context, input RestartContinuationInput, prior *implementationstate.Operation, stem string) (*implementationstate.Operation, error) {
	if prior == nil || input.Run == nil || input.StateStore == nil {
		return nil, errors.New("run operation supersession requires state, store, and prior operation")
	}
	basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	if prior.Basis == basis {
		return prior, nil
	}
	encoded, err := json.Marshal(input.Run)
	if err != nil {
		return nil, fmt.Errorf("clone run before operation supersession: %w", err)
	}
	var candidate implementationstate.Run
	if err := json.Unmarshal(encoded, &candidate); err != nil {
		return nil, fmt.Errorf("clone run before operation supersession: %w", err)
	}
	replacementID, _ := nextRestartIDs(&candidate, stem)
	replacement := implementationstate.Operation{
		ID: replacementID, Kind: prior.Kind, Basis: basis, Description: prior.Description,
		Counter: prior.Counter, Episode: prior.Episode, Supersedes: prior.ID,
		UncountedResumeCheck: prior.UncountedResumeCheck,
	}
	if err := candidate.SupersedeRunOperation(prior.ID, replacement); err != nil {
		return nil, err
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), &candidate); err != nil {
		return nil, fmt.Errorf("persist run operation supersession: %w", err)
	}
	*input.Run = candidate
	operation := finalRunOperation(input.Run, replacementID)
	if operation == nil {
		return nil, errors.New("persisted run operation supersession is missing")
	}
	return operation, nil
}

func restartCallID(operation implementationstate.OperationID, existing *implementationstate.Operation) string {
	if existing != nil {
		return restartOperationCallID(existing)
	}
	return string(operation) + "-call-1"
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
	number := len(operation.Attempts) + 1
	if len(operation.Attempts) != 0 {
		outcome := operation.Attempts[len(operation.Attempts)-1].Outcome
		if outcome == "" || outcome == implementationstate.AttemptSucceeded {
			number = len(operation.Attempts)
		}
	}
	return fmt.Sprintf("%s-call-%d", operation.ID, number)
}

func nextRestartAssignmentID(run *implementationstate.Run) implementationstate.AssignmentID {
	for number := len(run.Assignments) + 1; ; number++ {
		candidate := implementationstate.AssignmentID(fmt.Sprintf("assignment-%d", number))
		if assignmentByID(run, candidate) == nil {
			return candidate
		}
	}
}
