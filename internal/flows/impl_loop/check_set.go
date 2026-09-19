package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

var (
	// ErrUnknownCheck means an agent requested a name which is not declared by
	// the project's checks configuration. The entire request is rejected before
	// any command can start.
	ErrUnknownCheck = errors.New("unknown configured check")
	// ErrInvalidCheckSet means the controller was asked to run an unsupported
	// kind of check set.
	ErrInvalidCheckSet = errors.New("invalid check set")
)

// checkResultPersistenceTimeout matches the default configured check timeout.
// It bounds the controller-owned post-termination capture/publication step
// without inheriting a user pause, stop, or command deadline that has already
// terminated the child process.
const checkResultPersistenceTimeout = 600 * time.Second

// CheckSetKind identifies why the controller is running a set of configured
// checks. Requested sets use the caller's name order; required sets always use
// the project-defined required order.
type CheckSetKind string

const (
	CheckSetRequested CheckSetKind = "checks_requested"
	CheckSetRequired  CheckSetKind = "required_checks"
)

// CheckStatus distinguishes a check that was not started after an earlier
// failure from one that could not run on this platform. Neither is success.
type CheckStatus string

const (
	CheckSucceeded     CheckStatus = "succeeded"
	CheckFailed        CheckStatus = "failed"
	CheckNotRun        CheckStatus = "not_run"
	CheckNotApplicable CheckStatus = "not_applicable"
)

// CheckSetResult preserves the configured name and direct-execution result of
// every requested check. NotRun entries deliberately have no command result:
// they make the skipped tail of a fail-fast set explicit.
type CheckSetResult struct {
	Name         string             `json:"name"`
	Status       CheckStatus        `json:"status"`
	Command      checkexec.Command  `json:"-"`
	Result       checkexec.Result   `json:"-"`
	Duration     time.Duration      `json:"duration"`
	Presentation *CheckPresentation `json:"presentation,omitempty"`
	Err          error              `json:"-"`
}

// CheckSet is the ordered outcome of one requested or required set.
type CheckSet struct {
	Kind    CheckSetKind
	Results []CheckSetResult
}

// Succeeded reports whether every check in the set completed successfully.
// A platform-inapplicable check and an explicitly not-run check never count as
// success, so callers cannot silently accept a partial required set.
func (set CheckSet) Succeeded() bool {
	for _, result := range set.Results {
		if result.Status != CheckSucceeded {
			return false
		}
	}
	return true
}

// executionBlockedResult identifies failures that the executor cannot repair
// by changing assignment code. A non-zero program exit remains ordinary
// implementation feedback; an unavailable configured command or controller
// infrastructure failure requires user remediation instead.
func (set CheckSet) executionBlockedResult() (CheckSetResult, bool) {
	for _, result := range set.Results {
		if result.Status == CheckNotApplicable || result.Result.Failure == checkexec.FailureLaunch || result.Result.Failure == checkexec.FailureInfrastructure {
			return result, true
		}
	}
	return CheckSetResult{}, false
}

// CheckRunner is the direct-command boundary. It is injected to keep set
// ordering tests deterministic; DirectCheckRunner connects production calls to
// checkexec from tasks 5.1 and 5.2.
type CheckRunner interface {
	RunCheck(context.Context, checkexec.Command) (checkexec.Result, error)
}

// CheckRunnerFunc adapts a function to CheckRunner.
type CheckRunnerFunc func(context.Context, checkexec.Command) (checkexec.Result, error)

func (runner CheckRunnerFunc) RunCheck(ctx context.Context, command checkexec.Command) (checkexec.Result, error) {
	return runner(ctx, command)
}

// DirectCheckRunner runs commands through checkexec.
type DirectCheckRunner struct{}

func (DirectCheckRunner) RunCheck(ctx context.Context, command checkexec.Command) (checkexec.Result, error) {
	return checkexec.RunContext(ctx, command)
}

// CheckResultReporter receives every command that was actually started while
// its observed code state is still current. Reporters persist full logs and
// return the bounded, agent-facing presentation; they never receive not-run
// or platform-inapplicable entries.
type CheckResultReporter interface {
	ReportCheck(context.Context, string, checkexec.Command, checkexec.Result, time.Duration) (CheckPresentation, error)
}

// CheckLifecycleReporter optionally observes the workspace immediately before
// a configured command starts.  The optional boundary keeps the basic check
// runner small while allowing the controller to attribute generator and build
// side effects to the assignment that requested them.
type CheckLifecycleReporter interface {
	BeforeCheck(context.Context, string, checkexec.Command) error
}

// RunRequestedChecks rejects every unknown name before starting a command, then
// runs exactly the supplied configured names in exactly that order. It accepts
// additional checks as well as required checks and intentionally does not sort
// or deduplicate names.
func RunRequestedChecks(ctx context.Context, selection setting.CheckSelection, names []string, runner CheckRunner) (CheckSet, error) {
	return RunRequestedChecksWithReporter(ctx, selection, names, runner, nil)
}

// RunRequestedChecksWithReporter has the same ordering and fail-fast behavior
// as RunRequestedChecks and additionally persists/presents every check that
// starts. A reporter error is returned after the command result is retained;
// callers must not create a durable state event that claims that result.
func RunRequestedChecksWithReporter(ctx context.Context, selection setting.CheckSelection, names []string, runner CheckRunner, reporter CheckResultReporter) (CheckSet, error) {
	for _, name := range names {
		if _, ok := selection.Checks[name]; !ok {
			return CheckSet{Kind: CheckSetRequested}, fmt.Errorf("%w %q", ErrUnknownCheck, name)
		}
	}
	return runCheckSet(ctx, CheckSetRequested, selection, append([]string(nil), names...), runner, reporter)
}

// RunRequiredChecks runs every configured required check in project order. It
// never reuses a prior requested result and it does not accept caller-selected
// names, filters, parameters, or commands.
func RunRequiredChecks(ctx context.Context, selection setting.CheckSelection, runner CheckRunner) (CheckSet, error) {
	return RunRequiredChecksWithReporter(ctx, selection, runner, nil)
}

// RunRequiredChecksWithReporter has the same behavior as RunRequiredChecks
// while publishing a report for every command that starts.
func RunRequiredChecksWithReporter(ctx context.Context, selection setting.CheckSelection, runner CheckRunner, reporter CheckResultReporter) (CheckSet, error) {
	for _, name := range selection.Required {
		if _, ok := selection.Checks[name]; !ok {
			return CheckSet{Kind: CheckSetRequired}, fmt.Errorf("%w %q", ErrUnknownCheck, name)
		}
	}
	return runCheckSet(ctx, CheckSetRequired, selection, append([]string(nil), selection.Required...), runner, reporter)
}

func runCheckSet(ctx context.Context, kind CheckSetKind, selection setting.CheckSelection, names []string, runner CheckRunner, reporter CheckResultReporter) (CheckSet, error) {
	if runner == nil {
		return CheckSet{Kind: kind}, fmt.Errorf("%w: check runner is required", ErrInvalidCheckSet)
	}
	set := CheckSet{Kind: kind, Results: make([]CheckSetResult, len(names))}
	for index, name := range names {
		set.Results[index].Name = name
	}

	for index, name := range names {
		check := selection.Checks[name]
		if !check.Available {
			set.Results[index].Status = CheckNotApplicable
			set.Results[index].Err = fmt.Errorf("configured check %q has no command for this platform", name)
			markNotRun(set.Results[index+1:])
			return set, nil
		}

		command := checkCommand(check)
		if lifecycle, ok := reporter.(CheckLifecycleReporter); ok {
			if err := lifecycle.BeforeCheck(ctx, name, command); err != nil {
				set.Results[index] = CheckSetResult{Name: name, Status: CheckFailed, Command: command, Err: err}
				markNotRun(set.Results[index+1:])
				return set, err
			}
		}
		started := time.Now()
		result, err := runner.RunCheck(ctx, command)
		duration := time.Since(started)
		set.Results[index] = CheckSetResult{
			Name:     name,
			Status:   checkStatus(result, err),
			Command:  command,
			Result:   result,
			Duration: duration,
			Err:      err,
		}
		if reporter != nil {
			persistenceContext, cancelPersistence := context.WithTimeout(context.Background(), checkResultPersistenceTimeout)
			presentation, reportErr := reporter.ReportCheck(persistenceContext, name, command, result, duration)
			cancelPersistence()
			set.Results[index].Presentation = &presentation
			if reportErr != nil {
				set.Results[index].Status = CheckFailed
				set.Results[index].Err = reportErr
				markNotRun(set.Results[index+1:])
				return set, fmt.Errorf("persist check %q result: %w", name, reportErr)
			}
		}
		if set.Results[index].Status != CheckSucceeded {
			markNotRun(set.Results[index+1:])
			return set, nil
		}
	}
	return set, nil
}

func markNotRun(results []CheckSetResult) {
	for index := range results {
		results[index].Status = CheckNotRun
	}
}

func checkCommand(check setting.SelectedCheck) checkexec.Command {
	return checkexec.Command{
		Program: check.Command.Program,
		Args:    append([]string(nil), check.Command.Args...),
		Env:     copyEnvironment(check.Command.Env),
		CWD:     check.CWD,
		Timeout: time.Duration(check.TimeoutSeconds) * time.Second,
	}
}

func copyEnvironment(environment map[string]string) map[string]string {
	if environment == nil {
		return nil
	}
	copy := make(map[string]string, len(environment))
	for name, value := range environment {
		copy[name] = value
	}
	return copy
}

func checkStatus(result checkexec.Result, err error) CheckStatus {
	if err == nil && result.Failure == checkexec.FailureNone && result.ExitCode == 0 {
		return CheckSucceeded
	}
	return CheckFailed
}
