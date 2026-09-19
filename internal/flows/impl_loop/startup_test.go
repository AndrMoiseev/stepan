package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func TestDiscoverStartupRunFindsPausedWorkCopyWithoutOpeningProjection(t *testing.T) {
	run, state, journal, workCopy := newInitialCheckRun(t)
	if err := run.AddRunOperation(implstate.Operation{
		ID: "initial-check", Kind: implstate.OperationCheck,
		Basis:       implstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration},
		Description: "run required check lint",
	}); err != nil {
		t.Fatal(err)
	}
	if err := run.Pause("lint failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	projection := filepath.Join(journal.Path(), runstore.StateDatabaseFileName)
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.OpenExisting(filepath.Dir(filepath.Dir(journal.Path())))
	if err != nil {
		t.Fatal(err)
	}

	found, err := DiscoverStartupRun(context.Background(), store, workCopy)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Run.Identity.ID != run.Identity.ID {
		t.Fatalf("discovered run = %#v, want %q", found, run.Identity.ID)
	}
	if found.Summary.Change != "change" || found.Summary.Stage != "initial required checks" || found.Summary.LastAction != "run required check lint" || found.Summary.StopReason != "lint failed" || found.Summary.Lifecycle != LifecyclePaused {
		t.Fatalf("startup summary = %#v", found.Summary)
	}
	panel := FormatStartupSummary(found.Summary)
	for _, want := range []string{"change: change", "stage: initial required checks", "last action: run required check lint", "stop reason: lint failed", "available: /resume /stop /status"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("startup panel %q does not contain %q", panel, want)
		}
	}
	if _, err := os.Stat(projection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only startup discovery rebuilt projection: %v", err)
	}
}

func TestDiscoverStartupRunSkipsClosedRunAndTerminalStatuses(t *testing.T) {
	run, state, journal, workCopy := newInitialCheckRun(t)
	if err := run.Close("user stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.OpenExisting(filepath.Dir(filepath.Dir(journal.Path())))
	if err != nil {
		t.Fatal(err)
	}
	found, err := DiscoverStartupRun(context.Background(), store, workCopy)
	if err != nil || found != nil {
		t.Fatalf("closed discovery = %#v, %v", found, err)
	}
	for _, status := range []implstate.RunStatus{implstate.RunClosed, implstate.RunSucceeded} {
		if isUnclosedStatus(status) {
			t.Fatalf("terminal status %q was selected as unclosed", status)
		}
	}
}

func TestStartupSummaryUsesLatestDurableTransitionForStageAndAction(t *testing.T) {
	basis := implstate.AcceptanceBasis{
		Specification: implstate.EvidenceRef{ID: "spec", Digest: "spec"},
		Configuration: implstate.EvidenceRef{ID: "config", Digest: "config"},
	}
	assignmentOperation := implstate.Operation{ID: "implement", Kind: implstate.OperationAgent, Basis: basis, Description: "implement assignment"}
	resumeOperation := implstate.Operation{ID: "resume-check", Kind: implstate.OperationCheck, Basis: basis, Description: "go test ./...", UncountedResumeCheck: true}
	base := func() *implstate.Run {
		return &implstate.Run{
			Status:                implstate.RunActive,
			InitialBaselineStatus: implstate.InitialBaselinePassed,
			Tasks:                 []implstate.Task{{ID: "task", Title: "task"}},
			LeafStatus:            map[implstate.TaskID]implstate.TaskStatus{"task": implstate.TaskPending},
		}
	}
	for _, test := range []struct {
		name, wantStage, wantAction string
		previous, current           *implstate.Run
	}{
		{
			name: "between assignments selects next", wantStage: "selecting next assignment", wantAction: "created implementation run",
			previous: base(), current: base(),
		},
		{
			name: "active assignment", wantStage: "implementing assignment assignment-1", wantAction: "implement assignment",
			previous: func() *implstate.Run {
				r := base()
				r.Assignments = []implstate.Assignment{{ID: "assignment-1", Status: implstate.AssignmentActive}}
				return r
			}(),
			current: func() *implstate.Run {
				r := base()
				r.Assignments = []implstate.Assignment{{ID: "assignment-1", Status: implstate.AssignmentActive, Operations: []implstate.Operation{assignmentOperation}}}
				return r
			}(),
		},
		{
			name: "resume check is newer than assignment operation", wantStage: "resume required checks", wantAction: "resume required check: go test ./...",
			previous: func() *implstate.Run {
				r := base()
				r.Assignments = []implstate.Assignment{{ID: "assignment-1", Status: implstate.AssignmentActive, Operations: []implstate.Operation{assignmentOperation}}}
				return r
			}(),
			current: func() *implstate.Run {
				r := base()
				r.Assignments = []implstate.Assignment{{ID: "assignment-1", Status: implstate.AssignmentActive, Operations: []implstate.Operation{assignmentOperation}}}
				r.RunOperations = []implstate.Operation{resumeOperation}
				return r
			}(),
		},
		{
			name: "accepted assignment awaits commit", wantStage: "committing accepted assignment assignment-1", wantAction: "accept assignment assignment-1",
			previous: func() *implstate.Run {
				r := base()
				r.Assignments = []implstate.Assignment{{ID: "assignment-1", Status: implstate.AssignmentActive}}
				return r
			}(),
			current: func() *implstate.Run {
				r := base()
				r.Assignments = []implstate.Assignment{{ID: "assignment-1", Status: implstate.AssignmentAcceptedAwaitingCommit}}
				return r
			}(),
		},
		{
			name: "failed resume check", wantStage: "resume required checks", wantAction: "resume required check: go test ./... (failed)",
			previous: base(), current: func() *implstate.Run {
				r := base()
				r.Status = implstate.RunPaused
				r.PauseReason = "execution_blocked: resume checks"
				r.ExecutionBlock = &implstate.ExecutionBlock{BlockedAction: "resume checks", Diagnostic: "tests failed", RequiredUserAction: "fix tests"}
				r.RunOperations = []implstate.Operation{resumeOperation}
				return r
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			summary := SummarizeStartupRun(test.current)
			summary.Stage = journalStage(test.current, test.previous, summary.Stage)
			summary.LastAction = journalLastAction(test.previous, test.current, summary.LastAction)
			if summary.Stage != test.wantStage || summary.LastAction != test.wantAction {
				t.Fatalf("summary = %#v, want stage %q and action %q", summary, test.wantStage, test.wantAction)
			}
			if test.name == "failed resume check" && summary.StopReason != "tests failed" {
				t.Fatalf("failed resume reason = %q", summary.StopReason)
			}
		})
	}
}

func TestDiscoverStartupRunMarksOrphanedActiveRunRecoverableWithoutMutation(t *testing.T) {
	run, state, journal, workCopy := newInitialCheckRun(t)
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.OpenExisting(filepath.Dir(filepath.Dir(journal.Path())))
	if err != nil {
		t.Fatal(err)
	}
	found, err := DiscoverStartupRun(context.Background(), store, workCopy)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || !found.RecoveryRequired || found.Summary.Lifecycle != LifecyclePaused || !strings.Contains(found.Summary.StopReason, "interrupted") {
		t.Fatalf("orphaned startup = %#v", found)
	}
	current, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil || current.Status != implstate.RunActive {
		t.Fatalf("discovery mutated active run = %#v, %v", current, err)
	}
}

func TestStartupSummaryUsesDurableResumeResultTransitions(t *testing.T) {
	for _, test := range []struct {
		name       string
		runner     CheckRunner
		wantAction string
	}{
		{
			name: "successful resume result", runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
				return checkexec.Result{}, nil
			}), wantAction: "resume required check: resume required checks",
		},
		{
			name: "failed result then pause", runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
				return checkexec.Result{ExitCode: 1, Stderr: []byte("required check failed")}, errors.New("required check failed")
			}), wantAction: "resume required check: resume required checks (failed)",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResumeFixture(t, "")
			input := fixture.input()
			input.Runner = test.runner
			if _, err := Resume(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			events, err := runstore.JournalStates(fixture.journal)
			if err != nil {
				t.Fatal(err)
			}
			summary := summarizeStartupJournal(fixture.run, events)
			if summary.Stage != "resume required checks" || summary.LastAction != test.wantAction {
				t.Fatalf("resume journal summary = %#v, want stage resume required checks and action %q", summary, test.wantAction)
			}
		})
	}
}

func TestStartupSummaryUsesAssignmentResultAndFollowingPause(t *testing.T) {
	run, state, journal, _ := newInitialCheckRun(t)
	defer state.Close()
	basis := implstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	if err := run.AddRunOperation(implstate.Operation{ID: "baseline", Kind: implstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(implstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("assignment", []implstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief, err := journal.Publish("brief", []byte("assignment brief"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", implstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddOperation("assignment", implstate.Operation{ID: "implement", Kind: implstate.OperationAgent, Basis: basis, BriefID: "brief", Description: "implement assignment"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartAssignmentAttempt("assignment", "implement"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := run.AddResult("assignment", implstate.OperationResult{ID: "implement-result", OperationID: "implement", Status: implstate.ResultFailed, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	events, err := runstore.JournalStates(journal)
	if err != nil {
		t.Fatal(err)
	}
	if action, resume := latestJournalAction(events); action != "implement assignment" || resume {
		t.Fatalf("assignment result action = %q, resume=%v", action, resume)
	}
	if err := run.Pause("implementer failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	events, err = runstore.JournalStates(journal)
	if err != nil {
		t.Fatal(err)
	}
	if action, resume := latestJournalAction(events); action != "implement assignment (failed)" || resume {
		t.Fatalf("assignment result then pause action = %q, resume=%v", action, resume)
	}
}

func TestStartupSummaryUsesAtomicAssignmentResultAndExecutionBlockedPause(t *testing.T) {
	run, state, journal, _ := newInitialCheckRun(t)
	defer state.Close()
	basis := implstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	if err := run.AddRunOperation(implstate.Operation{ID: "baseline", Kind: implstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(implstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("assignment", []implstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief, err := journal.Publish("brief", []byte("assignment brief"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", implstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddOperation("assignment", implstate.Operation{ID: "implement", Kind: implstate.OperationAgent, Basis: basis, BriefID: "brief", Description: "implement assignment"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartAssignmentAttempt("assignment", "implement"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := run.AddResult("assignment", implstate.OperationResult{ID: "implement-result", OperationID: "implement", Status: implstate.ResultFailed, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.PauseExecutionBlocked(implstate.ExecutionBlock{BlockedAction: "implement assignment", Diagnostic: "tool unavailable", Attempts: []string{"started executor"}, RequiredUserAction: "install tool"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	events, err := runstore.JournalStates(journal)
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeStartupJournal(run, events)
	if summary.LastAction != "implement assignment (failed)" || summary.StopReason != "tool unavailable" {
		t.Fatalf("atomic result+pause summary = %#v", summary)
	}
}
