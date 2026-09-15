package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
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
		return controller.resume(ctx, run)
	default:
		return CommandMenu{}, "", fmt.Errorf("unsupported implementation command %q", parsed.Command)
	}
}

func (controller ImplementationInteractiveController) resume(ctx context.Context, run *InteractiveRun) (CommandMenu, string, error) {
	if run == nil || run.Run == nil {
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
	// Prompt must return promptly when ctx is canceled. The driver cancels a
	// pending prompt to publish a completed background result or to shut down
	// without leaving an input goroutine behind.
	Prompt(context.Context, CommandMenu) (string, error)
	Report(string)
	ReportError(error)
}

type interactiveWorkResult struct {
	message string
	err     error
}

type interactiveWorkPhase uint8

const (
	interactiveWorkNone interactiveWorkPhase = iota
	interactiveWorkResuming
	interactiveWorkActive
)

type implementationInteractiveDriver struct {
	controller ImplementationInteractiveController
	ui         ImplementationInteractiveUI
	workCancel context.CancelFunc
	workDone   chan interactiveWorkResult
	workPhase  interactiveWorkPhase
	phaseReady <-chan struct{}
}

func (driver *implementationInteractiveDriver) workActive() bool { return driver.workDone != nil }

func (driver *implementationInteractiveDriver) startWork(parent context.Context, phase interactiveWorkPhase, phaseReady <-chan struct{}, work func(context.Context) (string, error)) error {
	if driver.workActive() {
		return errors.New("implementation work is already running")
	}
	ctx, cancel := context.WithCancel(parent)
	driver.workCancel = cancel
	driver.workDone = make(chan interactiveWorkResult, 1)
	driver.workPhase = phase
	driver.phaseReady = phaseReady
	go func(done chan<- interactiveWorkResult) {
		message, err := work(ctx)
		done <- interactiveWorkResult{message: message, err: err}
	}(driver.workDone)
	return nil
}

func (driver *implementationInteractiveDriver) finishWork() {
	if driver.workCancel != nil {
		driver.workCancel()
	}
	driver.workCancel = nil
	driver.workDone = nil
	driver.workPhase = interactiveWorkNone
	driver.phaseReady = nil
}

func (driver *implementationInteractiveDriver) setWorkPhase(phase interactiveWorkPhase) {
	if !driver.workActive() || driver.workPhase == phase {
		return
	}
	driver.workPhase = phase
	if phase == interactiveWorkActive {
		driver.phaseReady = nil
	}
}

func (driver *implementationInteractiveDriver) menu(ctx context.Context) (CommandMenu, error) {
	switch driver.workPhase {
	case interactiveWorkResuming:
		// Reconciliation can still be paused while configuration/rules are
		// loading. Only stop and status are usable until the check operation is
		// registered with UserRunControl.
		return CommandMenu{Lifecycle: LifecyclePaused, Commands: []CommandHint{
			{Command: CommandStop, Description: "Close the run and retain its work."},
			{Command: CommandStatus, Description: "Show implementation run status."},
		}}, nil
	case interactiveWorkActive:
		return CommandMenu{Lifecycle: LifecycleActive, Commands: CommandsForLifecycle(LifecycleActive)}, nil
	default:
		return driver.controller.Menu(ctx)
	}
}

func (driver *implementationInteractiveDriver) reportWork(result interactiveWorkResult) {
	driver.finishWork()
	if result.err != nil {
		driver.ui.ReportError(result.err)
		return
	}
	if result.message != "" {
		driver.ui.Report(result.message)
	}
}

func (driver *implementationInteractiveDriver) stopWork() {
	if !driver.workActive() {
		return
	}
	driver.workCancel()
	result := <-driver.workDone
	driver.finishWork()
	// The caller is leaving the UI, so the cancellation is expected. An
	// otherwise meaningful error is still surfaced deterministically before
	// returning to the caller.
	if result.err != nil && !errors.Is(result.err, context.Canceled) {
		driver.ui.ReportError(result.err)
	}
}

func (driver *implementationInteractiveDriver) beginImplement(ctx context.Context, change string) (string, error) {
	run, err := driver.controller.current(ctx)
	if err != nil {
		return "", err
	}
	lifecycle := lifecycleForRun(run.Run)
	if !commandAvailable(lifecycle, CommandImplement) {
		return unavailableCommandReason(lifecycle, CommandImplement), nil
	}
	if driver.controller.Start == nil {
		return "", errors.New("implementation start route is not configured")
	}
	started, err := driver.controller.Start(ctx, change)
	if err != nil {
		return "", err
	}
	if started == nil {
		return "", errors.New("implementation start route returned no run")
	}
	if err := driver.startWork(ctx, interactiveWorkActive, nil, func(workCtx context.Context) (string, error) {
		if err := driver.controller.continueRun(workCtx, started); err != nil {
			return "", err
		}
		return "implementation run is waiting for the next controller action", nil
	}); err != nil {
		return "", err
	}
	return "implementation run started", nil
}

func (driver *implementationInteractiveDriver) beginResume(ctx context.Context) (string, error) {
	run, err := driver.controller.current(ctx)
	if err != nil {
		return "", err
	}
	lifecycle := lifecycleForRun(run.Run)
	if !commandAvailable(lifecycle, CommandResume) {
		return unavailableCommandReason(lifecycle, CommandResume), nil
	}
	// Closing this one-shot channel cannot be lost if a check starts before
	// the driver reaches its select. The prompt is then canceled and rendered
	// again with the active-operation menu.
	phaseReady := make(chan struct{})
	var phaseOnce sync.Once
	resumeRun := *run
	resumeInput := run.ResumeInput
	runner := resumeInput.Runner
	if runner != nil {
		resumeInput.Runner = CheckRunnerFunc(func(checkCtx context.Context, command checkexec.Command) (checkexec.Result, error) {
			phaseOnce.Do(func() { close(phaseReady) })
			return runner.RunCheck(checkCtx, command)
		})
	}
	resumeRun.ResumeInput = resumeInput
	if err := driver.startWork(ctx, interactiveWorkResuming, phaseReady, func(workCtx context.Context) (string, error) {
		_, message, err := driver.controller.resume(workCtx, &resumeRun)
		return message, err
	}); err != nil {
		return "", err
	}
	return "implementation run is resuming", nil
}

func (driver *implementationInteractiveDriver) handle(ctx context.Context, input string) (string, error) {
	parsed, err := ParseInteractiveCommand(input)
	if err != nil {
		return "", err
	}
	if driver.workActive() && (parsed.Command == CommandImplement || parsed.Command == CommandResume) {
		return "implementation work is already running", nil
	}
	switch parsed.Command {
	case CommandImplement:
		return driver.beginImplement(ctx, parsed.Change)
	case CommandResume:
		return driver.beginResume(ctx)
	default:
		if parsed.Command == CommandStop && driver.workActive() {
			// Joining the canceled resume prevents its stale candidate from
			// writing an active state after this terminal close.
			driver.stopWork()
		}
		_, message, err := driver.controller.Dispatch(ctx, input)
		if parsed.Command == CommandPause && err == nil && driver.workActive() {
			// UserRunControl has durably paused the run and waited for any
			// registered operation to stop. Cancel the controller-owned context as
			// well, then drain its result before deriving another menu: otherwise a
			// stale active worker would hide the newly usable /resume command.
			driver.workCancel()
			result := <-driver.workDone
			driver.reportWork(result)
		}
		return message, err
	}
}

// RunImplementationInteractive runs long-lived continuation and resume work
// in one controller-owned background slot. The prompt remains live while an
// agent or required check owns UserRunControl, so /pause, /stop, and /status
// can be routed immediately. A completed worker cancels the pending prompt,
// reports exactly one result, and then presents a fresh lifecycle menu.
func RunImplementationInteractive(ctx context.Context, controller ImplementationInteractiveController, ui ImplementationInteractiveUI) error {
	if ui == nil {
		return ErrInteractiveInputCanceled
	}
	driver := implementationInteractiveDriver{controller: controller, ui: ui}
	defer driver.stopWork()
interactiveLoop:
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		menu, err := driver.menu(ctx)
		if err != nil {
			return err
		}

		promptCtx, cancelPrompt := context.WithCancel(ctx)
		type promptResult struct {
			input string
			err   error
		}
		promptDone := make(chan promptResult, 1)
		go func() {
			input, err := ui.Prompt(promptCtx, menu)
			promptDone <- promptResult{input: input, err: err}
		}()

		if driver.workActive() {
			for {
				select {
				case <-driver.phaseReady:
					cancelPrompt()
					<-promptDone
					driver.setWorkPhase(interactiveWorkActive)
					continue interactiveLoop
				case result := <-driver.workDone:
					cancelPrompt()
					<-promptDone
					driver.reportWork(result)
					continue interactiveLoop
				case result := <-promptDone:
					cancelPrompt()
					if result.err != nil {
						if errors.Is(result.err, ErrInteractiveInputCanceled) {
							return nil
						}
						return result.err
					}
					message, handleErr := driver.handle(ctx, result.input)
					if handleErr != nil {
						ui.ReportError(handleErr)
					} else if message != "" {
						ui.Report(message)
					}
					continue interactiveLoop
				case <-ctx.Done():
					cancelPrompt()
					<-promptDone
					return ctx.Err()
				}
			}
		}

		result := <-promptDone
		cancelPrompt()
		if result.err != nil {
			if errors.Is(result.err, ErrInteractiveInputCanceled) {
				return nil
			}
			return result.err
		}
		message, handleErr := driver.handle(ctx, result.input)
		if handleErr != nil {
			ui.ReportError(handleErr)
		} else if message != "" {
			ui.Report(message)
		}
	}
}
