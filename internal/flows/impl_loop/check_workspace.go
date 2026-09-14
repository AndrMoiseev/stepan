package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var (
	// ErrCheckWorkspaceViolation means a configured command changed a path
	// owned by the controller. Its result cannot be used as acceptance
	// evidence, even if the command itself exited successfully.
	ErrCheckWorkspaceViolation = errors.New("configured check changed a protected path")
	// ErrCheckWorkspaceUnstable means every attempted complete required set
	// changed the workspace, so the controller could not establish a stable
	// checked state within the configured bound.
	ErrCheckWorkspaceUnstable = errors.New("configured checks did not converge to a stable workspace")
)

// AssignmentDiff is the current candidate diff for an assignment. It starts
// at the state handed to the executor and accumulates code, generated files,
// and check side effects. Paths are repository-relative and sorted.
type AssignmentDiff struct {
	Baseline gitsnapshot.Snapshot
	Current  gitsnapshot.Snapshot
	Paths    []string
}

// WorkspaceCheckObserver makes command-produced writes first-class assignment
// changes. Protected paths are controller-owned inputs (configuration, rules,
// specs, and durable state); ordinary generated code is intentionally allowed.
// If a run is supplied, a changed accepted candidate reopens its assignment
// through ObserveCodeState using the durable checked-state evidence.
type WorkspaceCheckObserver struct {
	repository     string
	workspace      WorkspaceControl
	run            *implementationstate.Run
	journal        *runstore.Run
	protectedPaths []string
	diff           AssignmentDiff
	before         gitsnapshot.Snapshot
	pending        bool
	// candidateChanged is the mutation produced by the most recently observed
	// command after any protected paths have been restored. mutationGeneration
	// is monotonic so a required set that changes then reverts a file still
	// cannot be accepted as stable.
	candidateChanged   bool
	mutationGeneration uint64
}

// NewWorkspaceCheckObserver captures the assignment's initial candidate
// state. The caller must retain one observer for the complete assignment.
func NewWorkspaceCheckObserver(ctx context.Context, repository string, run *implementationstate.Run, journal *runstore.Run, protectedPaths []string) (*WorkspaceCheckObserver, error) {
	return NewWorkspaceCheckObserverWithControl(ctx, GitWorkspaceControl{}, repository, run, journal, protectedPaths)
}

// NewWorkspaceCheckObserverWithControl constructs an observer at the workspace
// seam. Production uses GitWorkspaceControl; orchestration tests can supply a
// deterministic adapter while real mutation/restore behavior remains covered
// by the default constructor's contract tests.
func NewWorkspaceCheckObserverWithControl(ctx context.Context, workspace WorkspaceControl, repository string, run *implementationstate.Run, journal *runstore.Run, protectedPaths []string) (*WorkspaceCheckObserver, error) {
	if strings.TrimSpace(repository) == "" {
		return nil, errors.New("workspace check observer requires a repository")
	}
	workspace = effectiveWorkspaceControl(workspace)
	baseline, err := workspace.Capture(ctx, repository)
	if err != nil {
		return nil, fmt.Errorf("capture assignment workspace: %w", err)
	}
	paths := make([]string, 0, len(protectedPaths))
	for _, path := range protectedPaths {
		normalized, err := validateCallPath(path)
		if err != nil {
			return nil, fmt.Errorf("invalid protected check path: %w", err)
		}
		paths = append(paths, normalized)
	}
	slices.Sort(paths)
	return &WorkspaceCheckObserver{repository: repository, workspace: workspace, run: run, journal: journal, protectedPaths: slices.Compact(paths), diff: AssignmentDiff{Baseline: baseline, Current: baseline}}, nil
}

// AssignmentDiff returns a copy of the current candidate diff. It includes
// untracked generated files because gitsnapshot captures the synthetic tree.
func (o *WorkspaceCheckObserver) AssignmentDiff() AssignmentDiff {
	if o == nil {
		return AssignmentDiff{}
	}
	copy := o.diff
	copy.Paths = append([]string(nil), o.diff.Paths...)
	return copy
}

// BeforeCheck verifies that no external mutation appeared between controlled
// operations and captures the exact pre-command state for attribution.
func (o *WorkspaceCheckObserver) BeforeCheck(ctx context.Context, _ string, _ checkexec.Command) error {
	if o == nil {
		return errors.New("workspace check observer is required")
	}
	if o.pending {
		return errors.New("workspace check observer already has a running check")
	}
	if err := o.workspace.EnsureUnchanged(ctx, o.repository, o.diff.Current); err != nil {
		return o.block(fmt.Errorf("verify workspace before configured check: %w", err))
	}
	before, err := o.workspace.Capture(ctx, o.repository)
	if err != nil {
		return o.block(fmt.Errorf("capture before configured check: %w", err))
	}
	o.before, o.pending = before, true
	return nil
}

// WorkspaceCheckReporter composes the task-5.4 publisher with the task-5.5
// side-effect observer. It is deliberately a reporter, so existing requested
// and required check-set ordering remains the sole command dispatcher.
type WorkspaceCheckReporter struct {
	Observer  *WorkspaceCheckObserver
	Publisher CheckResultReporter
}

func (r *WorkspaceCheckReporter) BeforeCheck(ctx context.Context, name string, command checkexec.Command) error {
	if r == nil || r.Observer == nil || r.Publisher == nil {
		return errors.New("workspace check reporter requires observer and publisher")
	}
	return r.Observer.BeforeCheck(ctx, name, command)
}

// ReportCheck observes the command workspace before publishing it. This order
// is essential: output-artifact publication may fail, but it must never skip
// protected-path restoration, violation journaling, or candidate attribution.
func (r *WorkspaceCheckReporter) ReportCheck(ctx context.Context, name string, command checkexec.Command, result checkexec.Result, duration time.Duration) (CheckPresentation, error) {
	if r == nil || r.Observer == nil || r.Publisher == nil {
		return CheckPresentation{}, errors.New("workspace check reporter requires observer and publisher")
	}
	observeErr := r.Observer.AfterCheck(ctx, name)
	presentation, publishErr := r.Publisher.ReportCheck(ctx, name, command, result, duration)
	if publishErr != nil {
		// A changed candidate without a durable checked-state reference cannot
		// refresh model state. Pause rather than inventing an EvidenceRef.
		if r.Observer.candidateChanged {
			publishErr = r.Observer.block(fmt.Errorf("publish changed checked state: %w", publishErr))
		}
		return presentation, errors.Join(observeErr, publishErr)
	}
	if r.Observer.candidateChanged {
		if stateErr := r.Observer.ObserveCheckedState(presentation.CheckedState.Reference); stateErr != nil {
			return presentation, errors.Join(observeErr, stateErr)
		}
	}
	return presentation, observeErr
}

func (o *WorkspaceCheckObserver) AfterCheck(ctx context.Context, name string) error {
	if o == nil || !o.pending {
		return errors.New("configured check has no captured pre-command state")
	}
	o.pending = false
	o.candidateChanged = false
	after, err := o.workspace.Capture(ctx, o.repository)
	if err != nil {
		return o.block(fmt.Errorf("capture after configured check: %w", err))
	}
	difference, err := o.workspace.Diff(ctx, o.repository, o.before, after)
	if err != nil {
		return o.block(fmt.Errorf("compare configured check changes: %w", err))
	}
	if difference.HeadChanged || difference.HeadRefChanged || difference.IndexChanged || difference.SubmodulesChanged {
		return o.block(fmt.Errorf("configured check changed Git control state: %#v", difference))
	}
	protected := protectedCheckPaths(difference.Paths, o.protectedPaths)
	if len(protected) != 0 {
		restored, restoreErr := o.workspace.RestorePaths(ctx, o.repository, o.before, after, protected)
		result := "restored"
		if restoreErr != nil {
			result = "failed: " + restoreErr.Error()
		}
		var journalErr error
		if o.journal == nil {
			journalErr = errors.New("configured check violation has no durable journal")
		} else {
			journalErr = o.journal.AppendViolation(runstore.ViolationRecord{
				Role: "check", CallID: name, Paths: protected,
				ViolatedConstraint: "protected path may be changed only by the controller",
				RestorationResult:  result,
			})
		}
		if restoreErr != nil || journalErr != nil {
			return o.block(errors.Join(restoreErr, journalErr))
		}
		o.diff.Current = restored
		if err := o.refreshDiff(ctx); err != nil {
			return o.block(err)
		}
		remaining, err := o.workspace.Diff(ctx, o.repository, o.before, restored)
		if err != nil {
			return o.block(fmt.Errorf("compare restored configured-check changes: %w", err))
		}
		o.candidateChanged = len(remaining.Paths) != 0
		if o.candidateChanged {
			o.mutationGeneration++
		}
		return fmt.Errorf("%w: %s", ErrCheckWorkspaceViolation, strings.Join(protected, ", "))
	}

	o.diff.Current = after
	if err := o.refreshDiff(ctx); err != nil {
		return o.block(err)
	}
	o.candidateChanged = len(difference.Paths) != 0
	if o.candidateChanged {
		o.mutationGeneration++
	}
	return nil
}

// ObserveCheckedState records the durable state only after publishing it. A
// publisher failure is deliberately handled by the caller as execution
// blocked, never by creating a synthetic or missing EvidenceRef.
func (o *WorkspaceCheckObserver) ObserveCheckedState(state implementationstate.EvidenceRef) error {
	if o == nil || !o.candidateChanged || o.run == nil {
		return nil
	}
	if err := o.run.ObserveCodeState(state); err != nil {
		return o.block(fmt.Errorf("record changed checked state: %w", err))
	}
	return nil
}

func (o *WorkspaceCheckObserver) refreshDiff(ctx context.Context) error {
	difference, err := o.workspace.Diff(ctx, o.repository, o.diff.Baseline, o.diff.Current)
	if err != nil {
		return fmt.Errorf("compute assignment candidate diff: %w", err)
	}
	o.diff.Paths = append(o.diff.Paths[:0], difference.Paths...)
	return nil
}

func protectedCheckPaths(paths, protected []string) []string {
	violations := make([]string, 0)
	for _, path := range paths {
		if pathIn(protected, path, false) {
			violations = append(violations, path)
		}
	}
	slices.Sort(violations)
	return violations
}

func (o *WorkspaceCheckObserver) block(cause error) error {
	if o != nil && o.run != nil && o.run.Status == implementationstate.RunActive {
		if err := o.run.Pause(executionBlockedPauseReason); err != nil {
			return errors.Join(cause, fmt.Errorf("pause execution-blocked run: %w", err))
		}
	}
	return cause
}

// RequiredCheckCycle is one complete required-check run. A changed cycle is
// never acceptance evidence: the next cycle starts from the first required
// command and checks the new candidate state.
type RequiredCheckCycle struct {
	Number  int
	Set     CheckSet
	Changed bool
}

// RequiredCheckConvergence records the bounded sequence used to establish a
// stable required-check result.
type RequiredCheckConvergence struct {
	Cycles []RequiredCheckCycle
	Diff   AssignmentDiff
}

// RunOneRequiredCheckCycle executes exactly one complete required set and
// records whether any command mutated the candidate while it ran. Callers
// that own durable mandatory-attempt accounting use this primitive so each
// restarted set can reserve its own attempt; RunRequiredChecksUntilStable
// below composes the same primitive for callers that only need convergence.
func RunOneRequiredCheckCycle(ctx context.Context, selection implementationconfig.CheckSelection, runner CheckRunner, reporter *WorkspaceCheckReporter, number int) (RequiredCheckCycle, error) {
	if reporter == nil || reporter.Observer == nil {
		return RequiredCheckCycle{}, errors.New("required check convergence requires a workspace reporter")
	}
	if number <= 0 {
		return RequiredCheckCycle{}, errors.New("required check cycle number must be positive")
	}
	beforeMutation := reporter.Observer.mutationGeneration
	set, err := RunRequiredChecksWithReporter(ctx, selection, runner, reporter)
	return RequiredCheckCycle{
		Number:  number,
		Set:     set,
		Changed: reporter.Observer.mutationGeneration != beforeMutation,
	}, err
}

// RunRequiredChecksUntilStable runs complete required sets until a set leaves
// the workspace unchanged. maxCycles is the controller's approved required
// check-attempt bound; it is never treated as an unbounded generator retry.
func RunRequiredChecksUntilStable(ctx context.Context, selection implementationconfig.CheckSelection, runner CheckRunner, reporter *WorkspaceCheckReporter, maxCycles int) (RequiredCheckConvergence, error) {
	if reporter == nil || reporter.Observer == nil {
		return RequiredCheckConvergence{}, errors.New("required check convergence requires a workspace reporter")
	}
	if maxCycles <= 0 {
		return RequiredCheckConvergence{}, errors.New("required check convergence requires a positive attempt bound")
	}
	convergence := RequiredCheckConvergence{}
	for cycle := 1; cycle <= maxCycles; cycle++ {
		result, err := RunOneRequiredCheckCycle(ctx, selection, runner, reporter, cycle)
		convergence.Cycles = append(convergence.Cycles, result)
		convergence.Diff = reporter.Observer.AssignmentDiff()
		if err != nil {
			return convergence, err
		}
		if !result.Set.Succeeded() {
			return convergence, nil
		}
		if !result.Changed {
			return convergence, nil
		}
	}
	return convergence, fmt.Errorf("%w after %d complete required-check cycles", ErrCheckWorkspaceUnstable, maxCycles)
}
