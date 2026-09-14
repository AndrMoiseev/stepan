package impl_loop

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestImplementerChecksRequestedReturnsConfiguredResultsToContinuation(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()

	result, err := ApplyImplementerTransition(context.Background(), fixture.input("requested", "requested-result"), fixture.response(ResponseChecksRequested, []string{"test_auth"}))
	if err != nil {
		t.Fatal(err)
	}
	if result.RequiredAcceptance || !result.Set.Succeeded() || len(result.Set.Results) != 1 || result.Set.Results[0].Name != "test_auth" || result.Set.Results[0].Presentation == nil {
		t.Fatalf("requested feedback = %#v", result)
	}
	assertRunOrder(t, fixture.runner, "test_auth")
	if fixture.run.Assignments[0].Status != implementationstate.AssignmentActive || fixture.run.LeafStatus["task"] != implementationstate.TaskPending {
		t.Fatalf("checks_requested changed task lifecycle: %#v", fixture.run.Assignments[0])
	}
	if got := fixture.run.Assignments[0].Counters.ChecksRequested; got != 1 {
		t.Fatalf("requested counter = %d, want 1", got)
	}
	if data, err := fixture.journal.Read(result.Evidence); err != nil || !strings.Contains(string(data), `"test_auth"`) {
		t.Fatalf("durable requested feedback = %q, %v", data, err)
	}
}

func TestImplementerReadyAlwaysRunsFullRequiredSetWithoutCompletingAssignment(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	if _, err := ApplyImplementerTransition(context.Background(), fixture.input("requested", "requested-result"), fixture.response(ResponseChecksRequested, []string{"test_auth"})); err != nil {
		t.Fatal(err)
	}

	message := "ready for acceptance"
	result, err := ApplyImplementerTransition(context.Background(), fixture.input("required", "required-result"), AgentResponse{
		Kind: ResponseImplementationReady, Message: &message, Binding: fixture.binding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RequiredAcceptance || !result.Set.Succeeded() {
		t.Fatalf("required result = %#v", result)
	}
	assertRunOrder(t, fixture.runner, "test_auth", "lint", "test_all")
	if fixture.run.Assignments[0].Status != implementationstate.AssignmentActive || fixture.run.LeafStatus["task"] != implementationstate.TaskPending {
		t.Fatalf("implementation_ready accepted or completed assignment: %#v", fixture.run.Assignments[0])
	}
	if got := fixture.run.Assignments[0].Counters.ChecksRequested; got != 1 {
		t.Fatalf("prior requested counter = %d, want 1", got)
	}
	if len(fixture.run.Assignments[0].Results) != 2 || fixture.run.Assignments[0].Results[1].Status != implementationstate.ResultSucceeded {
		t.Fatalf("durable required result = %#v", fixture.run.Assignments[0].Results)
	}
}

func TestImplementerChecksRequestedHasSeparateFiveRequestLimit(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	for index := 1; index <= 4; index++ {
		operation := implementationstate.OperationID("prior-request-" + strconv.Itoa(index))
		if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: operation, Kind: implementationstate.OperationCheck, BriefID: "brief", Basis: implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}, Counter: implementationstate.CycleCounterChecksRequested}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := fixture.state.RecordAssignmentAttemptStartWithLimits(context.Background(), fixture.run, "assignment", operation, controlledCallLimits()); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.state.RecordAssignmentAttemptOutcome(context.Background(), fixture.run, "assignment", operation, implementationstate.AttemptSucceeded, "prior completed request"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ApplyImplementerTransition(context.Background(), fixture.input("requested-5", "requested-result-5"), fixture.response(ResponseChecksRequested, []string{"lint"})); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyImplementerTransition(context.Background(), fixture.input("requested-6", "requested-result-6"), fixture.response(ResponseChecksRequested, []string{"lint"}))
	if !errors.Is(err, implementationstate.ErrLimitExceeded) || fixture.run.Status != implementationstate.RunPaused {
		t.Fatalf("sixth requested check = %v, run=%#v", err, fixture.run)
	}
	assertRunOrder(t, fixture.runner, "lint")
	current, _, err := runstore.ReadJournalCurrent(fixture.journal)
	if err != nil || current.Status != implementationstate.RunPaused || current.Assignments[0].Counters.ChecksRequested != 5 {
		t.Fatalf("durable five-request limit = %#v, %v", current, err)
	}
}

func TestImplementerChecksRequestedRejectsCommandLikeNameBeforeDispatch(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	_, err := ApplyImplementerTransition(context.Background(), fixture.input("requested", "requested-result"), fixture.response(ResponseChecksRequested, []string{"test_all -run private"}))
	if !errors.Is(err, ErrUnknownCheck) || len(fixture.runner.commands) != 0 || len(fixture.run.Assignments[0].Operations) != 0 {
		t.Fatalf("command-like request = %v, calls=%#v, operations=%#v", err, fixture.runner.commands, fixture.run.Assignments[0].Operations)
	}
}

type implementerTransitionFixture struct {
	run        *implementationstate.Run
	state      *runstore.StateStore
	journal    *runstore.Run
	repository string
	runner     *recordingCheckRunner
	selection  implementationconfig.CheckSelection
}

func newImplementerTransitionFixture(t *testing.T) implementerTransitionFixture {
	t.Helper()
	run, state, journal, repository := newInitialCheckRun(t)
	selection := testCheckSelection([]string{"lint", "test_all"})
	runner := &recordingCheckRunner{}
	if _, err := RunInitialRequiredChecks(context.Background(), InitialRequiredChecks{
		Run: run, StateStore: state, Journal: journal, Repository: repository, Selection: selection, Runner: runner,
		MaxCycles: 3, Operation: "baseline", Result: "baseline-result",
	}); err != nil {
		t.Fatal(err)
	}
	runner.commands = nil
	if err := run.StartAssignment("assignment", []implementationstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief, err := journal.Publish("brief", []byte("brief"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", implementationstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return implementerTransitionFixture{run: run, state: state, journal: journal, repository: repository, runner: runner, selection: selection}
}

func (fixture implementerTransitionFixture) binding() ResponseBinding {
	return ResponseBinding{CallID: "executor-call", RunID: fixture.run.Identity.ID, AssignmentID: "assignment", BriefID: "brief", Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration, TaskList: fixture.run.Identity.TaskList}
}

func (fixture implementerTransitionFixture) response(kind ResponseKind, names []string) AgentResponse {
	return AgentResponse{Kind: kind, CheckNames: names, Binding: fixture.binding()}
}

func (fixture implementerTransitionFixture) input(operation implementationstate.OperationID, result implementationstate.ResultID) ImplementerTransitionInput {
	return ImplementerTransitionInput{Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, AssignmentID: "assignment", BriefID: "brief", Selection: fixture.selection, Runner: fixture.runner, Limits: controlledCallLimits(), OperationID: operation, ResultID: result}
}
