package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestDiscoverStartupRunFindsPausedWorkCopyWithoutOpeningProjection(t *testing.T) {
	run, state, journal, workCopy := newInitialCheckRun(t)
	if err := run.AddRunOperation(implementationstate.Operation{
		ID: "initial-check", Kind: implementationstate.OperationCheck,
		Basis:       implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration},
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
	for _, status := range []implementationstate.RunStatus{implementationstate.RunClosed, implementationstate.RunSucceeded} {
		if isUnclosedStatus(status) {
			t.Fatalf("terminal status %q was selected as unclosed", status)
		}
	}
}

func TestStartupSummaryUsesLatestDurableTransitionForStageAndAction(t *testing.T) {
	basis := implementationstate.AcceptanceBasis{
		Specification: implementationstate.EvidenceRef{ID: "spec", Digest: "spec"},
		Configuration: implementationstate.EvidenceRef{ID: "config", Digest: "config"},
	}
	assignmentOperation := implementationstate.Operation{ID: "implement", Kind: implementationstate.OperationAgent, Basis: basis, Description: "implement assignment"}
	resumeOperation := implementationstate.Operation{ID: "resume-check", Kind: implementationstate.OperationCheck, Basis: basis, Description: "go test ./...", UncountedResumeCheck: true}
	base := func() *implementationstate.Run {
		return &implementationstate.Run{
			Status:                implementationstate.RunActive,
			InitialBaselineStatus: implementationstate.InitialBaselinePassed,
			Tasks:                 []implementationstate.Task{{ID: "task", Title: "task"}},
			LeafStatus:            map[implementationstate.TaskID]implementationstate.TaskStatus{"task": implementationstate.TaskPending},
		}
	}
	for _, test := range []struct {
		name, wantStage, wantAction string
		previous, current           *implementationstate.Run
	}{
		{
			name: "between assignments selects next", wantStage: "selecting next assignment", wantAction: "created implementation run",
			previous: base(), current: base(),
		},
		{
			name: "active assignment", wantStage: "implementing assignment assignment-1", wantAction: "implement assignment",
			previous: func() *implementationstate.Run {
				r := base()
				r.Assignments = []implementationstate.Assignment{{ID: "assignment-1", Status: implementationstate.AssignmentActive}}
				return r
			}(),
			current: func() *implementationstate.Run {
				r := base()
				r.Assignments = []implementationstate.Assignment{{ID: "assignment-1", Status: implementationstate.AssignmentActive, Operations: []implementationstate.Operation{assignmentOperation}}}
				return r
			}(),
		},
		{
			name: "resume check is newer than assignment operation", wantStage: "resume required checks", wantAction: "resume required check: go test ./...",
			previous: func() *implementationstate.Run {
				r := base()
				r.Assignments = []implementationstate.Assignment{{ID: "assignment-1", Status: implementationstate.AssignmentActive, Operations: []implementationstate.Operation{assignmentOperation}}}
				return r
			}(),
			current: func() *implementationstate.Run {
				r := base()
				r.Assignments = []implementationstate.Assignment{{ID: "assignment-1", Status: implementationstate.AssignmentActive, Operations: []implementationstate.Operation{assignmentOperation}}}
				r.RunOperations = []implementationstate.Operation{resumeOperation}
				return r
			}(),
		},
		{
			name: "accepted assignment awaits commit", wantStage: "committing accepted assignment assignment-1", wantAction: "accept assignment assignment-1",
			previous: func() *implementationstate.Run {
				r := base()
				r.Assignments = []implementationstate.Assignment{{ID: "assignment-1", Status: implementationstate.AssignmentActive}}
				return r
			}(),
			current: func() *implementationstate.Run {
				r := base()
				r.Assignments = []implementationstate.Assignment{{ID: "assignment-1", Status: implementationstate.AssignmentAcceptedAwaitingCommit}}
				return r
			}(),
		},
		{
			name: "failed resume check", wantStage: "resume required checks", wantAction: "resume required check: go test ./... (failed)",
			previous: base(), current: func() *implementationstate.Run {
				r := base()
				r.Status = implementationstate.RunPaused
				r.PauseReason = "execution_blocked: resume checks"
				r.ExecutionBlock = &implementationstate.ExecutionBlock{BlockedAction: "resume checks", Diagnostic: "tests failed", RequiredUserAction: "fix tests"}
				r.RunOperations = []implementationstate.Operation{resumeOperation}
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
	if err != nil || current.Status != implementationstate.RunActive {
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
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	if err := run.AddRunOperation(implementationstate.Operation{ID: "baseline", Kind: implementationstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(implementationstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implementationstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("assignment", []implementationstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief, err := journal.Publish("brief", []byte("assignment brief"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", implementationstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddOperation("assignment", implementationstate.Operation{ID: "implement", Kind: implementationstate.OperationAgent, Basis: basis, BriefID: "brief", Description: "implement assignment"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartAssignmentAttempt("assignment", "implement"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := run.AddResult("assignment", implementationstate.OperationResult{ID: "implement-result", OperationID: "implement", Status: implementationstate.ResultFailed, State: run.CurrentState, Basis: basis}); err != nil {
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
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	if err := run.AddRunOperation(implementationstate.Operation{ID: "baseline", Kind: implementationstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(implementationstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implementationstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("assignment", []implementationstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief, err := journal.Publish("brief", []byte("assignment brief"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", implementationstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddOperation("assignment", implementationstate.Operation{ID: "implement", Kind: implementationstate.OperationAgent, Basis: basis, BriefID: "brief", Description: "implement assignment"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartAssignmentAttempt("assignment", "implement"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := run.AddResult("assignment", implementationstate.OperationResult{ID: "implement-result", OperationID: "implement", Status: implementationstate.ResultFailed, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.PauseExecutionBlocked(implementationstate.ExecutionBlock{BlockedAction: "implement assignment", Diagnostic: "tool unavailable", Attempts: []string{"started executor"}, RequiredUserAction: "install tool"}); err != nil {
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
