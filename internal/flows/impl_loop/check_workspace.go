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
	run            *implementationstate.Run
	journal        *runstore.Run
	protectedPaths []string
	diff           AssignmentDiff
	before         gitsnapshot.Snapshot
	pending        bool
	// restoredCandidateChanged distinguishes a protected-only violation from
	// one command that also produced permitted generated/code output. The
	// latter must still invalidate prior acceptance after restoration.
	restoredCandidateChanged bool
}

// NewWorkspaceCheckObserver captures the assignment's initial candidate
// state. The caller must retain one observer for the complete assignment.
func NewWorkspaceCheckObserver(ctx context.Context, repository string, run *implementationstate.Run, journal *runstore.Run, protectedPaths []string) (*WorkspaceCheckObserver, error) {
	if strings.TrimSpace(repository) == "" {
		return nil, errors.New("workspace check observer requires a repository")
	}
	baseline, err := gitsnapshot.Capture(ctx, repository)
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
	return &WorkspaceCheckObserver{repository: repository, run: run, journal: journal, protectedPaths: slices.Compact(paths), diff: AssignmentDiff{Baseline: baseline, Current: baseline}}, nil
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
	if err := gitsnapshot.EnsureUnchanged(ctx, o.repository, o.diff.Current); err != nil {
		return o.block(fmt.Errorf("verify workspace before configured check: %w", err))
	}
	before, err := gitsnapshot.Capture(ctx, o.repository)
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

// ReportCheck publishes output and checked state, then records the command's
// workspace delta. A protected write is restored after its diagnostic state
// is published but is returned as a failed check and cannot be accepted.
func (r *WorkspaceCheckReporter) ReportCheck(ctx context.Context, name string, command checkexec.Command, result checkexec.Result, duration time.Duration) (CheckPresentation, error) {
	if r == nil || r.Observer == nil || r.Publisher == nil {
		return CheckPresentation{}, errors.New("workspace check reporter requires observer and publisher")
	}
	presentation, err := r.Publisher.ReportCheck(ctx, name, command, result, duration)
	if err != nil {
		r.Observer.pending = false
		return presentation, err
	}
	if err := r.Observer.AfterCheck(ctx, name, presentation); err != nil {
		if errors.Is(err, ErrCheckWorkspaceViolation) {
			// The first publication describes the violating state. Publish a
			// second state only after targeted restoration so any later model
			// transition can reference the actual surviving candidate, never
			// the protected mutation that was rejected.
			restoredPresentation, publishErr := r.Publisher.ReportCheck(ctx, name, command, result, duration)
			if publishErr != nil {
				return presentation, errors.Join(err, fmt.Errorf("publish restored checked state: %w", publishErr))
			}
			if stateErr := r.Observer.ObserveRestoredCheckedState(restoredPresentation.CheckedState.Reference); stateErr != nil {
				return restoredPresentation, errors.Join(err, stateErr)
			}
			return restoredPresentation, err
		}
		return presentation, err
	}
	return presentation, nil
}

func (o *WorkspaceCheckObserver) AfterCheck(ctx context.Context, name string, presentation CheckPresentation) error {
	if o == nil || !o.pending {
		return errors.New("configured check has no captured pre-command state")
	}
	o.pending = false
	after, err := gitsnapshot.Capture(ctx, o.repository)
	if err != nil {
		return o.block(fmt.Errorf("capture after configured check: %w", err))
	}
	difference, err := gitsnapshot.Diff(ctx, o.repository, o.before, after)
	if err != nil {
		return o.block(fmt.Errorf("compare configured check changes: %w", err))
	}
	if difference.HeadChanged || difference.HeadRefChanged || difference.IndexChanged || difference.SubmodulesChanged {
		return o.block(fmt.Errorf("configured check changed Git control state: %#v", difference))
	}
	protected := protectedCheckPaths(difference.Paths, o.protectedPaths)
	if len(protected) != 0 {
		o.restoredCandidateChanged = false
		restored, restoreErr := gitsnapshot.RestorePaths(ctx, o.repository, o.before, after, protected)
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
		remaining, err := gitsnapshot.Diff(ctx, o.repository, o.before, restored)
		if err != nil {
			return o.block(fmt.Errorf("compare restored configured-check changes: %w", err))
		}
		o.restoredCandidateChanged = len(remaining.Paths) != 0
		return fmt.Errorf("%w: %s", ErrCheckWorkspaceViolation, strings.Join(protected, ", "))
	}

	o.diff.Current = after
	if err := o.refreshDiff(ctx); err != nil {
		return o.block(err)
	}
	if len(difference.Paths) != 0 && o.run != nil {
		if err := o.run.ObserveCodeState(presentation.CheckedState.Reference); err != nil {
			return o.block(fmt.Errorf("record changed checked state: %w", err))
		}
	}
	return nil
}

// ObserveRestoredCheckedState updates freshness after a prohibited command
// was repaired but retained other permitted output. It deliberately does not
// make a protected-only failed check stale by itself.
func (o *WorkspaceCheckObserver) ObserveRestoredCheckedState(state implementationstate.EvidenceRef) error {
	if o == nil || !o.restoredCandidateChanged || o.run == nil {
		return nil
	}
	o.restoredCandidateChanged = false
	if err := o.run.ObserveCodeState(state); err != nil {
		return o.block(fmt.Errorf("record restored changed checked state: %w", err))
	}
	return nil
}

func (o *WorkspaceCheckObserver) refreshDiff(ctx context.Context) error {
	difference, err := gitsnapshot.Diff(ctx, o.repository, o.diff.Baseline, o.diff.Current)
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
		before := reporter.Observer.diff.Current
		set, err := RunRequiredChecksWithReporter(ctx, selection, runner, reporter)
		after := reporter.Observer.diff.Current
		difference, diffErr := gitsnapshot.Diff(ctx, reporter.Observer.repository, before, after)
		changed := diffErr == nil && len(difference.Paths) != 0
		convergence.Cycles = append(convergence.Cycles, RequiredCheckCycle{Number: cycle, Set: set, Changed: changed})
		convergence.Diff = reporter.Observer.AssignmentDiff()
		if err != nil {
			return convergence, err
		}
		if diffErr != nil {
			return convergence, reporter.Observer.block(fmt.Errorf("compare required-check cycle: %w", diffErr))
		}
		if !set.Succeeded() {
			return convergence, nil
		}
		if !changed {
			return convergence, nil
		}
	}
	return convergence, fmt.Errorf("%w after %d complete required-check cycles", ErrCheckWorkspaceUnstable, maxCycles)
}
