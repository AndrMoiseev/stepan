package impl_loop

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

func TestCommandsForLifecycleShowsOnlyValidCommands(t *testing.T) {
	tests := []struct {
		name      string
		lifecycle ImplementationLifecycle
		want      []InteractiveCommand
	}{
		{"no run", LifecycleNoRun, []InteractiveCommand{CommandImplement, CommandStatus}},
		{"active", LifecycleActive, []InteractiveCommand{CommandPause, CommandStop, CommandStatus}},
		{"paused", LifecyclePaused, []InteractiveCommand{CommandResume, CommandStop, CommandStatus}},
		{"closed", LifecycleClosed, []InteractiveCommand{CommandImplement, CommandStatus}},
		{"succeeded", LifecycleSucceeded, []InteractiveCommand{CommandImplement, CommandStatus}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hints := CommandsForLifecycle(test.lifecycle)
			got := make([]InteractiveCommand, 0, len(hints))
			for _, hint := range hints {
				got = append(got, hint.Command)
				if hint.Command == InteractiveCommand("/bootstrap") {
					t.Fatalf("bootstrap is a separate CLI mode and must not appear in an implementation-loop menu: %#v", hints)
				}
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("menu commands = %v, want %v", got, test.want)
			}
		})
	}
}

func TestImplementationInteractiveControllerRejectsUnavailableCommandWithoutMutation(t *testing.T) {
	run, state, _, _ := newInitialCheckRun(t)
	defer state.Close()
	control, err := NewUserRunControl(run, state)
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	controller := ImplementationInteractiveController{
		Current: func(context.Context) (*InteractiveRun, error) {
			return &InteractiveRun{Run: run, Control: control}, nil
		},
		Start: func(context.Context, string) (*InteractiveRun, error) {
			starts++
			return nil, errors.New("must not start")
		},
	}

	menu, message, err := controller.Dispatch(context.Background(), "/implement different-change")
	if err != nil {
		t.Fatal(err)
	}
	if message != "/implement is unavailable while the run is active" {
		t.Fatalf("unavailable command message = %q", message)
	}
	if menu.Lifecycle != LifecycleActive || starts != 0 || run.Status != implstate.RunActive {
		t.Fatalf("unavailable command changed lifecycle: menu=%#v starts=%d run=%s", menu, starts, run.Status)
	}
}

func TestImplementationInteractiveControllerPauseAndStopUseDurableUserControl(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   string
		want    implstate.RunStatus
		message string
	}{
		{"pause active work", "/pause", implstate.RunPaused, "implementation run paused"},
		{"stop active work", "/stop", implstate.RunClosed, "implementation run closed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run, state, journal, _ := newInitialCheckRun(t)
			defer state.Close()
			control, err := NewUserRunControl(run, state)
			if err != nil {
				t.Fatal(err)
			}
			controller := ImplementationInteractiveController{Current: func(context.Context) (*InteractiveRun, error) {
				return &InteractiveRun{Run: run, Control: control}, nil
			}}

			menu, message, err := controller.Dispatch(context.Background(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if message != test.message || run.Status != test.want || menu.Lifecycle != lifecycleForRun(run) {
				t.Fatalf("route result: menu=%#v message=%q run=%#v", menu, message, run)
			}
			persisted, _, err := state.Current(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Status != test.want {
				t.Fatalf("durable status = %s, want %s (journal %s)", persisted.Status, test.want, journal.ID())
			}
		})
	}
}

func TestImplementationInteractiveControllerPauseAndStopInterruptActiveOperation(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  implstate.RunStatus
	}{
		{"pause", "/pause", implstate.RunPaused},
		{"stop", "/stop", implstate.RunClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			run, state, _, _ := newInitialCheckRun(t)
			defer state.Close()
			control, err := NewUserRunControl(run, state)
			if err != nil {
				t.Fatal(err)
			}
			operation, finish, err := control.BeginOperation(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			controller := ImplementationInteractiveController{Current: func(context.Context) (*InteractiveRun, error) {
				return &InteractiveRun{Run: run, Control: control}, nil
			}}
			done := make(chan error, 1)
			go func() {
				_, _, dispatchErr := controller.Dispatch(context.Background(), test.input)
				done <- dispatchErr
			}()
			select {
			case <-operation.Done():
			case <-time.After(time.Second):
				t.Fatal("route did not interrupt active operation")
			}
			if !UserOperationInterrupted(operation) {
				t.Fatalf("operation cancellation cause = %v", context.Cause(operation))
			}
			finish()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if run.Status != test.want {
				t.Fatalf("run status after route interruption = %s, want %s", run.Status, test.want)
			}
		})
	}
}

func TestImplementationInteractiveControllerResumeUsesReconciliationGate(t *testing.T) {
	fixture := newResumeFixture(t, "")
	control, err := NewUserRunControl(fixture.run, fixture.state)
	if err != nil {
		t.Fatal(err)
	}
	continued := 0
	controller := ImplementationInteractiveController{
		Current: func(context.Context) (*InteractiveRun, error) {
			return &InteractiveRun{Run: fixture.run, Control: control, ResumeInput: fixture.input()}, nil
		},
		Continue: func(_ context.Context, run *InteractiveRun) error {
			continued++
			if run.Run.Status != implstate.RunActive {
				t.Fatalf("continue received status %s", run.Run.Status)
			}
			return nil
		},
	}

	menu, message, err := controller.Dispatch(context.Background(), "/resume")
	if err != nil {
		t.Fatal(err)
	}
	if message != "implementation run resumed" || fixture.run.Status != implstate.RunActive || continued != 1 {
		t.Fatalf("resume route = menu=%#v message=%q status=%s continued=%d", menu, message, fixture.run.Status, continued)
	}
	if menu.Lifecycle != LifecycleActive {
		t.Fatalf("post-resume menu lifecycle = %v", menu.Lifecycle)
	}
}

func TestRunImplementationInteractiveRendersActiveThenPausedMenu(t *testing.T) {
	run, state, _, _ := newInitialCheckRun(t)
	defer state.Close()
	control, err := NewUserRunControl(run, state)
	if err != nil {
		t.Fatal(err)
	}
	ui := &interactiveUIFake{inputs: []string{"/pause"}}
	controller := ImplementationInteractiveController{Current: func(context.Context) (*InteractiveRun, error) {
		return &InteractiveRun{Run: run, Control: control}, nil
	}}
	if err := RunImplementationInteractive(context.Background(), controller, ui); err != nil {
		t.Fatal(err)
	}
	if len(ui.menus) != 2 {
		t.Fatalf("prompt menus = %d, want active then paused", len(ui.menus))
	}
	if ui.menus[0].Lifecycle != LifecycleActive || ui.menus[1].Lifecycle != LifecyclePaused {
		t.Fatalf("prompt lifecycles = %#v", ui.menus)
	}
	if !slices.Equal(commandsFromHints(ui.menus[0].Commands), []InteractiveCommand{CommandPause, CommandStop, CommandStatus}) || !slices.Equal(commandsFromHints(ui.menus[1].Commands), []InteractiveCommand{CommandResume, CommandStop, CommandStatus}) {
		t.Fatalf("rendered menus = %#v", ui.menus)
	}
	if !slices.Equal(ui.messages, []string{"implementation run paused"}) {
		t.Fatalf("reported messages = %#v", ui.messages)
	}
}

func TestRunImplementationInteractiveReportsManuallyEnteredUnavailableCommand(t *testing.T) {
	run, state, _, _ := newInitialCheckRun(t)
	defer state.Close()
	control, err := NewUserRunControl(run, state)
	if err != nil {
		t.Fatal(err)
	}
	ui := &interactiveUIFake{inputs: []string{"/resume"}}
	controller := ImplementationInteractiveController{Current: func(context.Context) (*InteractiveRun, error) {
		return &InteractiveRun{Run: run, Control: control}, nil
	}}
	if err := RunImplementationInteractive(context.Background(), controller, ui); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ui.messages, []string{"/resume is unavailable while the run is active"}) {
		t.Fatalf("unavailable-command feedback = %#v", ui.messages)
	}
	if run.Status != implstate.RunActive || len(ui.errors) != 0 {
		t.Fatalf("unavailable command mutated run or reported an error: run=%s errors=%#v", run.Status, ui.errors)
	}
}

func TestRunImplementationInteractiveAcceptsStatusAndPauseWhileContinueRuns(t *testing.T) {
	run, state, _, _ := newInitialCheckRun(t)
	defer state.Close()
	control, err := NewUserRunControl(run, state)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	ui := &blockedWorkUI{started: started}
	var current *InteractiveRun
	controller := ImplementationInteractiveController{
		Current: func(context.Context) (*InteractiveRun, error) { return current, nil },
		Start: func(context.Context, string) (*InteractiveRun, error) {
			current = &InteractiveRun{Run: run, Control: control}
			return current, nil
		},
		Continue: func(ctx context.Context, active *InteractiveRun) error {
			operation, finish, err := active.Control.BeginOperation(ctx)
			if err != nil {
				return err
			}
			defer finish()
			close(started)
			<-operation.Done()
			return context.Cause(operation)
		},
	}

	if err := RunImplementationInteractive(context.Background(), controller, ui); err != nil {
		t.Fatal(err)
	}
	if run.Status != implstate.RunPaused {
		t.Fatalf("run status after pause during continue = %s", run.Status)
	}
	if len(ui.menus) < 4 || ui.menus[1].Lifecycle != LifecycleActive || ui.menus[2].Lifecycle != LifecycleActive || ui.menus[3].Lifecycle != LifecyclePaused {
		t.Fatalf("menus did not remain live during active continuation then return paused: %#v", ui.menus)
	}
	if !slices.Equal(commandsFromHints(ui.menus[1].Commands), []InteractiveCommand{CommandPause, CommandStop, CommandStatus}) {
		t.Fatalf("active continuation menu = %#v", ui.menus[1])
	}
	if !slices.Equal(commandsFromHints(ui.menus[3].Commands), []InteractiveCommand{CommandResume, CommandStop, CommandStatus}) {
		t.Fatalf("paused continuation menu = %#v", ui.menus[3])
	}
	if !slices.Contains(ui.messages, "implementation run initial-baseline is active") || !slices.Contains(ui.messages, "implementation run paused") {
		t.Fatalf("messages while continuation was active = %#v", ui.messages)
	}
	if len(ui.errors) != 1 || !errors.Is(ui.errors[0], ErrUserOperationInterrupted) {
		t.Fatalf("background interruption result = %#v", ui.errors)
	}
}

func TestRunImplementationInteractivePublishesConciseActiveProgressWithoutAgentTranscript(t *testing.T) {
	run, state, _, _ := newInitialCheckRun(t)
	defer state.Close()
	run.Tasks = []implstate.Task{{ID: "12.3", Title: "show progress"}}
	run.Assignments = []implstate.Assignment{{
		ID: "assignment-12", TaskIDs: []implstate.TaskID{"12.3"}, Status: implstate.AssignmentActive,
		Operations: []implstate.Operation{{ID: "implement", Kind: implstate.OperationAgent, Description: "implement assignment", Attempts: []implstate.OperationAttempt{{Number: 1}}}},
	}}
	control, err := NewUserRunControl(run, state)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	ui := &blockedWorkUI{started: started}
	var current *InteractiveRun
	controller := ImplementationInteractiveController{
		Runtime: setting.Platform{OS: "windows", Architecture: "amd64"},
		Current: func(context.Context) (*InteractiveRun, error) { return current, nil },
		Start: func(context.Context, string) (*InteractiveRun, error) {
			current = &InteractiveRun{Run: run, Control: control}
			return current, nil
		},
		Continue: func(ctx context.Context, active *InteractiveRun) error {
			operation, finish, err := active.Control.BeginOperation(ctx)
			if err != nil {
				return err
			}
			defer finish()
			close(started)
			<-operation.Done()
			return context.Cause(operation)
		},
	}
	if err := RunImplementationInteractive(context.Background(), controller, ui); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(ui.messages, "\n")
	for _, want := range []string{"runtime platform: windows/amd64", "assignment: assignment-12 — 12.3 (show progress)", "current: implementer — implement assignment"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("live progress missing %q: %#v", want, ui.messages)
		}
	}
	if strings.Contains(joined, "agent private transcript") {
		t.Fatalf("interactive UI exposed agent transcript: %#v", ui.messages)
	}
}

func TestRunImplementationInteractiveEOFJoinsBackgroundWork(t *testing.T) {
	run, state, _, _ := newInitialCheckRun(t)
	defer state.Close()
	control, err := NewUserRunControl(run, state)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan struct{})
	ui := &eofDuringWorkUI{started: started}
	var current *InteractiveRun
	controller := ImplementationInteractiveController{
		Current: func(context.Context) (*InteractiveRun, error) { return current, nil },
		Start: func(context.Context, string) (*InteractiveRun, error) {
			current = &InteractiveRun{Run: run, Control: control}
			return current, nil
		},
		Continue: func(ctx context.Context, active *InteractiveRun) error {
			operation, finish, err := active.Control.BeginOperation(ctx)
			if err != nil {
				return err
			}
			defer func() {
				finish()
				close(finished)
			}()
			close(started)
			<-operation.Done()
			return context.Cause(operation)
		},
	}

	if err := RunImplementationInteractive(context.Background(), controller, ui); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("EOF returned before background work stopped")
	}
	if len(ui.errors) != 0 {
		t.Fatalf("expected cancellation during EOF to be quiet, got %#v", ui.errors)
	}
}

func TestRunImplementationInteractiveStopJoinsBlockedResumeBeforeClosing(t *testing.T) {
	fixture := newResumeFixture(t, "")
	configuration, err := fixture.load(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	loaderStarted := make(chan struct{})
	releaseLoader := make(chan struct{})
	fixture.load = func(string) (setting.Configuration, error) {
		close(loaderStarted)
		<-releaseLoader
		return configuration, nil
	}
	checks, continued := 0, 0
	input := fixture.input()
	input.Runner = CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
		checks++
		return checkexec.Result{}, nil
	})
	control, err := NewUserRunControl(fixture.run, fixture.state)
	if err != nil {
		t.Fatal(err)
	}
	ui := &blockedResumeStopUI{loaderStarted: loaderStarted, releaseLoader: releaseLoader}
	controller := ImplementationInteractiveController{
		Current: func(context.Context) (*InteractiveRun, error) {
			return &InteractiveRun{Run: fixture.run, Control: control, ResumeInput: input}, nil
		},
		Continue: func(context.Context, *InteractiveRun) error {
			continued++
			return nil
		},
	}

	if err := RunImplementationInteractive(context.Background(), controller, ui); err != nil {
		t.Fatal(err)
	}
	if fixture.run.Status != implstate.RunClosed || checks != 0 || continued != 0 {
		t.Fatalf("stop during resume = status %s, checks %d, continue %d", fixture.run.Status, checks, continued)
	}
	persisted, _, err := fixture.state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != implstate.RunClosed {
		t.Fatalf("durable status after stopped resume = %s", persisted.Status)
	}
	if len(ui.menus) < 2 || !slices.Equal(commandsFromHints(ui.menus[1].Commands), []InteractiveCommand{CommandStop, CommandStatus}) {
		t.Fatalf("resuming menu exposed unusable commands: %#v", ui.menus)
	}
}

func TestRunImplementationInteractiveRefreshesMenuWhenResumeChecksBecomeActive(t *testing.T) {
	fixture := newResumeFixture(t, "")
	checkStarted := make(chan struct{})
	checks := 0
	input := fixture.input()
	input.Runner = CheckRunnerFunc(func(ctx context.Context, _ checkexec.Command) (checkexec.Result, error) {
		checks++
		close(checkStarted)
		<-ctx.Done()
		return checkexec.Result{Failure: checkexec.FailureCanceled}, ctx.Err()
	})
	control, err := NewUserRunControl(fixture.run, fixture.state)
	if err != nil {
		t.Fatal(err)
	}
	ui := &resumePhaseUI{checkStarted: checkStarted}
	controller := ImplementationInteractiveController{Current: func(context.Context) (*InteractiveRun, error) {
		return &InteractiveRun{Run: fixture.run, Control: control, ResumeInput: input}, nil
	}}

	if err := RunImplementationInteractive(context.Background(), controller, ui); err != nil {
		t.Fatal(err)
	}
	if checks != 1 || fixture.run.Status != implstate.RunPaused {
		t.Fatalf("paused resume check = checks %d status %s", checks, fixture.run.Status)
	}
	if len(ui.menus) < 3 {
		t.Fatalf("expected initial, resuming, and active menus; got %#v", ui.menus)
	}
	if !slices.Equal(commandsFromHints(ui.menus[1].Commands), []InteractiveCommand{CommandStop, CommandStatus}) {
		t.Fatalf("pre-check resume menu = %#v", ui.menus[1])
	}
	if !slices.Equal(commandsFromHints(ui.menus[2].Commands), []InteractiveCommand{CommandPause, CommandStop, CommandStatus}) {
		t.Fatalf("active resume-check menu = %#v", ui.menus[2])
	}
}

func TestStatusIsReadOnly(t *testing.T) {
	run, state, _, _ := newInitialCheckRun(t)
	defer state.Close()
	controller := ImplementationInteractiveController{Current: func(context.Context) (*InteractiveRun, error) { return &InteractiveRun{Run: run}, nil }}
	_, message, err := controller.Dispatch(context.Background(), "/status")
	if err != nil {
		t.Fatal(err)
	}
	if message != "implementation run initial-baseline is active" || run.Status != implstate.RunActive {
		t.Fatalf("status route mutated run: message=%q state=%#v", message, run)
	}
}

func commandsFromHints(hints []CommandHint) []InteractiveCommand {
	result := make([]InteractiveCommand, 0, len(hints))
	for _, hint := range hints {
		result = append(result, hint.Command)
	}
	return result
}

type interactiveUIFake struct {
	inputs   []string
	menus    []CommandMenu
	messages []string
	errors   []error
}

func (ui *interactiveUIFake) Prompt(_ context.Context, menu CommandMenu) (string, error) {
	ui.menus = append(ui.menus, menu)
	if len(ui.inputs) == 0 {
		return "", ErrInteractiveInputCanceled
	}
	input := ui.inputs[0]
	ui.inputs = ui.inputs[1:]
	return input, nil
}

func (ui *interactiveUIFake) Report(message string) { ui.messages = append(ui.messages, message) }
func (ui *interactiveUIFake) ReportError(err error) { ui.errors = append(ui.errors, err) }

type blockedWorkUI struct {
	started  <-chan struct{}
	step     int
	menus    []CommandMenu
	messages []string
	errors   []error
}

func (ui *blockedWorkUI) Prompt(ctx context.Context, menu CommandMenu) (string, error) {
	ui.menus = append(ui.menus, menu)
	switch ui.step {
	case 0:
		ui.step++
		return "/implement change", nil
	case 1:
		select {
		case <-ui.started:
			ui.step++
			return "/status", nil
		case <-ctx.Done():
			return "", ErrInteractiveInputCanceled
		}
	case 2:
		ui.step++
		return "/pause", nil
	default:
		// The background result may have been delivered just before the next
		// prompt is constructed. EOF still exercises driver shutdown without
		// keeping the fake input goroutine alive.
		return "", ErrInteractiveInputCanceled
	}
}

func (ui *blockedWorkUI) Report(message string) { ui.messages = append(ui.messages, message) }
func (ui *blockedWorkUI) ReportError(err error) { ui.errors = append(ui.errors, err) }

type eofDuringWorkUI struct {
	started <-chan struct{}
	step    int
	errors  []error
}

func (ui *eofDuringWorkUI) Prompt(ctx context.Context, _ CommandMenu) (string, error) {
	switch ui.step {
	case 0:
		ui.step++
		return "/implement change", nil
	case 1:
		select {
		case <-ui.started:
			ui.step++
			return "", ErrInteractiveInputCanceled
		case <-ctx.Done():
			return "", ErrInteractiveInputCanceled
		}
	default:
		return "", ErrInteractiveInputCanceled
	}
}

func (*eofDuringWorkUI) Report(string)            {}
func (ui *eofDuringWorkUI) ReportError(err error) { ui.errors = append(ui.errors, err) }

type blockedResumeStopUI struct {
	loaderStarted <-chan struct{}
	releaseLoader chan struct{}
	step          int
	menus         []CommandMenu
}

func (ui *blockedResumeStopUI) Prompt(_ context.Context, menu CommandMenu) (string, error) {
	ui.menus = append(ui.menus, menu)
	switch ui.step {
	case 0:
		ui.step++
		return "/resume", nil
	case 1:
		<-ui.loaderStarted
		close(ui.releaseLoader)
		ui.step++
		return "/stop", nil
	default:
		return "", ErrInteractiveInputCanceled
	}
}

func (*blockedResumeStopUI) Report(string)     {}
func (*blockedResumeStopUI) ReportError(error) {}

type resumePhaseUI struct {
	checkStarted <-chan struct{}
	step         int
	menus        []CommandMenu
}

func (ui *resumePhaseUI) Prompt(ctx context.Context, menu CommandMenu) (string, error) {
	ui.menus = append(ui.menus, menu)
	switch ui.step {
	case 0:
		ui.step++
		return "/resume", nil
	case 1:
		ui.step++
		select {
		case <-ui.checkStarted:
			<-ctx.Done() // phase notification must cancel and refresh this prompt.
			return "", ErrInteractiveInputCanceled
		case <-ctx.Done():
			return "", ErrInteractiveInputCanceled
		}
	case 2:
		ui.step++
		return "/pause", nil
	default:
		return "", ErrInteractiveInputCanceled
	}
}

func (*resumePhaseUI) Report(string)     {}
func (*resumePhaseUI) ReportError(error) {}
