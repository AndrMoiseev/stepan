package impl_loop

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/checkexec"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

func TestRunRequestedChecksRunsAdditionalNarrowCheckOnly(t *testing.T) {
	selection := testCheckSelection([]string{"lint", "test_all"})
	runner := &recordingCheckRunner{}

	set, err := RunRequestedChecks(context.Background(), selection, []string{"test_auth"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, runner, "test_auth")
	assertResultStatuses(t, set, CheckSucceeded)
	command := runner.commands[0]
	if command.CWD != "repository-root" || command.Timeout != 600*time.Second || !reflect.DeepEqual(command.Args, []string{"entire configured check"}) || !reflect.DeepEqual(command.Env, map[string]string{"CHECK": "test_auth"}) {
		t.Fatalf("configured command = %#v", command)
	}
	if set.Kind != CheckSetRequested || !set.Succeeded() {
		t.Fatalf("requested set = %#v", set)
	}
}

func TestRunRequestedChecksPreservesMixedCallerOrder(t *testing.T) {
	selection := testCheckSelection([]string{"lint", "test_all"})
	runner := &recordingCheckRunner{}

	set, err := RunRequestedChecks(context.Background(), selection, []string{"test_auth", "lint", "test_all"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, runner, "test_auth", "lint", "test_all")
	assertResultNames(t, set, "test_auth", "lint", "test_all")
	assertResultStatuses(t, set, CheckSucceeded, CheckSucceeded, CheckSucceeded)
}

func TestRunRequestedChecksRejectsUnknownNameBeforeExecution(t *testing.T) {
	selection := testCheckSelection([]string{"lint", "test_all"})
	runner := &recordingCheckRunner{}

	set, err := RunRequestedChecks(context.Background(), selection, []string{"lint", "test_all -run Auth"}, runner)
	if !errors.Is(err, ErrUnknownCheck) {
		t.Fatalf("error = %v, want unknown check", err)
	}
	if len(set.Results) != 0 {
		t.Fatalf("rejected set results = %#v, want none", set.Results)
	}
	assertRunOrder(t, runner)
}

func TestRunRequiredChecksUsesCompleteProjectOrderAfterRequestedSuccess(t *testing.T) {
	selection := testCheckSelection([]string{"lint", "test_all"})
	runner := &recordingCheckRunner{}

	if _, err := RunRequestedChecks(context.Background(), selection, []string{"test_auth", "lint"}, runner); err != nil {
		t.Fatal(err)
	}
	runner.commands = nil
	set, err := RunRequiredChecks(context.Background(), selection, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, runner, "lint", "test_all")
	assertResultNames(t, set, "lint", "test_all")
	assertResultStatuses(t, set, CheckSucceeded, CheckSucceeded)
	if set.Kind != CheckSetRequired || !set.Succeeded() {
		t.Fatalf("required set = %#v", set)
	}
}

func TestRunRequestedChecksMarksPlatformInapplicableCheckAndRemainderNotRun(t *testing.T) {
	selection := testCheckSelection([]string{"lint"})
	selection.Checks["mac_only"] = setting.SelectedCheck{Name: "mac_only", Kind: setting.CheckKindTests}
	runner := &recordingCheckRunner{}

	set, err := RunRequestedChecks(context.Background(), selection, []string{"mac_only", "lint"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, runner)
	assertResultNames(t, set, "mac_only", "lint")
	assertResultStatuses(t, set, CheckNotApplicable, CheckNotRun)
	if set.Succeeded() || set.Results[0].Err == nil {
		t.Fatalf("inapplicable set = %#v", set)
	}
}

func TestRunRequiredChecksTreatsPlatformInapplicableCheckAsFailure(t *testing.T) {
	selection := testCheckSelection([]string{"mac_only", "lint"})
	selection.Checks["mac_only"] = setting.SelectedCheck{Name: "mac_only", Kind: setting.CheckKindTests}
	runner := &recordingCheckRunner{}

	set, err := RunRequiredChecks(context.Background(), selection, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, runner)
	assertResultNames(t, set, "mac_only", "lint")
	assertResultStatuses(t, set, CheckNotApplicable, CheckNotRun)
	if set.Succeeded() || set.Results[0].Err == nil {
		t.Fatalf("required inapplicable set = %#v", set)
	}
}

func TestRunRequiredChecksStopsOnFirstFailureAndMarksRemainingNotRun(t *testing.T) {
	selection := testCheckSelection([]string{"lint", "test_all", "build"})
	runner := &recordingCheckRunner{fail: map[string]error{"test_all": errors.New("test failure")}}

	set, err := RunRequiredChecks(context.Background(), selection, runner)
	if err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, runner, "lint", "test_all")
	assertResultNames(t, set, "lint", "test_all", "build")
	assertResultStatuses(t, set, CheckSucceeded, CheckFailed, CheckNotRun)
	if set.Succeeded() || set.Results[1].Err == nil || string(set.Results[1].Result.Stderr) != "test_all failed" {
		t.Fatalf("failed set = %#v", set)
	}
}

type recordingCheckRunner struct {
	commands []checkexec.Command
	fail     map[string]error
}

func (runner *recordingCheckRunner) RunCheck(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
	runner.commands = append(runner.commands, command)
	if err := runner.fail[command.Program]; err != nil {
		return checkexec.Result{ExitCode: 1, Failure: checkexec.FailureExit, Stderr: []byte(command.Program + " failed")}, err
	}
	return checkexec.Result{ExitCode: 0}, nil
}

func testCheckSelection(required []string) setting.CheckSelection {
	checks := make(map[string]setting.SelectedCheck, 4)
	for _, name := range []string{"lint", "test_all", "test_auth", "build"} {
		checks[name] = setting.SelectedCheck{
			Name:           name,
			Kind:           setting.CheckKindTests,
			Command:        setting.CheckCommand{Program: name, Args: []string{"entire configured check"}, Env: map[string]string{"CHECK": name}},
			CWD:            "repository-root",
			TimeoutSeconds: 600,
			Available:      true,
		}
	}
	return setting.CheckSelection{Required: append([]string(nil), required...), Checks: checks}
}

func assertRunOrder(t *testing.T, runner *recordingCheckRunner, want ...string) {
	t.Helper()
	got := make([]string, len(runner.commands))
	for index, command := range runner.commands {
		got[index] = command.Program
	}
	if len(got) != len(want) {
		t.Fatalf("runner order = %#v, want %#v", got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("runner order = %#v, want %#v", got, want)
		}
	}
}

func assertResultNames(t *testing.T, set CheckSet, want ...string) {
	t.Helper()
	got := make([]string, len(set.Results))
	for index, result := range set.Results {
		got[index] = result.Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result names = %#v, want %#v", got, want)
	}
}

func assertResultStatuses(t *testing.T, set CheckSet, want ...CheckStatus) {
	t.Helper()
	got := make([]CheckStatus, len(set.Results))
	for index, result := range set.Results {
		got[index] = result.Status
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result statuses = %#v, want %#v", got, want)
	}
}
