package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

// InteractiveCommand names an implementation-flow command entered at the
// terminal. Commands remain deliberately small: lifecycle decisions are made
// by ImplementationInteractiveController from durable run state, not by the
// terminal frontend.
type InteractiveCommand string

const (
	CommandImplement InteractiveCommand = "/implement"
	CommandPause     InteractiveCommand = "/pause"
	CommandResume    InteractiveCommand = "/resume"
	CommandStop      InteractiveCommand = "/stop"
	CommandStatus    InteractiveCommand = "/status"
)

// ParsedInteractiveCommand preserves an optional change name only for
// /implement. The other commands do not accept arguments.
type ParsedInteractiveCommand struct {
	Command InteractiveCommand
	Change  string
}

// ImplementationLifecycle is the UI-level view of the sole run associated
// with the current work copy. Terminal runs remain observable, but cannot be
// resumed or paused.
type ImplementationLifecycle uint8

const (
	LifecycleNoRun ImplementationLifecycle = iota
	LifecycleActive
	LifecyclePaused
	LifecycleClosed
	LifecycleSucceeded
)

// CommandHint is a selectable row in the implementation command menu.
type CommandHint struct {
	Command     InteractiveCommand
	Description string
}

// CommandMenu is a snapshot for one prompt. Its Commands slice is the only
// selectable command list a UI may render.
type CommandMenu struct {
	Lifecycle ImplementationLifecycle
	Commands  []CommandHint
}

var ErrInteractiveInputCanceled = errors.New("implementation interactive input canceled")

// ParseInteractiveCommand accepts exactly the context-command syntax. It
// intentionally parses a manually typed command before availability is
// checked, so the controller can explain why a known command is unavailable.
func ParseInteractiveCommand(input string) (ParsedInteractiveCommand, error) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return ParsedInteractiveCommand{}, errors.New("enter an implementation command")
	}
	command := InteractiveCommand(fields[0])
	switch command {
	case CommandImplement:
		if len(fields) != 2 {
			return ParsedInteractiveCommand{}, errors.New("/implement requires one OpenSpec change name")
		}
		return ParsedInteractiveCommand{Command: command, Change: fields[1]}, nil
	case CommandPause, CommandResume, CommandStop, CommandStatus:
		if len(fields) != 1 {
			return ParsedInteractiveCommand{}, fmt.Errorf("%s does not take arguments", command)
		}
		return ParsedInteractiveCommand{Command: command}, nil
	default:
		return ParsedInteractiveCommand{}, fmt.Errorf("unknown implementation command %q", fields[0])
	}
}

// CommandsForLifecycle is the single command-visibility policy. /status is
// always read-only and available; a terminal run is a completed context, so a
// different change may be started without making the old run resumable.
func CommandsForLifecycle(lifecycle ImplementationLifecycle) []CommandHint {
	status := CommandHint{Command: CommandStatus, Description: "Show implementation run status."}
	implement := CommandHint{Command: CommandImplement, Description: "Start an OpenSpec implementation run."}
	switch lifecycle {
	case LifecycleNoRun, LifecycleClosed, LifecycleSucceeded:
		return []CommandHint{implement, status}
	case LifecycleActive:
		return []CommandHint{
			{Command: CommandPause, Description: "Pause the active implementation run."},
			{Command: CommandStop, Description: "Close the run and retain its work."},
			status,
		}
	case LifecyclePaused:
		return []CommandHint{
			{Command: CommandResume, Description: "Reconcile the run and resume work."},
			{Command: CommandStop, Description: "Close the run and retain its work."},
			status,
		}
	default:
		return []CommandHint{status}
	}
}

func lifecycleForRun(run *implementationstate.Run) ImplementationLifecycle {
	if run == nil {
		return LifecycleNoRun
	}
	switch run.Status {
	case implementationstate.RunActive:
		return LifecycleActive
	case implementationstate.RunPaused:
		return LifecyclePaused
	case implementationstate.RunClosed:
		return LifecycleClosed
	case implementationstate.RunSucceeded:
		return LifecycleSucceeded
	default:
		return LifecycleNoRun
	}
}

func commandAvailable(lifecycle ImplementationLifecycle, command InteractiveCommand) bool {
	for _, hint := range CommandsForLifecycle(lifecycle) {
		if hint.Command == command {
			return true
		}
	}
	return false
}

func unavailableCommandReason(lifecycle ImplementationLifecycle, command InteractiveCommand) string {
	switch lifecycle {
	case LifecycleNoRun:
		return fmt.Sprintf("%s is unavailable: there is no implementation run", command)
	case LifecycleActive:
		return fmt.Sprintf("%s is unavailable while the run is active", command)
	case LifecyclePaused:
		return fmt.Sprintf("%s is unavailable while the run is paused", command)
	case LifecycleClosed, LifecycleSucceeded:
		return fmt.Sprintf("%s is unavailable: the run is already closed", command)
	default:
		return fmt.Sprintf("%s is unavailable in this context", command)
	}
}

// InteractiveRun holds the resources that must remain associated with one
// current run. Start and continuation wiring are supplied by the composition
// root, while pause, stop, and resume use the durable loop APIs directly.
type InteractiveRun struct {
	Run         *implementationstate.Run
	Control     *UserRunControl
	ResumeInput ResumeInput
}

// ImplementationInteractiveController is the command dispatcher used by a
// terminal UI. Start and Continue are deliberately composition seams: task
// extraction and the subsequent autonomous loop are assembled elsewhere.
// The lifecycle routes themselves are not callbacks: /pause and /stop call
// UserRunControl, and /resume calls Resume so their durable guarantees cannot
// be bypassed by a frontend.
type ImplementationInteractiveController struct {
	Current  func(context.Context) (*InteractiveRun, error)
	Start    func(context.Context, string) (*InteractiveRun, error)
	Continue func(context.Context, *InteractiveRun) error
}

// Menu derives selectable commands from the latest durable run snapshot.
func (controller ImplementationInteractiveController) Menu(ctx context.Context) (CommandMenu, error) {
	run, err := controller.current(ctx)
	if err != nil {
		return CommandMenu{}, err
	}
	lifecycle := lifecycleForRun(run.Run)
	return CommandMenu{Lifecycle: lifecycle, Commands: CommandsForLifecycle(lifecycle)}, nil
}

// Dispatch parses an entered command, rejects known but unavailable commands
// without invoking any lifecycle mutation, and routes valid commands to the
// loop's lifecycle primitives.
func (controller ImplementationInteractiveController) Dispatch(ctx context.Context, input string) (CommandMenu, string, error) {
	parsed, err := ParseInteractiveCommand(input)
	if err != nil {
		return CommandMenu{}, "", err
	}
	run, err := controller.current(ctx)
	if err != nil {
		return CommandMenu{}, "", err
	}
	lifecycle := lifecycleForRun(run.Run)
	if !commandAvailable(lifecycle, parsed.Command) {
		return CommandMenu{Lifecycle: lifecycle, Commands: CommandsForLifecycle(lifecycle)}, unavailableCommandReason(lifecycle, parsed.Command), nil
	}

	switch parsed.Command {
	case CommandStatus:
		return CommandMenu{Lifecycle: lifecycle, Commands: CommandsForLifecycle(lifecycle)}, statusMessage(run.Run), nil
	case CommandImplement:
		if controller.Start == nil {
			return CommandMenu{}, "", errors.New("implementation start route is not configured")
		}
		started, err := controller.Start(ctx, parsed.Change)
		if err != nil {
			return CommandMenu{}, "", err
		}
		if err := controller.continueRun(ctx, started); err != nil {
			return CommandMenu{}, "", err
		}
		return controller.menuForRun(started), "implementation run started", nil
	case CommandPause:
		if run.Control == nil {
			return CommandMenu{}, "", errors.New("implementation pause route is not configured")
		}
		if err := run.Control.Pause(ctx, "paused by user command"); err != nil {
			return CommandMenu{}, "", err
		}
		return controller.menuForRun(run), "implementation run paused", nil
	case CommandStop:
		if run.Control == nil {
			return CommandMenu{}, "", errors.New("implementation stop route is not configured")
		}
		if err := run.Control.Close(ctx, "stopped by user command"); err != nil {
			return CommandMenu{}, "", err
		}
		return controller.menuForRun(run), "implementation run closed", nil
	case CommandResume:
		if run.Run == nil {
			return CommandMenu{}, "", errors.New("implementation resume route has no run")
		}
		input := run.ResumeInput
		input.Run = run.Run
		input.UserControl = run.Control
		if _, err := Resume(ctx, input); err != nil {
			return controller.menuForRun(run), "", err
		}
		if err := controller.continueRun(ctx, run); err != nil {
			return CommandMenu{}, "", err
		}
		return controller.menuForRun(run), "implementation run resumed", nil
	default:
		return CommandMenu{}, "", fmt.Errorf("unsupported implementation command %q", parsed.Command)
	}
}

func (controller ImplementationInteractiveController) current(ctx context.Context) (*InteractiveRun, error) {
	if controller.Current == nil {
		return &InteractiveRun{}, nil
	}
	run, err := controller.Current(ctx)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return &InteractiveRun{}, nil
	}
	return run, nil
}

func (controller ImplementationInteractiveController) continueRun(ctx context.Context, run *InteractiveRun) error {
	if controller.Continue == nil || run == nil || run.Run == nil || run.Run.Status != implementationstate.RunActive {
		return nil
	}
	return controller.Continue(ctx, run)
}

func (controller ImplementationInteractiveController) menuForRun(run *InteractiveRun) CommandMenu {
	var state *implementationstate.Run
	if run != nil {
		state = run.Run
	}
	lifecycle := lifecycleForRun(state)
	return CommandMenu{Lifecycle: lifecycle, Commands: CommandsForLifecycle(lifecycle)}
}

func statusMessage(run *implementationstate.Run) string {
	if run == nil {
		return "no implementation run for this working copy"
	}
	return fmt.Sprintf("implementation run %s is %s", run.Identity.ID, run.Status)
}

// ImplementationInteractiveUI is intentionally presentation-only. In
// particular, it receives the already filtered menu and cannot turn an
// unavailable command into a lifecycle action.
type ImplementationInteractiveUI interface {
	Prompt(CommandMenu) (string, error)
	Report(string)
	ReportError(error)
}

// RunImplementationInteractive drives command input without owning lifecycle
// transitions. A canceled input exits cleanly; all command mutation remains in
// ImplementationInteractiveController.Dispatch.
func RunImplementationInteractive(ctx context.Context, controller ImplementationInteractiveController, ui ImplementationInteractiveUI) error {
	if ui == nil {
		return ErrInteractiveInputCanceled
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		menu, err := controller.Menu(ctx)
		if err != nil {
			return err
		}
		input, err := ui.Prompt(menu)
		if err != nil {
			if errors.Is(err, ErrInteractiveInputCanceled) {
				return nil
			}
			return err
		}
		_, message, err := controller.Dispatch(ctx, input)
		if err != nil {
			ui.ReportError(err)
			continue
		}
		if message != "" {
			ui.Report(message)
		}
	}
}
