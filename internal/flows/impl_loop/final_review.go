package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var ErrFinalAcceptanceRoute = errors.New("invalid final acceptance route")

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
	if err := input.Run.AddRunOperation(implementationstate.Operation{ID: input.Operation, Kind: implementationstate.OperationCheck, Basis: basis, Description: "final required checks"}); err != nil {
		return FinalRequiredChecksResult{}, fmt.Errorf("%w: create final check operation: %v", ErrFinalAcceptanceRoute, err)
	}
	if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
		return FinalRequiredChecksResult{}, fmt.Errorf("%w: persist final check operation: %v", ErrFinalAcceptanceRoute, err)
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
	convergence, runErr := RunRequiredChecksUntilStable(ctx, input.Selection, input.Runner, &WorkspaceCheckReporter{Observer: observer, Publisher: publisher}, input.MaxCycles)
	set := initialCheckSet(convergence)
	diagnostic := initialCheckDiagnostic(set, runErr)
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkResultPersistenceTimeout)
	defer cancel()
	evidence, publishErr := publishFinalCheckEvidence(input.Journal, input.Result, convergence, runErr)
	result := FinalRequiredChecksResult{Set: set, Convergence: convergence, Diagnostic: diagnostic, Evidence: evidence}
	if publishErr != nil {
		return pauseFinalChecks(persistContext, input, result, fmt.Errorf("publish final check diagnostics: %w", publishErr))
	}
	status, outcome := implementationstate.ResultSucceeded, implementationstate.AttemptSucceeded
	if runErr != nil || !set.Succeeded() {
		status, outcome = implementationstate.ResultFailed, implementationstate.AttemptFailed
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
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
	if err := input.Run.AddRunOperation(implementationstate.Operation{ID: input.OperationID, Kind: implementationstate.OperationReview, Basis: basis, Description: "independent final review", Counter: implementationstate.CycleCounterFinalReview}); err != nil {
		return FinalReviewResult{}, fmt.Errorf("%w: create final review operation: %v", ErrFinalAcceptanceRoute, err)
	}
	if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
		return FinalReviewResult{}, fmt.Errorf("%w: persist final review operation: %v", ErrFinalAcceptanceRoute, err)
	}
	binding := ResponseBinding{CallID: input.CallID, RunID: input.Run.Identity.ID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
	call := ControlledAgentCall{Session: session, Repository: input.Repository, Workspace: input.Workspace, Policy: AgentCallPolicy{Role: AgentRoleFinalReviewer, CallID: input.CallID}, Run: input.Run, Journal: input.Journal, StateStore: input.StateStore, OperationID: input.OperationID, Limits: input.Limits, Expectation: ResponseExpectation{Role: ResponseRoleFinalReviewer, State: ResponseStateFinalReview, Scope: ResponseScopeRun, Binding: binding}, Message: "Review the complete specification and aggregate final diff. Return a structured final review result."}
	call.Timeout = input.Timeout
	call.ValidateResponse = validateFinalReviewerResponse
	turn, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return FinalReviewResult{Session: session, Attempts: turn.Attempts, DiffBase: diffBase}, err
	}
	if turn.Response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(turn.Response)
		if err != nil {
			return FinalReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase}, err
		}
		return FinalReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase}, PersistExecutionBlock(ctx, input.StateStore, input.Run, block)
	}
	evidence, err := publishFinalReviewEvidence(input.Journal, input.ResultID, turn.Response)
	if err != nil {
		return FinalReviewResult{}, err
	}
	status := implementationstate.ResultSucceeded
	if turn.Response.Kind == ResponseChangesRequested || turn.Response.Kind == ResponseClarificationNeeded {
		status = implementationstate.ResultFailed
	}
	if err := input.Run.AddRunResult(implementationstate.OperationResult{ID: input.ResultID, OperationID: input.OperationID, Status: status, State: input.Run.CurrentState, Basis: basis, Evidence: []implementationstate.EvidenceRef{evidence}}); err != nil {
		return FinalReviewResult{}, fmt.Errorf("%w: record final review result: %v", ErrFinalAcceptanceRoute, err)
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return FinalReviewResult{}, fmt.Errorf("%w: persist final review result: %v", ErrFinalAcceptanceRoute, err)
	}
	if turn.Response.Kind == ResponseClarificationNeeded {
		if err := input.Run.Close("final review requires user resolution: " + *turn.Response.Question); err != nil {
			return FinalReviewResult{}, fmt.Errorf("%w: close for final specification question: %v", ErrFinalAcceptanceRoute, err)
		}
		if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
			return FinalReviewResult{}, fmt.Errorf("%w: persist final specification closure: %v", ErrFinalAcceptanceRoute, err)
		}
	}
	return FinalReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase, Evidence: evidence, ResultID: input.ResultID}, nil
}

// CompleteFinalAcceptance is the terminal controller route. It intentionally
// has no Git or provider side effects: passing checks and a positive review
// are necessary evidence, and only then does it persist local success.
func CompleteFinalAcceptance(ctx context.Context, run *implementationstate.Run, state *runstore.StateStore, checks implementationstate.ResultID, review FinalReviewResult) error {
	if run == nil || state == nil || review.Response.Kind != ResponseReviewPassed || review.ResultID == "" || !currentSuccessfulFinalChecks(run, checks) {
		return fmt.Errorf("%w: successful final review and durable state are required", ErrFinalAcceptanceRoute)
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
	for _, result := range run.RunResults {
		if result.ID != id || result.Status != implementationstate.ResultSucceeded || result.State != run.CurrentState {
			continue
		}
		for _, operation := range run.RunOperations {
			if operation.ID == result.OperationID && operation.Kind == implementationstate.OperationCheck && operation.Description == "final required checks" {
				return true
			}
		}
	}
	return false
}

func validateFinalReviewerResponse(response AgentResponse) error {
	if response.Kind != ResponseReviewPassed && response.Kind != ResponseChangesRequested && response.Kind != ResponseClarificationNeeded {
		return fmt.Errorf("%w: final reviewer must pass, request blocking changes, or raise a specification question", ErrFinalAcceptanceRoute)
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
