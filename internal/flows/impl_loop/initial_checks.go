package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var ErrInitialRequiredChecks = errors.New("invalid initial required checks")

const initialRequiredChecksPauseReason = "initial required checks did not pass"

// InitialRequiredChecks is the controller-owned baseline gate between formal
// task extraction and the first assignment. It deliberately has no agent
// dependency: an unhealthy starting project is a user decision, not new
// implementation scope.
type InitialRequiredChecks struct {
	Run        *implementationstate.Run
	StateStore *runstore.StateStore
	Journal    *runstore.Run
	Repository string
	Selection  implementationconfig.CheckSelection
	Runner     CheckRunner
	Operation  implementationstate.OperationID
	Result     implementationstate.ResultID
}

// InitialRequiredChecksResult exposes the bounded diagnostics that a UI can
// show to the user. Full output, post-command states, and this exact summary
// are retained as immutable evidence in the run store.
type InitialRequiredChecksResult struct {
	Set        CheckSet
	Diagnostic string
	Evidence   implementationstate.EvidenceRef
}

// RunInitialRequiredChecks runs the complete configured required set once on
// the current initial baseline. It records the operation and its attempt
// before dispatching any command, stops at the first failure/inapplicable
// check through the shared check-set machinery, and pauses the run on every
// incomplete set. It never invokes an implementer or changes task scope.
func RunInitialRequiredChecks(ctx context.Context, input InitialRequiredChecks) (InitialRequiredChecksResult, error) {
	if err := validateInitialRequiredChecks(input); err != nil {
		return InitialRequiredChecksResult{}, err
	}
	basis := implementationstate.AcceptanceBasis{
		Specification: input.Run.Identity.Specification,
		Configuration: input.Run.Identity.Configuration,
	}
	if err := input.Run.AddRunOperation(implementationstate.Operation{
		ID: input.Operation, Kind: implementationstate.OperationCheck, Basis: basis,
		Description: "initial required checks", Counter: implementationstate.CycleCounterNone,
	}); err != nil {
		return InitialRequiredChecksResult{}, fmt.Errorf("%w: create baseline operation: %v", ErrInitialRequiredChecks, err)
	}
	if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
		return InitialRequiredChecksResult{}, fmt.Errorf("%w: persist baseline operation: %v", ErrInitialRequiredChecks, err)
	}
	if _, _, err := input.StateStore.RecordRunAttemptStart(ctx, input.Run, input.Operation); err != nil {
		return InitialRequiredChecksResult{}, fmt.Errorf("%w: reserve baseline check attempt: %v", ErrInitialRequiredChecks, err)
	}

	publisher, err := NewCheckResultPublisher(input.Journal, input.Repository, implementationstate.EvidenceID(input.Result))
	if err != nil {
		return pauseInitialChecks(ctx, input, InitialRequiredChecksResult{}, fmt.Errorf("create baseline check publisher: %w", err))
	}
	set, runErr := RunRequiredChecksWithReporter(ctx, input.Selection, input.Runner, publisher)
	diagnostic := initialCheckDiagnostic(set, runErr)
	evidence, publishErr := publishInitialCheckEvidence(input.Journal, input.Result, set, runErr)
	result := InitialRequiredChecksResult{Set: set, Diagnostic: diagnostic, Evidence: evidence}
	if publishErr != nil {
		return pauseInitialChecks(ctx, input, result, fmt.Errorf("publish baseline check diagnostics: %w", publishErr))
	}

	status := implementationstate.ResultSucceeded
	outcome := implementationstate.AttemptSucceeded
	if runErr != nil || !set.Succeeded() {
		status, outcome = implementationstate.ResultFailed, implementationstate.AttemptFailed
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status, outcome = implementationstate.ResultInterrupted, implementationstate.AttemptInterrupted
		}
	}
	if _, err := input.StateStore.RecordRunAttemptOutcome(ctx, input.Run, input.Operation, outcome, diagnostic); err != nil {
		return InitialRequiredChecksResult{}, fmt.Errorf("%w: persist baseline check outcome: %v", ErrInitialRequiredChecks, err)
	}
	state := finalInitialCheckedState(input.Run.CurrentState, set)
	if err := input.Run.AddRunResult(implementationstate.OperationResult{
		ID: input.Result, OperationID: input.Operation, Status: status, State: state, Basis: basis,
		Evidence: initialCheckEvidenceRefs(evidence, set),
	}); err != nil {
		return InitialRequiredChecksResult{}, fmt.Errorf("%w: record baseline check result: %v", ErrInitialRequiredChecks, err)
	}
	if state != input.Run.CurrentState {
		if err := input.Run.ObserveCodeState(state); err != nil {
			return InitialRequiredChecksResult{}, fmt.Errorf("%w: record checked baseline state: %v", ErrInitialRequiredChecks, err)
		}
	}
	if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
		return InitialRequiredChecksResult{}, fmt.Errorf("%w: persist baseline check result: %v", ErrInitialRequiredChecks, err)
	}
	if status != implementationstate.ResultSucceeded {
		return pauseInitialChecks(ctx, input, result, nil)
	}
	return result, nil
}

func validateInitialRequiredChecks(input InitialRequiredChecks) error {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || input.Runner == nil || strings.TrimSpace(input.Repository) == "" || input.Operation == "" || input.Result == "" {
		return fmt.Errorf("%w: run, store, journal, repository, runner, operation, and result are required", ErrInitialRequiredChecks)
	}
	if input.Run.Status != implementationstate.RunActive || input.Run.TaskExtractionPending || len(input.Run.Tasks) == 0 || input.Run.CurrentState != input.Run.Identity.BaselineState {
		return fmt.Errorf("%w: checks must run once after extraction on the current initial baseline", ErrInitialRequiredChecks)
	}
	return nil
}

func pauseInitialChecks(ctx context.Context, input InitialRequiredChecks, result InitialRequiredChecksResult, cause error) (InitialRequiredChecksResult, error) {
	if input.Run.Status == implementationstate.RunActive {
		if err := input.Run.Pause(initialRequiredChecksPauseReason); err != nil {
			return result, errors.Join(cause, fmt.Errorf("pause after initial required checks: %w", err))
		}
		if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
			return result, errors.Join(cause, fmt.Errorf("persist initial required-check pause: %w", err))
		}
	}
	return result, cause
}

func finalInitialCheckedState(fallback implementationstate.EvidenceRef, set CheckSet) implementationstate.EvidenceRef {
	state := fallback
	for _, result := range set.Results {
		if result.Presentation != nil {
			state = result.Presentation.CheckedState.Reference
		}
	}
	return state
}

func initialCheckEvidenceRefs(summary implementationstate.EvidenceRef, set CheckSet) []implementationstate.EvidenceRef {
	references := []implementationstate.EvidenceRef{summary}
	for _, result := range set.Results {
		if result.Presentation == nil {
			continue
		}
		references = append(references,
			result.Presentation.Stdout.Reference,
			result.Presentation.Stderr.Reference,
			result.Presentation.CheckedState.Reference,
		)
	}
	return references
}

type initialCheckSetEvidence struct {
	Kind    CheckSetKind                 `json:"kind"`
	Results []initialCheckResultEvidence `json:"results"`
	Error   string                       `json:"error,omitempty"`
}

type initialCheckResultEvidence struct {
	Name         string             `json:"name"`
	Status       CheckStatus        `json:"status"`
	Command      string             `json:"command,omitempty"`
	Error        string             `json:"error,omitempty"`
	Presentation *CheckPresentation `json:"presentation,omitempty"`
}

func publishInitialCheckEvidence(journal *runstore.Run, id implementationstate.ResultID, set CheckSet, setErr error) (implementationstate.EvidenceRef, error) {
	evidence := initialCheckSetEvidence{Kind: set.Kind, Results: make([]initialCheckResultEvidence, 0, len(set.Results))}
	if setErr != nil {
		evidence.Error = setErr.Error()
	}
	for _, result := range set.Results {
		item := initialCheckResultEvidence{Name: result.Name, Status: result.Status, Presentation: result.Presentation}
		if result.Command.Program != "" {
			item.Command = RenderCheckCommand(result.Command)
		}
		if result.Err != nil {
			item.Error = result.Err.Error()
		}
		evidence.Results = append(evidence.Results, item)
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return implementationstate.EvidenceRef{}, err
	}
	return journal.Publish(implementationstate.EvidenceID(string(id)+"-diagnostics"), data)
}

func initialCheckDiagnostic(set CheckSet, setErr error) string {
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
