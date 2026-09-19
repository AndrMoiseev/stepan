package impl_loop

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/setting"
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
	if fixture.run.Assignments[0].Status != implstate.AssignmentActive || fixture.run.LeafStatus["task"] != implstate.TaskPending {
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
	if fixture.run.Assignments[0].Status != implstate.AssignmentActive || fixture.run.LeafStatus["task"] != implstate.TaskPending {
		t.Fatalf("implementation_ready accepted or completed assignment: %#v", fixture.run.Assignments[0])
	}
	if got := fixture.run.Assignments[0].Counters.ChecksRequested; got != 1 {
		t.Fatalf("prior requested counter = %d, want 1", got)
	}
	if len(fixture.run.Assignments[0].Results) != 2 || fixture.run.Assignments[0].Results[1].Status != implstate.ResultSucceeded {
		t.Fatalf("durable required result = %#v", fixture.run.Assignments[0].Results)
	}
}

func TestImplementerChecksRequestedHasSeparateFiveRequestLimit(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	for index := 1; index <= 4; index++ {
		operation := implstate.OperationID("prior-request-" + strconv.Itoa(index))
		if err := fixture.run.AddOperation("assignment", implstate.Operation{ID: operation, Kind: implstate.OperationCheck, BriefID: "brief", Basis: implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}, Counter: implstate.CycleCounterChecksRequested}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := fixture.state.RecordAssignmentAttemptStartWithLimits(context.Background(), fixture.run, "assignment", operation, controlledCallLimits()); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.state.RecordAssignmentAttemptOutcome(context.Background(), fixture.run, "assignment", operation, implstate.AttemptSucceeded, "prior completed request"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ApplyImplementerTransition(context.Background(), fixture.input("requested-5", "requested-result-5"), fixture.response(ResponseChecksRequested, []string{"lint"})); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyImplementerTransition(context.Background(), fixture.input("requested-6", "requested-result-6"), fixture.response(ResponseChecksRequested, []string{"lint"}))
	if !errors.Is(err, implstate.ErrLimitExceeded) || fixture.run.Status != implstate.RunPaused {
		t.Fatalf("sixth requested check = %v, run=%#v", err, fixture.run)
	}
	assertRunOrder(t, fixture.runner, "lint")
	current, _, err := runstore.ReadJournalCurrent(fixture.journal)
	if err != nil || current.Status != implstate.RunPaused || current.Assignments[0].Counters.ChecksRequested != 5 {
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

func TestUserControlWaitsForFullImplementerCheckBookkeepingBeforePauseOrClose(t *testing.T) {
	for _, test := range []struct {
		name       string
		transition func(*UserRunControl) error
		wantStatus implstate.RunStatus
	}{
		{"pause", func(control *UserRunControl) error { return control.Pause(context.Background(), "user paused command") }, implstate.RunPaused},
		{"close", func(control *UserRunControl) error { return control.Close(context.Background(), "user closed command") }, implstate.RunClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImplementerTransitionFixture(t)
			defer fixture.state.Close()
			control, err := NewUserRunControl(fixture.run, fixture.state)
			if err != nil {
				t.Fatal(err)
			}
			workspace := &unchangedWorkspaceControl{}
			started := make(chan struct{})
			runner := CheckRunnerFunc(func(ctx context.Context, _ checkexec.Command) (checkexec.Result, error) {
				close(started)
				<-ctx.Done()
				return checkexec.Result{ExitCode: -1, Failure: checkexec.FailureCanceled, Stderr: []byte("user interrupted command")}, ctx.Err()
			})
			input := fixture.input("user-check", "user-check-result")
			input.UserControl, input.Workspace, input.Runner = control, workspace, runner
			done := make(chan error, 1)
			go func() {
				_, err := ApplyImplementerTransition(context.Background(), input, fixture.response(ResponseChecksRequested, []string{"test_auth"}))
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("configured command did not start")
			}
			if err := test.transition(control); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, ErrUserOperationInterrupted) {
				t.Fatalf("route error = %v", err)
			}
			persisted, sequence, err := runstore.ReadJournalCurrent(fixture.journal)
			if err != nil {
				t.Fatal(err)
			}
			assignment := persisted.Assignments[0]
			if persisted.Status != test.wantStatus || len(assignment.Operations) != 1 || len(assignment.Operations[0].Attempts) != 1 || assignment.Operations[0].Attempts[0].Outcome != implstate.AttemptInterrupted || len(assignment.Results) != 1 || assignment.Results[0].Status != implstate.ResultInterrupted {
				t.Fatalf("durable user interruption = %#v", persisted)
			}
			captures, differences := workspace.captures, workspace.diffs
			time.Sleep(20 * time.Millisecond)
			later, laterSequence, err := runstore.ReadJournalCurrent(fixture.journal)
			if err != nil || laterSequence != sequence || workspace.captures != captures || workspace.diffs != differences || len(later.Assignments[0].Results) != 1 {
				t.Fatalf("route wrote after user command returned: sequence %d -> %d, workspace %d/%d -> %d/%d, state=%#v, error=%v", sequence, laterSequence, captures, differences, workspace.captures, workspace.diffs, later, err)
			}
			if test.wantStatus == implstate.RunPaused {
				if err := persisted.Resume(); err != nil {
					t.Fatalf("paused run did not remain resumable: %v", err)
				}
			} else if err := persisted.Resume(); !errors.Is(err, implstate.ErrInvalidTransition) {
				t.Fatalf("closed run resumed: %v", err)
			}
		})
	}
}

type implementerTransitionFixture struct {
	run        *implstate.Run
	state      *runstore.StateStore
	journal    *runstore.Run
	repository string
	runner     *recordingCheckRunner
	selection  setting.CheckSelection
}

func newImplementerTransitionFixture(t *testing.T) implementerTransitionFixture {
	t.Helper()
	run, state, journal, repository := newInitialCheckRun(t)
	selection := testCheckSelection([]string{"lint", "test_all"})
	runner := &recordingCheckRunner{}
	if _, err := RunInitialRequiredChecks(context.Background(), InitialRequiredChecks{
		Run: run, Workspace: &unchangedWorkspaceControl{}, StateStore: state, Journal: journal, Repository: repository, Selection: selection, Runner: runner,
		MaxCycles: 3, Operation: "baseline", Result: "baseline-result",
	}); err != nil {
		t.Fatal(err)
	}
	runner.commands = nil
	if err := run.StartAssignment("assignment", []implstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief, err := journal.Publish("brief", []byte("brief"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", implstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
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

func (fixture implementerTransitionFixture) input(operation implstate.OperationID, result implstate.ResultID) ImplementerTransitionInput {
	return ImplementerTransitionInput{Run: fixture.run, Workspace: &unchangedWorkspaceControl{}, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, AssignmentID: "assignment", BriefID: "brief", Selection: fixture.selection, Runner: fixture.runner, Limits: controlledCallLimits(), OperationID: operation, ResultID: result}
}
