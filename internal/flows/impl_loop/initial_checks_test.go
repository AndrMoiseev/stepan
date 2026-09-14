package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestInitialRequiredChecksFailFastPauseAndPersistBaselineDiagnostics(t *testing.T) {
	run, state, journal, repository := newInitialCheckRun(t)
	defer state.Close()
	runner := &recordingCheckRunner{fail: map[string]error{"lint": errors.New("lint baseline failed")}}

	result, err := RunInitialRequiredChecks(context.Background(), InitialRequiredChecks{
		Run: run, StateStore: state, Journal: journal, Repository: repository,
		Selection: testCheckSelection([]string{"lint", "test_all", "build"}), Runner: runner,
		MaxCycles: 3, Operation: "baseline-checks", Result: "baseline-result",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, runner, "lint")
	assertResultStatuses(t, result.Set, CheckFailed, CheckNotRun, CheckNotRun)
	if run.Status != implementationstate.RunPaused || run.PauseReason != initialRequiredChecksPauseReason {
		t.Fatalf("baseline failure did not pause the run: %#v", run)
	}
	if len(run.Assignments) != 0 {
		t.Fatalf("baseline failure created an assignment: %#v", run.Assignments)
	}
	if len(run.RunOperations) != 1 || run.RunOperations[0].Counter != implementationstate.CycleCounterNone || len(run.RunOperations[0].Attempts) != 1 || run.RunOperations[0].Attempts[0].Outcome != implementationstate.AttemptFailed {
		t.Fatalf("baseline operation did not preserve run-level attempt semantics: %#v", run.RunOperations)
	}
	if len(run.RunResults) != 1 || run.RunResults[0].Status != implementationstate.ResultFailed || len(run.RunResults[0].Evidence) != 4 {
		t.Fatalf("baseline result lacks durable evidence: %#v", run.RunResults)
	}
	if !strings.Contains(result.Diagnostic, "lint baseline failed") {
		t.Fatalf("user diagnostic = %q", result.Diagnostic)
	}
	data, err := journal.Read(result.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"name":"lint"`) || !strings.Contains(string(data), `"status":"not_run"`) || !strings.Contains(string(data), "lint baseline failed") {
		t.Fatalf("durable baseline diagnostics = %s", data)
	}
	current, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != implementationstate.RunPaused || len(current.RunResults) != 1 || current.RunResults[0].State != run.CurrentState {
		t.Fatalf("durable paused baseline state = %#v", current)
	}
}

func TestInitialRequiredChecksRestartsCompleteSetAfterAllowedMutation(t *testing.T) {
	run, state, journal, repository := newInitialCheckRun(t)
	defer state.Close()
	selection := testCheckSelection([]string{"lint", "test_all"})
	selection.Checks["lint"] = implementationCheck(repository, "lint")
	selection.Checks["test_all"] = implementationCheck(repository, "test_all")
	var calls []string
	runner := CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		calls = append(calls, command.Program)
		if len(calls) == 1 {
			if err := os.WriteFile(filepath.Join(repository, "generated.go"), []byte("generated\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return checkexec.Result{}, nil
	})

	result, err := RunInitialRequiredChecks(context.Background(), InitialRequiredChecks{
		Run: run, StateStore: state, Journal: journal, Repository: repository, Selection: selection, Runner: runner,
		MaxCycles: 3, Operation: "baseline-checks", Result: "baseline-result",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"lint", "test_all", "lint", "test_all"}; !slices.Equal(got, want) {
		t.Fatalf("required check convergence order = %#v, want %#v", got, want)
	}
	if len(result.Convergence.Cycles) != 2 || !result.Convergence.Cycles[0].Changed || result.Convergence.Cycles[1].Changed || run.InitialBaseline == nil {
		t.Fatalf("baseline convergence = %#v, run = %#v", result.Convergence, run)
	}
}

func TestInitialRequiredChecksPersistsInterruptedResultAfterCallerCancellation(t *testing.T) {
	run, state, journal, repository := newInitialCheckRun(t)
	defer state.Close()
	selection := testCheckSelection([]string{"lint"})
	selection.Checks["lint"] = implementationCheck(repository, "lint")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := RunInitialRequiredChecks(ctx, InitialRequiredChecks{
		Run: run, StateStore: state, Journal: journal, Repository: repository, Selection: selection,
		Runner: CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
			cancel()
			return checkexec.Result{ExitCode: -1, Failure: checkexec.FailureCanceled, Stderr: []byte("interrupted baseline")}, context.Canceled
		}), MaxCycles: 3, Operation: "baseline-checks", Result: "baseline-result",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Set.Results[0].Status != CheckFailed || run.Status != implementationstate.RunPaused || len(run.RunResults) != 1 || run.RunResults[0].Status != implementationstate.ResultInterrupted {
		t.Fatalf("canceled baseline was not durably interrupted and paused: result=%#v run=%#v", result, run)
	}
	current, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != implementationstate.RunPaused || len(current.RunResults) != 1 || current.RunResults[0].Status != implementationstate.ResultInterrupted {
		t.Fatalf("durable canceled baseline = %#v", current)
	}
}

func TestInitialRequiredChecksBlocksProtectedMutationWithDurableEvidence(t *testing.T) {
	run, state, journal, repository := newInitialCheckRun(t)
	defer state.Close()
	selection := testCheckSelection([]string{"lint"})
	selection.Checks["lint"] = implementationCheck(repository, "lint")

	result, err := RunInitialRequiredChecks(context.Background(), InitialRequiredChecks{
		Run: run, StateStore: state, Journal: journal, Repository: repository, Selection: selection,
		Runner: CheckRunnerFunc(func(_ context.Context, _ checkexec.Command) (checkexec.Result, error) {
			if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("changed by check\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return checkexec.Result{}, nil
		}), MaxCycles: 3, ProtectedPaths: []string{"tracked.txt"}, Operation: "baseline-checks", Result: "baseline-result",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != implementationstate.RunPaused || run.PauseReason != initialRequiredChecksPauseReason || run.InitialBaseline != nil || len(run.RunResults) != 1 || run.RunResults[0].Status != implementationstate.ResultFailed {
		t.Fatalf("protected baseline mutation was not durably blocked: result=%#v run=%#v", result, run)
	}
	violation, err := os.ReadFile(journal.ViolationJournalPath())
	if err != nil || !strings.Contains(string(violation), "tracked.txt") {
		t.Fatalf("protected baseline mutation has no violation evidence: %q, %v", violation, err)
	}
	contents, err := os.ReadFile(filepath.Join(repository, "tracked.txt"))
	if err != nil || string(contents) != "initial\n" {
		t.Fatalf("protected file was not restored: %q, %v", contents, err)
	}
}

func TestInitialRequiredChecksRunsEntireProjectOrderAndBindsObservedBaseline(t *testing.T) {
	run, state, journal, repository := newInitialCheckRun(t)
	defer state.Close()
	runner := &recordingCheckRunner{}

	result, err := RunInitialRequiredChecks(context.Background(), InitialRequiredChecks{
		Run: run, StateStore: state, Journal: journal, Repository: repository,
		Selection: testCheckSelection([]string{"test_all", "lint", "build"}), Runner: runner,
		MaxCycles: 3, Operation: "baseline-checks", Result: "baseline-result",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, runner, "test_all", "lint", "build")
	assertResultStatuses(t, result.Set, CheckSucceeded, CheckSucceeded, CheckSucceeded)
	if run.Status != implementationstate.RunActive || len(run.RunResults) != 1 || run.RunResults[0].Status != implementationstate.ResultSucceeded {
		t.Fatalf("successful baseline state = %#v", run)
	}
	if run.RunResults[0].State != run.CurrentState || run.CurrentState == run.Identity.BaselineState {
		t.Fatalf("baseline result did not bind its observed current state: result=%#v run=%#v", run.RunResults[0], run)
	}
	if len(run.RunResults[0].Evidence) != 10 {
		t.Fatalf("required check evidence = %#v", run.RunResults[0].Evidence)
	}
}

func TestInitialRequiredChecksRejectsAnythingButExtractedInitialBaseline(t *testing.T) {
	run, state, journal, repository := newInitialCheckRun(t)
	defer state.Close()
	run.CurrentState = implementationstate.EvidenceRef{ID: "other", Digest: run.Identity.BaselineState.Digest}
	if _, err := RunInitialRequiredChecks(context.Background(), InitialRequiredChecks{
		Run: run, StateStore: state, Journal: journal, Repository: repository,
		Selection: testCheckSelection([]string{"lint"}), Runner: &recordingCheckRunner{}, MaxCycles: 3, Operation: "baseline", Result: "result",
	}); !errors.Is(err, ErrInitialRequiredChecks) {
		t.Fatalf("error = %v, want initial-baseline validation error", err)
	}
}

func newInitialCheckRun(t *testing.T) (*implementationstate.Run, *runstore.StateStore, *runstore.Run, string) {
	t.Helper()
	repository := newGitWorkspace(t)
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("initial-baseline")
	if err != nil {
		t.Fatal(err)
	}
	publish := func(id implementationstate.EvidenceID) implementationstate.EvidenceRef {
		t.Helper()
		ref, err := journal.Publish(id, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	baseline := publish("baseline")
	run, err := implementationstate.NewRun(implementationstate.RunIdentity{
		ID: journal.ID(), Change: "change", Repository: repository, WorkCopy: repository, Branch: "feature", BaselineCommit: "base",
		BaselineState: baseline, Specification: publish("specification"), TaskList: publish("tasks"), Configuration: publish("configuration"),
	}, []implementationstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	return run, state, journal, repository
}
