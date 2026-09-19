package impl_loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/git"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var ErrFinalAcceptanceRoute = errors.New("invalid final acceptance route")

// recordFinalExplorerState is a narrow recovery seam. The response artifact
// is deliberately published before this call, so a crash at this boundary can
// be recovered without asking Explorer to repeat completed research.
var recordFinalExplorerState = func(ctx context.Context, state *runstore.StateStore, run *implementationstate.Run) (implementationstate.Event, error) {
	return state.Record(ctx, run)
}

// FinalRequiredChecks is the controller-owned final command gate. It is
// deliberately run-scoped: it cannot be requested, narrowed, or bypassed by
// the final reviewer, and it never waits for a manual check.
type FinalRequiredChecks struct {
	Run            *implementationstate.Run
	Workspace      WorkspaceControl
	StateStore     *runstore.StateStore
	Journal        *runstore.Run
	Repository     string
	Selection      implementationconfig.CheckSelection
	Runner         CheckRunner
	UserControl    *UserRunControl
	MaxCycles      int
	ProtectedPaths []string
	Operation      implementationstate.OperationID
	Result         implementationstate.ResultID
}

type FinalRequiredChecksResult struct {
	Set         CheckSet
	Convergence RequiredCheckConvergence
	Diagnostic  string
	Evidence    implementationstate.EvidenceRef
}

// RunFinalRequiredChecks runs the entire configured required set on the
// completed task state. A command failure is an execution block, not a
// successful final state and not a request for an agent to weaken checks.
func RunFinalRequiredChecks(ctx context.Context, input FinalRequiredChecks) (FinalRequiredChecksResult, error) {
	if err := validateFinalRequiredChecks(input); err != nil {
		return FinalRequiredChecksResult{}, err
	}
	basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	existing := finalRunOperation(input.Run, input.Operation)
	if existing == nil {
		if err := input.Run.AddRunOperation(implementationstate.Operation{ID: input.Operation, Kind: implementationstate.OperationCheck, Basis: basis, Description: "final required checks"}); err != nil {
			return FinalRequiredChecksResult{}, fmt.Errorf("%w: create final check operation: %v", ErrFinalAcceptanceRoute, err)
		}
		if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
			return FinalRequiredChecksResult{}, fmt.Errorf("%w: persist final check operation: %v", ErrFinalAcceptanceRoute, err)
		}
	} else if existing.Kind != implementationstate.OperationCheck || existing.Basis != basis || existing.Description != "final required checks" || finalRunResultForOperation(input.Run, existing.ID) != nil {
		return FinalRequiredChecksResult{}, fmt.Errorf("%w: final check operation cannot be resumed", ErrFinalAcceptanceRoute)
	}
	if _, _, err := input.StateStore.RecordRunAttemptStart(ctx, input.Run, input.Operation); err != nil {
		return FinalRequiredChecksResult{}, fmt.Errorf("%w: reserve final check attempt: %v", ErrFinalAcceptanceRoute, err)
	}
	publisher, err := NewCheckResultPublisherWithControl(input.Journal, input.Workspace, input.Repository, implementationstate.EvidenceID(input.Result))
	if err != nil {
		return pauseFinalChecks(ctx, input, FinalRequiredChecksResult{}, fmt.Errorf("create final check publisher: %w", err))
	}
	observer, err := NewWorkspaceCheckObserverWithControl(ctx, input.Workspace, input.Repository, input.Run, input.Journal, input.ProtectedPaths)
	if err != nil {
		return pauseFinalChecks(ctx, input, FinalRequiredChecksResult{}, fmt.Errorf("create final workspace observer: %w", err))
	}
	checkContext, finishCheck, err := beginUserControlledCheck(ctx, input.UserControl)
	if err != nil {
		return FinalRequiredChecksResult{}, err
	}
	defer finishCheck()
	convergence, runErr := RunRequiredChecksUntilStable(checkContext, input.Selection, input.Runner, &WorkspaceCheckReporter{Observer: observer, Publisher: publisher}, input.MaxCycles)
	set := initialCheckSet(convergence)
	diagnostic := initialCheckDiagnostic(set, runErr)
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(checkContext), checkResultPersistenceTimeout)
	defer cancel()
	evidence, publishErr := publishFinalCheckEvidence(input.Journal, input.Result, convergence, runErr)
	result := FinalRequiredChecksResult{Set: set, Convergence: convergence, Diagnostic: diagnostic, Evidence: evidence}
	if publishErr != nil {
		return pauseFinalChecks(persistContext, input, result, fmt.Errorf("publish final check diagnostics: %w", publishErr))
	}
	status, outcome := implementationstate.ResultSucceeded, implementationstate.AttemptSucceeded
	if runErr != nil || !set.Succeeded() {
		status, outcome = implementationstate.ResultFailed, implementationstate.AttemptFailed
		if errors.Is(checkContext.Err(), context.Canceled) || errors.Is(checkContext.Err(), context.DeadlineExceeded) {
			status, outcome = implementationstate.ResultInterrupted, implementationstate.AttemptInterrupted
		}
	}
	if _, err := input.StateStore.RecordRunAttemptOutcome(persistContext, input.Run, input.Operation, outcome, diagnostic); err != nil {
		return FinalRequiredChecksResult{}, fmt.Errorf("%w: persist final check outcome: %v", ErrFinalAcceptanceRoute, err)
	}
	state := finalInitialCheckedState(input.Run.CurrentState, set)
	if err := input.Run.AddRunResult(implementationstate.OperationResult{ID: input.Result, OperationID: input.Operation, Status: status, State: state, Basis: basis, Evidence: initialCheckEvidenceRefs(evidence, set)}); err != nil {
		return FinalRequiredChecksResult{}, fmt.Errorf("%w: record final check result: %v", ErrFinalAcceptanceRoute, err)
	}
	if state != input.Run.CurrentState && input.Run.Status == implementationstate.RunActive {
		if err := input.Run.ObserveCodeState(state); err != nil {
			return FinalRequiredChecksResult{}, fmt.Errorf("%w: record final checked state: %v", ErrFinalAcceptanceRoute, err)
		}
	}
	if _, err := input.StateStore.Record(persistContext, input.Run); err != nil {
		return FinalRequiredChecksResult{}, fmt.Errorf("%w: persist final check result: %v", ErrFinalAcceptanceRoute, err)
	}
	if status != implementationstate.ResultSucceeded {
		if UserOperationInterrupted(checkContext) {
			return result, ErrUserOperationInterrupted
		}
		return pauseFinalChecks(persistContext, input, result, nil)
	}
	return result, nil
}

func validateFinalRequiredChecks(input FinalRequiredChecks) error {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || input.Runner == nil || strings.TrimSpace(input.Repository) == "" || input.Operation == "" || input.Result == "" || input.MaxCycles <= 0 {
		return fmt.Errorf("%w: run, store, journal, repository, runner, check identities, and convergence bound are required", ErrFinalAcceptanceRoute)
	}
	if !runHasOnlyCompletedTasks(input.Run) {
		return fmt.Errorf("%w: final checks require all tasks to be committed", ErrFinalAcceptanceRoute)
	}
	return nil
}

func pauseFinalChecks(ctx context.Context, input FinalRequiredChecks, result FinalRequiredChecksResult, cause error) (FinalRequiredChecksResult, error) {
	if input.Run.Status == implementationstate.RunActive {
		diagnostic := strings.TrimSpace(result.Diagnostic)
		if diagnostic == "" && cause != nil {
			diagnostic = cause.Error()
		}
		if diagnostic == "" {
			diagnostic = "final required checks did not pass"
		}
		block, err := ExecutionBlockForUserRemediation("run the final required checks", diagnostic, []string{"ran the full configured final required-check set without changing scope or check definitions"}, "repair the environment or project configuration, then explicitly resume or close the run")
		if err != nil {
			return result, errors.Join(cause, err)
		}
		if err := input.Run.PauseExecutionBlocked(block); err != nil {
			return result, errors.Join(cause, fmt.Errorf("pause after final required checks: %w", err))
		}
		if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
			return result, errors.Join(cause, fmt.Errorf("persist final required-check pause: %w", err))
		}
	}
	return result, cause
}

func publishFinalCheckEvidence(journal *runstore.Run, id implementationstate.ResultID, convergence RequiredCheckConvergence, setErr error) (implementationstate.EvidenceRef, error) {
	if journal == nil {
		return implementationstate.EvidenceRef{}, errors.New("final checks require a run journal")
	}
	evidence := initialCheckSetEvidence{Kind: CheckSetRequired}
	if setErr != nil {
		evidence.Error = setErr.Error()
	}
	for _, cycle := range convergence.Cycles {
		for _, result := range cycle.Set.Results {
			item := initialCheckResultEvidence{Name: result.Name, Status: result.Status, Presentation: result.Presentation}
			if result.Command.Program != "" {
				item.Command = RenderCheckCommand(result.Command)
			}
			if result.Err != nil {
				item.Error = result.Err.Error()
			}
			evidence.Results = append(evidence.Results, item)
		}
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return implementationstate.EvidenceRef{}, err
	}
	return journal.Publish(implementationstate.EvidenceID(string(id)+"-diagnostics"), data)
}

// FinalReviewInput contains only controller-created inputs. The required-check
// result is checked against the current durable state before a new final
// reviewer session receives its deliberately independent context.
type FinalReviewInput struct {
	Owner       *SessionOwner
	Workspace   WorkspaceControl
	Run         *implementationstate.Run
	StateStore  *runstore.StateStore
	Journal     *runstore.Run
	Repository  string
	Rules       RulesIndex
	CheckResult implementationstate.ResultID
	OperationID implementationstate.OperationID
	ResultID    implementationstate.ResultID
	CallID      string
	RoundID     string
	Limits      implementationstate.CycleLimits
	Timeout     time.Duration
	Explorer    *FinalReviewExplorer
}

// FinalReviewExplorer contains controller-owned identities for an Explorer
// episode and the next turn in the same final-reviewer session. More than one
// request is explicit rather than letting the reviewer mint identifiers.
type FinalReviewExplorer struct {
	ExplorerOperationID     implementationstate.OperationID
	ExplorerResultID        implementationstate.ResultID
	ExplorerCallID          string
	ContinuationOperationID implementationstate.OperationID
	ContinuationResultID    implementationstate.ResultID
	ContinuationCallID      string
	ExplorerCharacters      int
	Additional              []FinalReviewExplorer
}

type FinalReviewResult struct {
	Response AgentResponse
	Session  *AgentSession
	Attempts uint64
	DiffBase string
	Evidence implementationstate.EvidenceRef
	ResultID implementationstate.ResultID
}

// StartFinalReview opens a new final-review session for this round. It passes
// no task-review history or command result data to that session; the
// controller separately verifies the required-check result before it may
// record success.
func StartFinalReview(ctx context.Context, input FinalReviewInput) (FinalReviewResult, error) {
	if err := validateFinalReviewInput(input); err != nil {
		return FinalReviewResult{}, err
	}
	if input.Owner == nil {
		return FinalReviewResult{}, fmt.Errorf("%w: final reviewer owner is required", ErrFinalAcceptanceRoute)
	}
	if _, err := ensureFinalCheckedWorkspace(ctx, input); err != nil {
		return FinalReviewResult{}, err
	}
	specification, err := input.Journal.Read(input.Run.Identity.Specification)
	if err != nil || strings.TrimSpace(string(specification)) == "" {
		return FinalReviewResult{}, fmt.Errorf("%w: read complete specification: %v", ErrFinalAcceptanceRoute, err)
	}
	diffBase := input.Run.Identity.BaselineCommit
	diff, err := effectiveWorkspaceControl(input.Workspace).AssignmentDiff(ctx, input.Repository, diffBase)
	if err != nil {
		return FinalReviewResult{}, fmt.Errorf("%w: capture aggregate final diff: %v", ErrFinalAcceptanceRoute, err)
	}
	start, err := BuildFinalReviewerStartContext(FinalReviewerStartInput{Specification: string(specification), Rules: input.Rules, Diff: diff})
	if err != nil {
		return FinalReviewResult{}, err
	}
	session, err := input.Owner.FinalReviewer(ctx, input.RoundID, start)
	if err != nil {
		return FinalReviewResult{}, fmt.Errorf("start final reviewer session: %w", err)
	}
	return runFinalReviewerTurn(ctx, input, session, diffBase)
}

func runFinalReviewerTurn(ctx context.Context, input FinalReviewInput, session *AgentSession, diffBase string) (FinalReviewResult, error) {
	if err := validateFinalReviewInput(input); err != nil {
		return FinalReviewResult{}, err
	}
	if session == nil || session.Role != ResponseRoleFinalReviewer {
		return FinalReviewResult{}, fmt.Errorf("%w: an independent final reviewer session is required", ErrFinalAcceptanceRoute)
	}
	basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	existing := finalRunOperation(input.Run, input.OperationID)
	if existing == nil {
		if err := input.Run.AddRunOperation(implementationstate.Operation{ID: input.OperationID, Kind: implementationstate.OperationReview, Basis: basis, Description: "independent final review", Counter: implementationstate.CycleCounterFinalReview}); err != nil {
			return FinalReviewResult{}, fmt.Errorf("%w: create final review operation: %v", ErrFinalAcceptanceRoute, err)
		}
		if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
			return FinalReviewResult{}, fmt.Errorf("%w: persist final review operation: %v", ErrFinalAcceptanceRoute, err)
		}
	} else if existing.Kind != implementationstate.OperationReview || existing.Basis != basis || existing.Description != "independent final review" || existing.Counter != implementationstate.CycleCounterFinalReview || finalRunResultForOperation(input.Run, existing.ID) != nil {
		return FinalReviewResult{}, fmt.Errorf("%w: final review operation cannot be resumed", ErrFinalAcceptanceRoute)
	}
	binding := ResponseBinding{CallID: input.CallID, RunID: input.Run.Identity.ID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
	call := finalReviewerCall(input, session, input.OperationID, ResponseExpectation{Role: ResponseRoleFinalReviewer, State: ResponseStateFinalReview, Scope: ResponseScopeRun, Binding: binding}, "Review the complete specification and aggregate final diff. Return a structured final review result.")
	return dispatchFinalReviewerCall(ctx, input, session, call, input.OperationID, input.ResultID, diffBase, 0)
}

func finalReviewerCall(input FinalReviewInput, session *AgentSession, operationID implementationstate.OperationID, expectation ResponseExpectation, message string) ControlledAgentCall {
	return ControlledAgentCall{Session: session, Repository: input.Repository, Workspace: input.Workspace, Policy: AgentCallPolicy{Role: AgentRoleFinalReviewer, CallID: expectation.Binding.CallID}, Run: input.Run, Journal: input.Journal, StateStore: input.StateStore, OperationID: operationID, Limits: input.Limits, Expectation: expectation, Message: message, Timeout: input.Timeout, ValidateResponse: validateFinalReviewerResponse}
}

func dispatchFinalReviewerCall(ctx context.Context, input FinalReviewInput, session *AgentSession, call ControlledAgentCall, operationID implementationstate.OperationID, resultID implementationstate.ResultID, diffBase string, episode int) (FinalReviewResult, error) {
	expected, err := ensureFinalCheckedWorkspace(ctx, input)
	if err != nil {
		return FinalReviewResult{Session: session, DiffBase: diffBase}, err
	}
	turn, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return FinalReviewResult{Session: session, Attempts: turn.Attempts, DiffBase: diffBase}, err
	}
	if err := ensureFinalCheckedWorkspaceAfterCall(ctx, input, expected, turn.Snapshot); err != nil {
		return FinalReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase}, err
	}
	if turn.Response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(turn.Response)
		if err != nil {
			return FinalReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase}, err
		}
		return FinalReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase}, PersistExecutionBlock(ctx, input.StateStore, input.Run, block)
	}
	result, err := persistFinalReviewOutcome(ctx, input, operationID, resultID, turn.Response)
	result.Session, result.Attempts, result.DiffBase = turn.Session, turn.Attempts, diffBase
	if err != nil || turn.Response.Kind != ResponseExplorationRequested {
		return result, err
	}
	return continueFinalReviewExplorer(ctx, input, turn.Session, call.Expectation, turn.Response, diffBase, episode)
}

func persistFinalReviewOutcome(ctx context.Context, input FinalReviewInput, operationID implementationstate.OperationID, resultID implementationstate.ResultID, response AgentResponse) (FinalReviewResult, error) {
	evidence, err := publishFinalReviewEvidence(input.Journal, resultID, response)
	if err != nil {
		return FinalReviewResult{}, err
	}
	status := implementationstate.ResultSucceeded
	if response.Kind == ResponseChangesRequested || response.Kind == ResponseClarificationNeeded {
		status = implementationstate.ResultFailed
	}
	basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	if err := input.Run.AddRunResult(implementationstate.OperationResult{ID: resultID, OperationID: operationID, Status: status, State: input.Run.CurrentState, Basis: basis, Evidence: []implementationstate.EvidenceRef{evidence}}); err != nil {
		return FinalReviewResult{}, fmt.Errorf("%w: record final review result: %v", ErrFinalAcceptanceRoute, err)
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return FinalReviewResult{}, fmt.Errorf("%w: persist final review result: %v", ErrFinalAcceptanceRoute, err)
	}
	if response.Kind == ResponseClarificationNeeded {
		if err := input.Run.Close("final review requires user resolution: " + *response.Question); err != nil {
			return FinalReviewResult{}, fmt.Errorf("%w: close for final specification question: %v", ErrFinalAcceptanceRoute, err)
		}
		if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
			return FinalReviewResult{}, fmt.Errorf("%w: persist final specification closure: %v", ErrFinalAcceptanceRoute, err)
		}
	}
	return FinalReviewResult{Response: response, Evidence: evidence, ResultID: resultID}, nil
}

func continueFinalReviewExplorer(ctx context.Context, input FinalReviewInput, session *AgentSession, sourceExpectation ResponseExpectation, request AgentResponse, diffBase string, episode int) (FinalReviewResult, error) {
	value := finalReviewExplorerAt(input.Explorer, episode)
	if value == nil || input.Owner == nil {
		return FinalReviewResult{}, fmt.Errorf("%w: final Explorer request has no controller-owned route", ErrFinalAcceptanceRoute)
	}
	if err := prepareFinalReviewExplorerOperations(ctx, input, value); err != nil {
		return FinalReviewResult{}, err
	}
	explorerExpectation := ResponseExpectation{Role: ResponseRoleExplorer, State: ResponseStateExploring, Scope: ResponseScopeRun, ExplorerSource: ExplorerSourceFinalReviewer, Binding: sourceExpectation.Binding}
	explorerExpectation.Binding.CallID = value.ExplorerCallID
	continuationExpectation := sourceExpectation
	continuationExpectation.Binding.CallID = value.ContinuationCallID
	explorerCall := ControlledAgentCall{Repository: input.Repository, Workspace: input.Workspace, Policy: AgentCallPolicy{Role: AgentRoleExplorer, CallID: value.ExplorerCallID}, Run: input.Run, Journal: input.Journal, StateStore: input.StateStore, OperationID: value.ExplorerOperationID, Limits: input.Limits, Expectation: explorerExpectation}
	continuation := finalReviewerCall(input, session, value.ContinuationOperationID, continuationExpectation, "")
	routed, err := RouteExplorer(ctx, ExplorerRoute{
		Owner: input.Owner, SourceSession: session, SourceExpectation: sourceExpectation, Request: request, ExplorerCall: explorerCall, ExplorerResultID: value.ExplorerResultID, SourceContinuation: continuation,
		ExplorerCharacters: value.ExplorerCharacters,
		PersistExplorerResponse: func(persistContext context.Context, result ControlledAgentCallResult) error {
			return persistFinalExplorerOutcome(persistContext, input, value, result.Response)
		},
	})
	if err != nil {
		return FinalReviewResult{}, err
	}
	if routed.Paused {
		return FinalReviewResult{Session: session, DiffBase: diffBase}, nil
	}
	expected, err := ensureFinalCheckedWorkspace(ctx, input)
	if err != nil {
		return FinalReviewResult{Session: session, DiffBase: diffBase}, err
	}
	if err := ensureFinalCheckedWorkspaceAfterCall(ctx, input, expected, routed.SourceContinuationSnapshot); err != nil {
		return FinalReviewResult{Response: routed.SourceContinuationResponse, Session: session, Attempts: routed.SourceContinuationAttempts, DiffBase: diffBase}, err
	}
	result, err := persistFinalReviewOutcome(ctx, input, value.ContinuationOperationID, value.ContinuationResultID, routed.SourceContinuationResponse)
	result.Session, result.Attempts, result.DiffBase = session, routed.SourceContinuationAttempts, diffBase
	if err != nil || routed.SourceContinuationResponse.Kind != ResponseExplorationRequested {
		return result, err
	}
	return continueFinalReviewExplorer(ctx, input, session, continuationExpectation, routed.SourceContinuationResponse, diffBase, episode+1)
}

func prepareFinalReviewExplorerOperations(ctx context.Context, input FinalReviewInput, value *FinalReviewExplorer) error {
	if value == nil || value.ExplorerOperationID == "" || value.ExplorerResultID == "" || strings.TrimSpace(value.ExplorerCallID) == "" || value.ContinuationOperationID == "" || value.ContinuationResultID == "" || strings.TrimSpace(value.ContinuationCallID) == "" {
		return fmt.Errorf("%w: final Explorer route identities are required", ErrFinalAcceptanceRoute)
	}
	basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	for _, operation := range []implementationstate.Operation{
		{ID: value.ExplorerOperationID, Kind: implementationstate.OperationAgent, Basis: basis, Description: "research for final review", Counter: implementationstate.CycleCounterExplorer, Episode: "final-reviewer"},
		{ID: value.ContinuationOperationID, Kind: implementationstate.OperationReview, Basis: basis, Description: "continue final review after research"},
	} {
		if finalRunOperation(input.Run, operation.ID) == nil {
			if err := input.Run.AddRunOperation(operation); err != nil {
				return err
			}
		}
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return fmt.Errorf("%w: persist final Explorer operations: %v", ErrFinalAcceptanceRoute, err)
	}
	return nil
}

func persistFinalExplorerOutcome(ctx context.Context, input FinalReviewInput, value *FinalReviewExplorer, response AgentResponse) error {
	if finalRunResult(input.Run, value.ExplorerResultID) != nil {
		return nil
	}
	evidence, err := publishFinalExplorerResponse(input.Journal, value.ExplorerResultID, response)
	if err != nil {
		return err
	}
	candidate, err := cloneFinalExplorerRun(input.Run)
	if err != nil {
		return err
	}
	status := implementationstate.ResultSucceeded
	if response.Kind == ResponseExecutionBlocked {
		status = implementationstate.ResultFailed
	}
	basis := implementationstate.AcceptanceBasis{Specification: candidate.Identity.Specification, Configuration: candidate.Identity.Configuration}
	if err := candidate.AddRunResult(implementationstate.OperationResult{ID: value.ExplorerResultID, OperationID: value.ExplorerOperationID, Status: status, State: candidate.CurrentState, Basis: basis, Evidence: []implementationstate.EvidenceRef{evidence}}); err != nil {
		return err
	}
	written, err := recordFinalExplorerState(context.WithoutCancel(ctx), input.StateStore, candidate)
	if written.Sequence != 0 {
		*input.Run = *candidate
	}
	if err != nil {
		return fmt.Errorf("%w: persist final Explorer result: %v", ErrFinalAcceptanceRoute, err)
	}
	return nil
}

func cloneFinalExplorerRun(run *implementationstate.Run) (*implementationstate.Run, error) {
	event, err := implementationstate.NewRunStateEvent(1, run)
	if err != nil {
		return nil, fmt.Errorf("%w: clone final Explorer state: %v", ErrFinalAcceptanceRoute, err)
	}
	return event.State, nil
}

func finalReviewExplorerAt(value *FinalReviewExplorer, episode int) *FinalReviewExplorer {
	if value == nil || episode < 0 {
		return nil
	}
	if episode == 0 {
		return value
	}
	if episode > len(value.Additional) {
		return nil
	}
	return &value.Additional[episode-1]
}

// CompleteFinalAcceptance is the terminal controller route. It rereads the
// durable checked-state snapshot immediately before success, so a reviewer
// cannot approve a working copy that changed after final checks.
func CompleteFinalAcceptance(ctx context.Context, run *implementationstate.Run, state *runstore.StateStore, journal *runstore.Run, workspace WorkspaceControl, repository string, checks implementationstate.ResultID, review FinalReviewResult) error {
	if run == nil || state == nil || journal == nil || strings.TrimSpace(repository) == "" || review.Response.Kind != ResponseReviewPassed || review.ResultID == "" || !currentSuccessfulFinalChecks(run, checks) {
		return fmt.Errorf("%w: successful final review and durable state are required", ErrFinalAcceptanceRoute)
	}
	if _, err := ensureFinalCheckedWorkspaceForCompletion(ctx, run, state, journal, workspace, repository, checks); err != nil {
		return err
	}
	if err := run.RecordFinalAcceptance(implementationstate.FinalAcceptanceEvidence{State: run.CurrentState, Basis: implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}, CheckResultIDs: []implementationstate.ResultID{checks}, ReviewResultID: review.ResultID}); err != nil {
		return fmt.Errorf("%w: record final acceptance: %v", ErrFinalAcceptanceRoute, err)
	}
	if err := run.Succeed(); err != nil {
		return fmt.Errorf("%w: complete final acceptance: %v", ErrFinalAcceptanceRoute, err)
	}
	if _, err := state.Record(context.WithoutCancel(ctx), run); err != nil {
		return fmt.Errorf("%w: persist successful run: %v", ErrFinalAcceptanceRoute, err)
	}
	return nil
}

func ensureFinalCheckedWorkspace(ctx context.Context, input FinalReviewInput) (git.Snapshot, error) {
	return ensureFinalCheckedWorkspaceForCompletion(ctx, input.Run, input.StateStore, input.Journal, input.Workspace, input.Repository, input.CheckResult)
}

func ensureFinalCheckedWorkspaceForCompletion(ctx context.Context, run *implementationstate.Run, state *runstore.StateStore, journal *runstore.Run, workspace WorkspaceControl, repository string, resultID implementationstate.ResultID) (git.Snapshot, error) {
	expected, err := finalCheckedSnapshot(run, journal, resultID)
	if err != nil {
		return git.Snapshot{}, err
	}
	if err := effectiveWorkspaceControl(workspace).EnsureUnchanged(ctx, repository, expected); err != nil {
		if run.Status == implementationstate.RunActive {
			if pauseErr := run.Pause(unexpectedWorkspaceChangePauseReason); pauseErr != nil {
				return git.Snapshot{}, fmt.Errorf("%w: pause changed final workspace: %v", ErrFinalAcceptanceRoute, pauseErr)
			}
			if _, recordErr := state.Record(context.WithoutCancel(ctx), run); recordErr != nil {
				return git.Snapshot{}, fmt.Errorf("%w: persist changed final workspace: %v", ErrFinalAcceptanceRoute, recordErr)
			}
		}
		return git.Snapshot{}, fmt.Errorf("%w: final checked workspace changed: %v", ErrFinalAcceptanceRoute, err)
	}
	return expected, nil
}

func ensureFinalCheckedWorkspaceAfterCall(ctx context.Context, input FinalReviewInput, expected, observed git.Snapshot) error {
	if !sameFinalCheckedSnapshot(expected, observed) {
		return pauseFinalWorkspaceChanged(ctx, input, "final reviewer call ended on a different checked state")
	}
	_, err := ensureFinalCheckedWorkspace(ctx, input)
	return err
}

func pauseFinalWorkspaceChanged(ctx context.Context, input FinalReviewInput, reason string) error {
	if input.Run.Status == implementationstate.RunActive {
		if err := input.Run.Pause(unexpectedWorkspaceChangePauseReason); err != nil {
			return fmt.Errorf("%w: pause changed final workspace: %v", ErrFinalAcceptanceRoute, err)
		}
		if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
			return fmt.Errorf("%w: persist changed final workspace: %v", ErrFinalAcceptanceRoute, err)
		}
	}
	return fmt.Errorf("%w: %s", ErrFinalAcceptanceRoute, reason)
}

func finalCheckedSnapshot(run *implementationstate.Run, journal *runstore.Run, resultID implementationstate.ResultID) (git.Snapshot, error) {
	if run == nil || journal == nil || !currentSuccessfulFinalChecks(run, resultID) {
		return git.Snapshot{}, fmt.Errorf("%w: current successful final checks are required", ErrFinalAcceptanceRoute)
	}
	result := finalRunResult(run, resultID)
	if result == nil || result.State != run.CurrentState {
		return git.Snapshot{}, fmt.Errorf("%w: final check state is not current", ErrFinalAcceptanceRoute)
	}
	if err := journal.VerifyReference(result.State); err != nil {
		return git.Snapshot{}, fmt.Errorf("%w: verify final checked state: %v", ErrFinalAcceptanceRoute, err)
	}
	data, err := journal.Read(result.State)
	if err != nil {
		return git.Snapshot{}, fmt.Errorf("%w: read final checked state: %v", ErrFinalAcceptanceRoute, err)
	}
	var snapshot git.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || !validFinalCheckedSnapshot(snapshot) {
		return git.Snapshot{}, fmt.Errorf("%w: decode final checked state: %v", ErrFinalAcceptanceRoute, err)
	}
	return snapshot, nil
}

func validFinalCheckedSnapshot(snapshot git.Snapshot) bool {
	return strings.TrimSpace(snapshot.HeadOID) != "" && strings.TrimSpace(snapshot.TreeOID) != "" && strings.TrimSpace(snapshot.IndexHash) != "" && strings.TrimSpace(snapshot.StatusHash) != "" && strings.TrimSpace(snapshot.SubmodulesHash) != ""
}

func sameFinalCheckedSnapshot(left, right git.Snapshot) bool {
	return left.HeadOID == right.HeadOID && left.HeadRef == right.HeadRef && left.TreeOID == right.TreeOID && left.IndexHash == right.IndexHash && left.StatusHash == right.StatusHash && left.SubmodulesHash == right.SubmodulesHash
}

func finalRunOperation(run *implementationstate.Run, id implementationstate.OperationID) *implementationstate.Operation {
	if run == nil {
		return nil
	}
	for index := range run.RunOperations {
		if run.RunOperations[index].ID == id {
			return &run.RunOperations[index]
		}
	}
	return nil
}

func finalRunResult(run *implementationstate.Run, id implementationstate.ResultID) *implementationstate.OperationResult {
	if run == nil {
		return nil
	}
	for index := range run.RunResults {
		if run.RunResults[index].ID == id {
			return &run.RunResults[index]
		}
	}
	return nil
}

func validateFinalReviewInput(input FinalReviewInput) error {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || strings.TrimSpace(input.Repository) == "" || input.CheckResult == "" || input.OperationID == "" || input.ResultID == "" || strings.TrimSpace(input.CallID) == "" || strings.TrimSpace(input.RoundID) == "" || !validTransitionLimits(input.Limits) {
		return fmt.Errorf("%w: durable state, final check, review identities, and limits are required", ErrFinalAcceptanceRoute)
	}
	if !runHasOnlyCompletedTasks(input.Run) || !currentSuccessfulFinalChecks(input.Run, input.CheckResult) {
		return fmt.Errorf("%w: final review requires current successful final required checks and no remaining tasks", ErrFinalAcceptanceRoute)
	}
	return validateRulesIndex(input.Rules)
}

func runHasOnlyCompletedTasks(run *implementationstate.Run) bool {
	if run == nil || run.Status != implementationstate.RunActive || len(run.Tasks) == 0 || len(run.Assignments) == 0 {
		return false
	}
	for _, status := range run.LeafStatus {
		if status != implementationstate.TaskComplete {
			return false
		}
	}
	for _, assignment := range run.Assignments {
		if assignment.Status != implementationstate.AssignmentCommitted {
			return false
		}
	}
	return true
}

func currentSuccessfulFinalChecks(run *implementationstate.Run, id implementationstate.ResultID) bool {
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	for _, result := range run.RunResults {
		if result.ID != id || result.Status != implementationstate.ResultSucceeded || result.State != run.CurrentState || result.Basis != basis {
			continue
		}
		for _, operation := range run.RunOperations {
			if operation.ID == result.OperationID && operation.Kind == implementationstate.OperationCheck && operation.Description == "final required checks" && operation.Basis == basis {
				return true
			}
		}
	}
	return false
}

func validateFinalReviewerResponse(response AgentResponse) error {
	if response.Kind == ResponseExecutionBlocked {
		_, err := ExecutionBlockFromResponse(response)
		return err
	}
	if response.Kind != ResponseReviewPassed && response.Kind != ResponseChangesRequested && response.Kind != ResponseClarificationNeeded && response.Kind != ResponseExplorationRequested {
		return fmt.Errorf("%w: final reviewer must pass, request blocking changes, raise a specification question, or request exploration", ErrFinalAcceptanceRoute)
	}
	if response.Binding.AssignmentID != "" || response.Binding.BriefID != "" {
		return fmt.Errorf("%w: final reviewer response must be run-scoped", ErrFinalAcceptanceRoute)
	}
	if response.Kind == ResponseChangesRequested {
		if len(response.FindingIDs) == 0 || len(response.FindingIDs) != len(response.Findings) || len(response.FindingIDs) != len(response.FindingDecisions) || len(response.FindingIDs) != len(response.FindingReasons) || len(response.FindingIDs) != len(response.Locations) || len(response.FindingIDs) != len(response.Bases) || len(response.FindingIDs) != len(response.ExpectedResults) {
			return fmt.Errorf("%w: final findings have inconsistent fields", ErrFinalAcceptanceRoute)
		}
		seen := map[string]bool{}
		for index, id := range response.FindingIDs {
			if seen[id] || implementationstate.FindingStatus(response.FindingDecisions[index]) != implementationstate.FindingOpen || !blockingFindingBasis(response.Bases[index]) || strings.TrimSpace(response.FindingReasons[index]) == "" {
				return fmt.Errorf("%w: final finding %q is not a concrete blocking finding", ErrFinalAcceptanceRoute, id)
			}
			seen[id] = true
		}
	}
	if response.Kind == ResponseClarificationNeeded && (response.Question == nil || strings.TrimSpace(*response.Question) == "") {
		return fmt.Errorf("%w: final specification question is empty", ErrFinalAcceptanceRoute)
	}
	return nil
}

func publishFinalReviewEvidence(journal *runstore.Run, resultID implementationstate.ResultID, response AgentResponse) (implementationstate.EvidenceRef, error) {
	data, err := json.Marshal(struct {
		Response AgentResponse `json:"response"`
	}{Response: response})
	if err != nil {
		return implementationstate.EvidenceRef{}, err
	}
	return journal.Publish(implementationstate.EvidenceID(string(resultID)+"-discussion"), data)
}

// publishFinalExplorerResponse uses the same response-only representation as
// the other Explorer routes. Its stable ID is also the crash-recovery marker:
// a later state event links this immutable artifact to the operation.
func publishFinalExplorerResponse(journal *runstore.Run, resultID implementationstate.ResultID, response AgentResponse) (implementationstate.EvidenceRef, error) {
	data, err := json.Marshal(response)
	if err != nil {
		return implementationstate.EvidenceRef{}, err
	}
	id := implementationstate.EvidenceID(string(resultID) + "-response")
	existing, err := journal.PublishedReference(id)
	if err == nil {
		stored, readErr := journal.Read(existing)
		if readErr != nil {
			return implementationstate.EvidenceRef{}, readErr
		}
		if !bytes.Equal(stored, data) {
			return implementationstate.EvidenceRef{}, fmt.Errorf("%w: final Explorer response differs from immutable published response", runstore.ErrConflictingPublication)
		}
		return existing, nil
	}
	if !errors.Is(err, runstore.ErrReferenceUnavailable) {
		return implementationstate.EvidenceRef{}, err
	}
	return journal.Publish(id, data)
}
