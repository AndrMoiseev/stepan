package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

var ErrImplementerTransition = errors.New("invalid implementer response transition")

// ErrRequiredChecksChanged is recorded when a successful required command
// changed the candidate workspace. The set is useful diagnostic evidence but
// cannot prove acceptance: every required command must be rerun against the
// newly observed state.
var ErrRequiredChecksChanged = errors.New("required checks changed the workspace")

// ImplementerTransitionInput contains the controller-owned dependencies for
// one response from the current executor session. Operation and result IDs are
// allocated by the controller; an agent cannot choose an executable command,
// an operation, or an assignment by putting one in its response.
type ImplementerTransitionInput struct {
	Run            *implstate.Run
	Workspace      WorkspaceControl
	StateStore     *runstore.StateStore
	Journal        *runstore.Run
	Repository     string
	AssignmentID   implstate.AssignmentID
	BriefID        implstate.BriefID
	Selection      setting.CheckSelection
	Runner         CheckRunner
	UserControl    *UserRunControl
	Limits         implstate.CycleLimits
	ProtectedPaths []string
	OperationID    implstate.OperationID
	ResultID       implstate.ResultID
}

// ImplementerTransitionResult is the bounded, durable check feedback to send
// in a following turn of the *same* executor session. The controller has no
// need to recreate that session merely because a configured check was run.
// RequiredAcceptance is true only after implementation_ready; it deliberately
// does not mean that the assignment was accepted or committed.
type ImplementerTransitionResult struct {
	Set                CheckSet
	Diagnostic         string
	Evidence           implstate.EvidenceRef
	RequiredAcceptance bool
	// WorkspaceChanged is meaningful only for RequiredAcceptance. It is true
	// for any mutation during the complete set, including a later reversion.
	// Such a set is deliberately nonterminal and consumes one mandatory round.
	WorkspaceChanged bool
	// ExecutionBlock is populated only for a required check that could not run
	// because of environment/configuration or controller infrastructure. Its
	// failed result remains durable evidence and the executor is not continued.
	ExecutionBlock *implstate.ExecutionBlock
}

// ValidateImplementerTransitionResponse is suitable for
// ControlledAgentCall.ValidateResponse. It rejects unknown check names before
// a response is accepted, so an attempted command string (for example
// "tests -run One") becomes a technical response retry instead of a command
// dispatch. The flat response schema already rejects all command/argument
// fields; this validation additionally binds names to the configured catalog.
func ValidateImplementerTransitionResponse(selection setting.CheckSelection, run *implstate.Run, assignmentID implstate.AssignmentID, briefID implstate.BriefID, response AgentResponse) error {
	if err := validateImplementerResponseBinding(run, assignmentID, briefID, response); err != nil {
		return err
	}
	if response.Kind != ResponseChecksRequested {
		if response.Kind != ResponseImplementationReady {
			return fmt.Errorf("%w: unsupported implementer response %q", ErrImplementerTransition, response.Kind)
		}
		return nil
	}
	if len(response.CheckNames) == 0 {
		return fmt.Errorf("%w: checks_requested has no check names", ErrImplementerTransition)
	}
	for _, name := range response.CheckNames {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: checks_requested has blank check name", ErrImplementerTransition)
		}
		if _, ok := selection.Checks[name]; !ok {
			return fmt.Errorf("%w: %w %q", ErrImplementerTransition, ErrUnknownCheck, name)
		}
	}
	return nil
}

// ApplyImplementerTransition runs the controller-owned next action selected
// by a validated executor response. checks_requested runs exactly the named
// configured checks in executor order. implementation_ready starts one full
// required acceptance set in project order, even if prior requested checks
// succeeded. It never accepts, completes, or commits the assignment.
func ApplyImplementerTransition(ctx context.Context, input ImplementerTransitionInput, response AgentResponse) (ImplementerTransitionResult, error) {
	if err := validateImplementerTransitionInput(input); err != nil {
		return ImplementerTransitionResult{}, err
	}
	if err := ValidateImplementerTransitionResponse(input.Selection, input.Run, input.AssignmentID, input.BriefID, response); err != nil {
		return ImplementerTransitionResult{}, err
	}

	kind := CheckSetRequested
	counter := implstate.CycleCounterChecksRequested
	description := "requested executor checks"
	if response.Kind == ResponseImplementationReady {
		kind = CheckSetRequired
		counter = implstate.CycleCounterMandatoryChecks
		description = "required acceptance checks"
	}
	basis := implstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	existing := assignmentOperation(input.Run, input.AssignmentID, input.OperationID)
	if existing == nil {
		if err := input.Run.AddOperation(input.AssignmentID, implstate.Operation{
			ID: input.OperationID, Kind: implstate.OperationCheck, BriefID: input.BriefID,
			Basis: basis, Description: description, Counter: counter,
		}); err != nil {
			return ImplementerTransitionResult{}, fmt.Errorf("%w: create check operation: %v", ErrImplementerTransition, err)
		}
		if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
			return ImplementerTransitionResult{}, fmt.Errorf("%w: persist check operation: %v", ErrImplementerTransition, err)
		}
	} else if existing.Kind != implstate.OperationCheck || existing.BriefID != input.BriefID || existing.Basis != basis || existing.Description != description || existing.Counter != counter || assignmentResultForOperation(input.Run, input.AssignmentID, existing.ID) != nil {
		return ImplementerTransitionResult{}, fmt.Errorf("%w: check operation cannot be resumed", ErrImplementerTransition)
	}
	if _, _, err := input.StateStore.RecordAssignmentAttemptStartWithLimits(ctx, input.Run, input.AssignmentID, input.OperationID, input.Limits); err != nil {
		return ImplementerTransitionResult{}, fmt.Errorf("%w: reserve check attempt: %w", ErrImplementerTransition, err)
	}

	publisher, err := NewCheckResultPublisherWithControl(input.Journal, input.Workspace, input.Repository, implstate.EvidenceID(input.ResultID))
	if err != nil {
		return ImplementerTransitionResult{}, fmt.Errorf("%w: create check publisher: %v", ErrImplementerTransition, err)
	}
	observer, err := NewWorkspaceCheckObserverWithControl(ctx, input.Workspace, input.Repository, input.Run, input.Journal, input.ProtectedPaths)
	if err != nil {
		return ImplementerTransitionResult{}, fmt.Errorf("%w: observe check workspace: %v", ErrImplementerTransition, err)
	}
	reporter := &WorkspaceCheckReporter{Observer: observer, Publisher: publisher}
	checkContext, finishCheck, err := beginUserControlledCheck(ctx, input.UserControl)
	if err != nil {
		return ImplementerTransitionResult{}, err
	}
	defer finishCheck()
	var set CheckSet
	var workspaceChanged bool
	if kind == CheckSetRequested {
		set, err = RunRequestedChecksWithReporter(checkContext, input.Selection, response.CheckNames, input.Runner, reporter)
	} else {
		cycle, cycleErr := RunOneRequiredCheckCycle(checkContext, input.Selection, input.Runner, reporter, 1)
		set, err, workspaceChanged = cycle.Set, cycleErr, cycle.Changed
	}
	diagnostic := checkSetDiagnostic(set, err)
	if kind == CheckSetRequired && workspaceChanged {
		if diagnostic != "" {
			diagnostic += "\n"
		}
		diagnostic += ErrRequiredChecksChanged.Error()
	}
	evidenceErr := err
	if kind == CheckSetRequired && workspaceChanged {
		evidenceErr = errors.Join(evidenceErr, ErrRequiredChecksChanged)
	}
	evidence, publishErr := publishImplementerCheckEvidence(input.Journal, input.ResultID, set, evidenceErr)
	if publishErr != nil {
		return ImplementerTransitionResult{}, fmt.Errorf("%w: publish check diagnostics: %v", ErrImplementerTransition, publishErr)
	}

	status, outcome := implstate.ResultSucceeded, implstate.AttemptSucceeded
	if err != nil || !set.Succeeded() || (kind == CheckSetRequired && workspaceChanged) {
		status, outcome = implstate.ResultFailed, implstate.AttemptFailed
		if errors.Is(checkContext.Err(), context.Canceled) || errors.Is(checkContext.Err(), context.DeadlineExceeded) {
			status, outcome = implstate.ResultInterrupted, implstate.AttemptInterrupted
		}
	}
	persistContext := context.WithoutCancel(checkContext)
	if _, recordErr := input.StateStore.RecordAssignmentAttemptOutcome(persistContext, input.Run, input.AssignmentID, input.OperationID, outcome, diagnostic); recordErr != nil {
		return ImplementerTransitionResult{}, fmt.Errorf("%w: persist check outcome: %v", ErrImplementerTransition, recordErr)
	}
	state := checkSetState(input.Run.CurrentState, set)
	if err := input.Run.AddResult(input.AssignmentID, implstate.OperationResult{
		ID: input.ResultID, OperationID: input.OperationID, Status: status, State: state, Basis: basis,
		Evidence: checkSetEvidenceRefs(evidence, set),
	}); err != nil {
		return ImplementerTransitionResult{}, fmt.Errorf("%w: record check result: %v", ErrImplementerTransition, err)
	}
	if state != input.Run.CurrentState {
		if err := input.Run.ObserveCodeState(state); err != nil {
			return ImplementerTransitionResult{}, fmt.Errorf("%w: record checked state: %v", ErrImplementerTransition, err)
		}
	}
	if _, recordErr := input.StateStore.Record(persistContext, input.Run); recordErr != nil {
		return ImplementerTransitionResult{}, fmt.Errorf("%w: persist check result: %v", ErrImplementerTransition, recordErr)
	}
	transition := ImplementerTransitionResult{Set: set, Diagnostic: diagnostic, Evidence: evidence, RequiredAcceptance: kind == CheckSetRequired, WorkspaceChanged: workspaceChanged}
	if UserOperationInterrupted(checkContext) {
		return transition, ErrUserOperationInterrupted
	}
	if failed, blocked := set.executionBlockedResult(); blocked {
		blockedAction := "run requested check " + failed.Name
		attempts := []string{"ran the configured requested check " + failed.Name}
		requiredUserAction := "repair the check environment or project configuration without weakening the check, then explicitly resume or close the run"
		if kind == CheckSetRequired {
			blockedAction = "run required check " + failed.Name
			attempts = []string{"ran the configured required check " + failed.Name}
			requiredUserAction = "repair the required-check environment or project configuration without weakening the check, then explicitly resume or close the run"
		}
		block, blockErr := ExecutionBlockForUserRemediation(
			blockedAction, checkExecutionDiagnostic(failed, diagnostic), attempts, requiredUserAction,
		)
		if blockErr != nil {
			return ImplementerTransitionResult{}, blockErr
		}
		if err := PersistExecutionBlock(persistContext, input.StateStore, input.Run, block); err != nil {
			return ImplementerTransitionResult{}, err
		}
		transition.ExecutionBlock = &block
	}
	return transition, nil
}

func validateImplementerTransitionInput(input ImplementerTransitionInput) error {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || input.Runner == nil || strings.TrimSpace(input.Repository) == "" || input.AssignmentID == "" || input.BriefID == "" || input.OperationID == "" || input.ResultID == "" {
		return fmt.Errorf("%w: run, store, journal, repository, assignment, brief, runner, operation, and result are required", ErrImplementerTransition)
	}
	if !validTransitionLimits(input.Limits) {
		return fmt.Errorf("%w: invalid check limits", ErrImplementerTransition)
	}
	return nil
}

func validTransitionLimits(limits implstate.CycleLimits) bool {
	return limits.AssignmentReview > 0 && limits.MandatoryChecks > 0 && limits.ChecksRequested > 0 && limits.BriefRefinement > 0 && limits.Explorer > 0 && limits.TechnicalAttempts > 0 && limits.FinalReview > 0
}

func validateImplementerResponseBinding(run *implstate.Run, assignmentID implstate.AssignmentID, briefID implstate.BriefID, response AgentResponse) error {
	if run == nil || strings.TrimSpace(response.Binding.CallID) == "" || response.Binding.RunID != run.Identity.ID || response.Binding.AssignmentID != assignmentID || response.Binding.BriefID != briefID || response.Binding.Specification != run.Identity.Specification || response.Binding.Configuration != run.Identity.Configuration || response.Binding.TaskList != run.Identity.TaskList {
		return fmt.Errorf("%w: response is not bound to the current assignment", ErrImplementerTransition)
	}
	return nil
}

func checkSetState(fallback implstate.EvidenceRef, set CheckSet) implstate.EvidenceRef {
	state := fallback
	for _, result := range set.Results {
		if result.Presentation != nil {
			state = result.Presentation.CheckedState.Reference
		}
	}
	return state
}

func checkSetEvidenceRefs(summary implstate.EvidenceRef, set CheckSet) []implstate.EvidenceRef {
	references := []implstate.EvidenceRef{summary}
	for _, result := range set.Results {
		if result.Presentation == nil {
			continue
		}
		references = append(references, result.Presentation.Stdout.Reference, result.Presentation.Stderr.Reference, result.Presentation.CheckedState.Reference)
	}
	return references
}

func checkSetDiagnostic(set CheckSet, setErr error) string {
	parts := make([]string, 0, len(set.Results)+1)
	if setErr != nil {
		parts = append(parts, setErr.Error())
	}
	for _, result := range set.Results {
		part := fmt.Sprintf("%s: %s", result.Name, result.Status)
		if result.Err != nil {
			part += ": " + result.Err.Error()
		}
		if result.Presentation != nil && result.Presentation.Diagnostics != "" {
			part += "\n" + result.Presentation.Diagnostics
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n")
}

func checkExecutionDiagnostic(result CheckSetResult, fallback string) string {
	if result.Err != nil {
		return result.Err.Error()
	}
	if result.Result.Failure != checkexec.FailureNone {
		return fmt.Sprintf("required check %q failed with %s", result.Name, result.Result.Failure)
	}
	return fallback
}

func publishImplementerCheckEvidence(journal *runstore.Run, id implstate.ResultID, set CheckSet, setErr error) (implstate.EvidenceRef, error) {
	evidence := struct {
		Kind    CheckSetKind                     `json:"kind"`
		Results []implementerCheckResultEvidence `json:"results"`
		Error   string                           `json:"error,omitempty"`
	}{Kind: set.Kind}
	for _, result := range set.Results {
		item := implementerCheckResultEvidence{Name: result.Name, Status: result.Status, Presentation: result.Presentation}
		if result.Command.Program != "" {
			item.Command = RenderCheckCommand(result.Command)
		}
		if result.Err != nil {
			item.Error = result.Err.Error()
		}
		evidence.Results = append(evidence.Results, item)
	}
	if setErr != nil {
		evidence.Error = setErr.Error()
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return implstate.EvidenceRef{}, err
	}
	return journal.Publish(implstate.EvidenceID(string(id)+"-diagnostics"), data)
}

// implementerCheckResultEvidence deliberately excludes command environment
// values while retaining the bounded presentation and durable artifact refs.
type implementerCheckResultEvidence struct {
	Name         string             `json:"name"`
	Status       CheckStatus        `json:"status"`
	Command      string             `json:"command,omitempty"`
	Error        string             `json:"error,omitempty"`
	Presentation *CheckPresentation `json:"presentation,omitempty"`
}
