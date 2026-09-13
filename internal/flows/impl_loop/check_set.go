package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
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
	Name    string
	Status  CheckStatus
	Command checkexec.Command
	Result  checkexec.Result
	Err     error
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

// RunRequestedChecks rejects every unknown name before starting a command, then
// runs exactly the supplied configured names in exactly that order. It accepts
// additional checks as well as required checks and intentionally does not sort
// or deduplicate names.
func RunRequestedChecks(ctx context.Context, selection implementationconfig.CheckSelection, names []string, runner CheckRunner) (CheckSet, error) {
	for _, name := range names {
		if _, ok := selection.Checks[name]; !ok {
			return CheckSet{Kind: CheckSetRequested}, fmt.Errorf("%w %q", ErrUnknownCheck, name)
		}
	}
	return runCheckSet(ctx, CheckSetRequested, selection, append([]string(nil), names...), runner)
}

// RunRequiredChecks runs every configured required check in project order. It
// never reuses a prior requested result and it does not accept caller-selected
// names, filters, parameters, or commands.
func RunRequiredChecks(ctx context.Context, selection implementationconfig.CheckSelection, runner CheckRunner) (CheckSet, error) {
	for _, name := range selection.Required {
		if _, ok := selection.Checks[name]; !ok {
			return CheckSet{Kind: CheckSetRequired}, fmt.Errorf("%w %q", ErrUnknownCheck, name)
		}
	}
	return runCheckSet(ctx, CheckSetRequired, selection, append([]string(nil), selection.Required...), runner)
}

func runCheckSet(ctx context.Context, kind CheckSetKind, selection implementationconfig.CheckSelection, names []string, runner CheckRunner) (CheckSet, error) {
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
		result, err := runner.RunCheck(ctx, command)
		set.Results[index] = CheckSetResult{
			Name:    name,
			Status:  checkStatus(result, err),
			Command: command,
			Result:  result,
			Err:     err,
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

func checkCommand(check implementationconfig.SelectedCheck) checkexec.Command {
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
