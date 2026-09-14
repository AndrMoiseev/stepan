package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestWorkspaceCheckIncludesNewGeneratedFileInAssignmentDiff(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	observer, reporter, _ := newWorkspaceCheckReporter(t, repository, nil, nil)
	selection := testCheckSelection(nil)
	selection.Checks["test_auth"] = implementationCheck(repository, "test_auth")
	runner := CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
		if err := os.WriteFile(filepath.Join(repository, "generated.go"), []byte("package generated\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return checkexec.Result{ExitCode: 0}, nil
	})

	set, err := RunRequestedChecksWithReporter(context.Background(), selection, []string{"test_auth"}, runner, reporter)
	if err != nil || !set.Succeeded() || set.Results[0].Presentation == nil {
		t.Fatalf("generated check = %#v, error %v", set, err)
	}
	if got := observer.AssignmentDiff().Paths; !reflect.DeepEqual(got, []string{"generated.go"}) {
		t.Fatalf("assignment diff paths = %#v, want generated file", got)
	}
}

func TestWorkspaceCheckChangingAcceptedCodeInvalidatesAcceptance(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	accepted := implementationstate.EvidenceRef{ID: "accepted", Digest: "accepted-digest"}
	run := &implementationstate.Run{
		Status:       implementationstate.RunActive,
		CurrentState: accepted,
		Assignments: []implementationstate.Assignment{{
			ID: "assignment", Status: implementationstate.AssignmentAcceptedAwaitingCommit,
			TaskIDs: []implementationstate.TaskID{"task"}, Acceptance: &implementationstate.AcceptanceEvidence{State: accepted},
		}},
		LeafStatus: map[implementationstate.TaskID]implementationstate.TaskStatus{"task": implementationstate.TaskAcceptedAwaitingCommit},
	}
	_, reporter, _ := newWorkspaceCheckReporter(t, repository, run, nil)
	selection := testCheckSelection(nil)
	selection.Checks["test_auth"] = implementationCheck(repository, "test_auth")
	runner := CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
		if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("check rewrote approved code\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return checkexec.Result{ExitCode: 0}, nil
	})

	set, err := RunRequestedChecksWithReporter(context.Background(), selection, []string{"test_auth"}, runner, reporter)
	if err != nil || !set.Succeeded() {
		t.Fatalf("changed accepted check = %#v, error %v", set, err)
	}
	assignment := run.Assignments[0]
	if assignment.Status != implementationstate.AssignmentActive || assignment.Acceptance != nil || len(assignment.AcceptanceHistory) != 1 || run.LeafStatus["task"] != implementationstate.TaskPending {
		t.Fatalf("accepted assignment remained current after check mutation: %#v", run)
	}
	if run.CurrentState == accepted {
		t.Fatal("changed check did not replace current checked state")
	}
}

func TestWorkspaceCheckRestoresProtectedMutationAndRejectsResult(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	protected := filepath.Join(repository, ".stepan", "settings.json")
	if err := os.MkdirAll(filepath.Dir(protected), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protected, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("check-violation")
	if err != nil {
		t.Fatal(err)
	}
	_, reporter, _ := newWorkspaceCheckReporter(t, repository, nil, journal, ".stepan/settings.json")
	selection := testCheckSelection(nil)
	selection.Checks["test_auth"] = implementationCheck(repository, "test_auth")
	runner := CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
		if err := os.WriteFile(protected, []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return checkexec.Result{ExitCode: 0}, nil
	})

	set, err := RunRequestedChecksWithReporter(context.Background(), selection, []string{"test_auth"}, runner, reporter)
	if !errors.Is(err, ErrCheckWorkspaceViolation) || set.Results[0].Status != CheckFailed || set.Results[0].Presentation == nil {
		t.Fatalf("protected mutation = %#v, error %v", set, err)
	}
	contents, readErr := os.ReadFile(protected)
	if readErr != nil || string(contents) != "before\n" {
		t.Fatalf("protected file after restoration = %q, error %v", contents, readErr)
	}
	records := readViolationRecords(t, journal)
	if len(records) != 1 || records[0].Role != "check" || !reflect.DeepEqual(records[0].Paths, []string{".stepan/settings.json"}) || records[0].RestorationResult != "restored" {
		t.Fatalf("check violation journal = %#v", records)
	}
}

func TestWorkspaceCheckRestoresAndAccountsForWritesWhenPublisherFails(t *testing.T) {
	for _, test := range []struct {
		name         string
		writeAllowed bool
	}{
		{name: "protected only"},
		{name: "protected and generated", writeAllowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newFilesystemWorkspace(t)
			protected := filepath.Join(repository, ".stepan", "settings.json")
			if err := os.MkdirAll(filepath.Dir(protected), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(protected, []byte("before\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := runstore.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			journal, err := store.Create("failed-publisher-violation")
			if err != nil {
				t.Fatal(err)
			}
			accepted := implementationstate.EvidenceRef{ID: "accepted", Digest: "accepted-digest"}
			model := acceptedTestRun(accepted)
			observer, err := NewWorkspaceCheckObserverWithControl(context.Background(), newFilesystemWorkspaceControl(), repository, model, journal, []string{".stepan/settings.json"})
			if err != nil {
				t.Fatal(err)
			}
			publisherErr := errors.New("artifact publication failed")
			reporter := &WorkspaceCheckReporter{Observer: observer, Publisher: failingCheckPublisher{err: publisherErr}}
			selection := testCheckSelection(nil)
			selection.Checks["test_auth"] = implementationCheck(repository, "test_auth")
			runner := CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
				if err := os.WriteFile(protected, []byte("changed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if test.writeAllowed {
					if err := os.WriteFile(filepath.Join(repository, "generated.go"), []byte("generated\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return checkexec.Result{ExitCode: 0}, nil
			})

			set, err := RunRequestedChecksWithReporter(context.Background(), selection, []string{"test_auth"}, runner, reporter)
			if !errors.Is(err, publisherErr) || !errors.Is(err, ErrCheckWorkspaceViolation) || set.Results[0].Status != CheckFailed {
				t.Fatalf("failed publication result = %#v, error %v", set, err)
			}
			contents, readErr := os.ReadFile(protected)
			if readErr != nil || string(contents) != "before\n" {
				t.Fatalf("protected file after failed publication = %q, error %v", contents, readErr)
			}
			records := readViolationRecords(t, journal)
			if len(records) != 1 || records[0].RestorationResult != "restored" {
				t.Fatalf("failed publication violation journal = %#v", records)
			}
			if test.writeAllowed {
				if got := observer.AssignmentDiff().Paths; !reflect.DeepEqual(got, []string{"generated.go"}) {
					t.Fatalf("surviving generated diff = %#v", got)
				}
				if model.Status != implementationstate.RunPaused || model.CurrentState != accepted {
					t.Fatalf("changed candidate without durable state was not fail-closed: %#v", model)
				}
			} else if got := observer.AssignmentDiff().Paths; len(got) != 0 {
				t.Fatalf("protected-only diff = %#v", got)
			}
		})
	}
}

func TestRequiredChecksConvergeAfterStableGeneration(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	observer, reporter, _ := newWorkspaceCheckReporter(t, repository, nil, nil)
	selection := testCheckSelection([]string{"test_auth"})
	selection.Checks["test_auth"] = implementationCheck(repository, "test_auth")
	calls := 0
	runner := CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
		calls++
		if calls == 1 {
			if err := os.WriteFile(filepath.Join(repository, "generated.go"), []byte("stable\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return checkexec.Result{ExitCode: 0}, nil
	})

	convergence, err := RunRequiredChecksUntilStable(context.Background(), selection, runner, reporter, 3)
	if err != nil || calls != 2 || len(convergence.Cycles) != 2 || !convergence.Cycles[0].Changed || convergence.Cycles[1].Changed {
		t.Fatalf("stable generation convergence = %#v, calls=%d, error=%v", convergence, calls, err)
	}
	if got := observer.AssignmentDiff().Paths; !reflect.DeepEqual(got, []string{"generated.go"}) {
		t.Fatalf("stable generated candidate paths = %#v", got)
	}
}

func TestRequiredChecksStopAfterBoundedUnstableGeneration(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	_, reporter, _ := newWorkspaceCheckReporter(t, repository, nil, nil)
	selection := testCheckSelection([]string{"test_auth"})
	selection.Checks["test_auth"] = implementationCheck(repository, "test_auth")
	calls := 0
	runner := CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
		calls++
		contents := []byte("generated A\n")
		if calls%2 == 0 {
			contents = []byte("generated B\n")
		}
		if err := os.WriteFile(filepath.Join(repository, "generated.go"), contents, 0o600); err != nil {
			t.Fatal(err)
		}
		return checkexec.Result{ExitCode: 0}, nil
	})

	convergence, err := RunRequiredChecksUntilStable(context.Background(), selection, runner, reporter, 3)
	if !errors.Is(err, ErrCheckWorkspaceUnstable) || calls != 3 || len(convergence.Cycles) != 3 {
		t.Fatalf("unstable generation = %#v, calls=%d, error=%v", convergence, calls, err)
	}
	for _, cycle := range convergence.Cycles {
		if !cycle.Changed || !cycle.Set.Succeeded() {
			t.Fatalf("unstable cycle = %#v", cycle)
		}
	}
}

func TestRequiredChecksRestartWhenCommandsChangeThenRevertCandidate(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	_, reporter, _ := newWorkspaceCheckReporter(t, repository, nil, nil)
	selection := testCheckSelection([]string{"test_auth", "lint"})
	selection.Checks["test_auth"] = implementationCheck(repository, "test_auth")
	selection.Checks["lint"] = implementationCheck(repository, "lint")
	var calls []string
	runner := CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		calls = append(calls, command.Program)
		contents := []byte("changed by first check\n")
		if command.Program == "lint" {
			contents = []byte("initial\n")
		}
		if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), contents, 0o600); err != nil {
			t.Fatal(err)
		}
		return checkexec.Result{ExitCode: 0}, nil
	})

	convergence, err := RunRequiredChecksUntilStable(context.Background(), selection, runner, reporter, 3)
	if !errors.Is(err, ErrCheckWorkspaceUnstable) || len(convergence.Cycles) != 3 {
		t.Fatalf("change-then-revert convergence = %#v, error %v", convergence, err)
	}
	if want := []string{"test_auth", "lint", "test_auth", "lint", "test_auth", "lint"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("required checks did not restart from first command: %#v, want %#v", calls, want)
	}
	for _, cycle := range convergence.Cycles {
		if !cycle.Changed || !cycle.Set.Succeeded() {
			t.Fatalf("net-zero mutation was accepted as stable: %#v", cycle)
		}
	}
}

type failingCheckPublisher struct{ err error }

func (p failingCheckPublisher) ReportCheck(context.Context, string, checkexec.Command, checkexec.Result, time.Duration) (CheckPresentation, error) {
	return CheckPresentation{}, p.err
}

func acceptedTestRun(accepted implementationstate.EvidenceRef) *implementationstate.Run {
	return &implementationstate.Run{
		Status:       implementationstate.RunActive,
		CurrentState: accepted,
		Assignments: []implementationstate.Assignment{{
			ID: "assignment", Status: implementationstate.AssignmentAcceptedAwaitingCommit,
			TaskIDs: []implementationstate.TaskID{"task"}, Acceptance: &implementationstate.AcceptanceEvidence{State: accepted},
		}},
		LeafStatus: map[implementationstate.TaskID]implementationstate.TaskStatus{"task": implementationstate.TaskAcceptedAwaitingCommit},
	}
}

func newWorkspaceCheckReporter(t *testing.T, repository string, model *implementationstate.Run, journal *runstore.Run, protected ...string) (*WorkspaceCheckObserver, *WorkspaceCheckReporter, *runstore.Run) {
	t.Helper()
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("check-workspace")
	if err != nil {
		t.Fatal(err)
	}
	workspace := newFilesystemWorkspaceControl()
	publisher, err := NewCheckResultPublisherWithControl(run, workspace, repository, "check-workspace")
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewWorkspaceCheckObserverWithControl(context.Background(), workspace, repository, model, journal, protected)
	if err != nil {
		t.Fatal(err)
	}
	return observer, &WorkspaceCheckReporter{Observer: observer, Publisher: publisher}, run
}
