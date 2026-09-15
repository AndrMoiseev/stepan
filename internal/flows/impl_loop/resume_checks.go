package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

var ErrResumeRequiredChecks = errors.New("invalid resume required checks")

// ResumeRequiredChecksResult is the durable, uncounted mandatory-check
// evidence produced by one explicit /resume.
type ResumeRequiredChecksResult struct {
	Set        CheckSet
	Diagnostic string
	Evidence   implementationstate.EvidenceRef
}

// runResumeRequiredChecks executes one complete required set from its first
// configured command.  Its operation is explicitly uncounted: unlike normal
// acceptance work, it must neither consume a semantic round nor a technical
// retry merely because the user resumed a paused run.  Nevertheless both the
// operation and result are durable, so an interrupted command cannot be
// mistaken for successful current evidence after recovery.
func runResumeRequiredChecks(ctx context.Context, input ResumeInput, selection implementationconfig.CheckSelection) (ResumeRequiredChecksResult, error) {
	if err := validateResumeRequiredChecks(input); err != nil {
		return ResumeRequiredChecksResult{}, err
	}
	operationID, resultID := nextResumeCheckIDs(input.Run)
	basis := implementationstate.AcceptanceBasis{
		Specification: input.Run.Identity.Specification,
		Configuration: input.Run.Identity.Configuration,
	}
	if err := input.Run.AddRunOperation(implementationstate.Operation{
		ID: operationID, Kind: implementationstate.OperationCheck, Basis: basis,
		Description: "resume required checks", Counter: implementationstate.CycleCounterNone,
		UncountedResumeCheck: true,
	}); err != nil {
		return ResumeRequiredChecksResult{}, fmt.Errorf("%w: create uncounted resume check operation: %v", ErrResumeRequiredChecks, err)
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return ResumeRequiredChecksResult{}, fmt.Errorf("%w: persist uncounted resume check operation: %v", ErrResumeRequiredChecks, err)
	}

	publisher, err := NewCheckResultPublisherWithControl(input.Journal, input.Workspace, input.Repository, implementationstate.EvidenceID(resultID))
	if err != nil {
		return pauseResumeRequiredChecks(ctx, input, ResumeRequiredChecksResult{}, fmt.Errorf("create resume check publisher: %w", err))
	}
	observer, err := NewWorkspaceCheckObserverWithControl(ctx, input.Workspace, input.Repository, input.Run, input.Journal, input.ProtectedPaths)
	if err != nil {
		return pauseResumeRequiredChecks(ctx, input, ResumeRequiredChecksResult{}, fmt.Errorf("create resume workspace observer: %w", err))
	}
	checkContext, finishCheck, err := beginUserControlledCheck(ctx, input.UserControl)
	if err != nil {
		return ResumeRequiredChecksResult{}, err
	}
	defer finishCheck()
	set, runErr := RunRequiredChecksWithReporter(checkContext, selection, input.Runner, &WorkspaceCheckReporter{Observer: observer, Publisher: publisher})
	diagnostic := checkSetDiagnostic(set, runErr)
	evidence, publishErr := publishImplementerCheckEvidence(input.Journal, resultID, set, runErr)
	result := ResumeRequiredChecksResult{Set: set, Diagnostic: diagnostic, Evidence: evidence}
	if publishErr != nil {
		return pauseResumeRequiredChecks(context.WithoutCancel(checkContext), input, result, fmt.Errorf("publish resume check diagnostics: %w", publishErr))
	}

	status := implementationstate.ResultSucceeded
	if runErr != nil || !set.Succeeded() {
		status = implementationstate.ResultFailed
		if errors.Is(checkContext.Err(), context.Canceled) || errors.Is(checkContext.Err(), context.DeadlineExceeded) {
			status = implementationstate.ResultInterrupted
		}
	}
	// The observer starts after reconciliation. Its mutation generation is
	// therefore limited to writes made by this check set, whereas every checked
	// state artifact also includes a permitted rules/tasks delta that resume
	// may already have accepted. Retain fresh artifacts in either case, but do
	// not turn that pre-check delta into a code-state transition.
	state := input.Run.CurrentState
	if observer.mutationGeneration != 0 {
		state = checkSetState(state, set)
	}
	if err := input.Run.AddRunResult(implementationstate.OperationResult{
		ID: resultID, OperationID: operationID, Status: status, State: state, Basis: basis,
		Evidence: checkSetEvidenceRefs(evidence, set),
	}); err != nil {
		return ResumeRequiredChecksResult{}, fmt.Errorf("%w: record resume check result: %v", ErrResumeRequiredChecks, err)
	}
	if state != input.Run.CurrentState {
		if err := input.Run.ObserveCodeState(state); err != nil {
			return ResumeRequiredChecksResult{}, fmt.Errorf("%w: record resume checked state: %v", ErrResumeRequiredChecks, err)
		}
	}
	if status == implementationstate.ResultSucceeded && (input.Run.InitialBaseline == nil || input.Run.InitialBaseline.Basis != basis) {
		if err := input.Run.RefreshInitialBaselinePass(operationID, resultID); err != nil {
			return ResumeRequiredChecksResult{}, fmt.Errorf("%w: refresh initial acceptance baseline: %v", ErrResumeRequiredChecks, err)
		}
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(checkContext), input.Run); err != nil {
		return ResumeRequiredChecksResult{}, fmt.Errorf("%w: persist resume check result: %v", ErrResumeRequiredChecks, err)
	}
	if status == implementationstate.ResultSucceeded {
		return result, nil
	}
	if UserOperationInterrupted(checkContext) {
		return result, ErrUserOperationInterrupted
	}
	return pauseResumeRequiredChecks(context.WithoutCancel(checkContext), input, result, nil)
}

func validateResumeRequiredChecks(input ResumeInput) error {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || input.Runner == nil || strings.TrimSpace(input.Repository) == "" {
		return fmt.Errorf("%w: run, store, journal, repository, and runner are required", ErrResumeRequiredChecks)
	}
	if input.Run.Status != implementationstate.RunActive {
		return fmt.Errorf("%w: reconciled run must be active before its mandatory gate", ErrResumeRequiredChecks)
	}
	return nil
}

func nextResumeCheckIDs(run *implementationstate.Run) (implementationstate.OperationID, implementationstate.ResultID) {
	operations := make(map[implementationstate.OperationID]struct{})
	results := make(map[implementationstate.ResultID]struct{})
	if run != nil {
		for _, operation := range run.RunOperations {
			operations[operation.ID] = struct{}{}
		}
		for _, result := range run.RunResults {
			results[result.ID] = struct{}{}
		}
	}
	for number := 1; ; number++ {
		operation := implementationstate.OperationID(fmt.Sprintf("resume-required-check-%d", number))
		result := implementationstate.ResultID(fmt.Sprintf("resume-required-check-%d-result", number))
		if _, exists := operations[operation]; exists {
			continue
		}
		if _, exists := results[result]; !exists {
			return operation, result
		}
	}
}

func pauseResumeRequiredChecks(ctx context.Context, input ResumeInput, result ResumeRequiredChecksResult, cause error) (ResumeRequiredChecksResult, error) {
	if input.Run.Status != implementationstate.RunActive {
		return result, cause
	}
	diagnostic := strings.TrimSpace(result.Diagnostic)
	if diagnostic == "" && cause != nil {
		diagnostic = cause.Error()
	}
	if diagnostic == "" {
		diagnostic = "resume required checks did not pass"
	}
	block, blockErr := ExecutionBlockForUserRemediation(
		"run the required checks before resuming work", diagnostic,
		[]string{"ran the full configured required-check set from its first command without consuming implementation attempts"},
		"repair the environment or project configuration without weakening the required checks, then explicitly resume or close the run",
	)
	if blockErr != nil {
		return result, errors.Join(cause, blockErr)
	}
	if err := PersistExecutionBlock(ctx, input.StateStore, input.Run, block); err != nil {
		return result, errors.Join(cause, err)
	}
	return result, cause
}
