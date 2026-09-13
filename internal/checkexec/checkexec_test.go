package checkexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperEnvironment = "STEPAN_CHECKEXEC_HELPER"

type helperReport struct {
	Args             []string          `json:"args"`
	CWD              string            `json:"cwd"`
	EOF              bool              `json:"eof"`
	StdoutCharDevice bool              `json:"stdout_char_device"`
	StderrCharDevice bool              `json:"stderr_char_device"`
	Env              map[string]string `json:"env"`
}

func TestMain(main *testing.M) {
	if os.Getenv(helperEnvironment) == "1" {
		os.Exit(runHelperProcess())
	}
	os.Exit(main.Run())
}

func TestRunDirectlyExecutesArgumentsInConfiguredDirectoryAndIsolatesEnvironment(t *testing.T) {
	t.Setenv("GOOS", "parent-goos")
	t.Setenv("GOARCH", "parent-goarch")
	workingDirectory := filepath.Join(t.TempDir(), "working directory")
	if err := os.Mkdir(workingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}

	firstArguments := []string{"argument with spaces", "semi; colon", "$(not a shell substitution)", "redirection > remains literal"}
	first, err := Run(helperCommand(workingDirectory, firstArguments, map[string]string{
		"GOOS":                     "darwin",
		"GOARCH":                   "arm64",
		"STEPAN_CHECKEXEC_PRIVATE": "present only for first command",
	}))
	if err != nil {
		t.Fatalf("first command: %v\nstderr: %s", err, first.Stderr)
	}
	if first.ExitCode != 0 {
		t.Fatalf("first exit code = %d", first.ExitCode)
	}
	firstReport := decodeReport(t, first.Stdout)
	if !reflect.DeepEqual(firstReport.Args, firstArguments) {
		t.Fatalf("first arguments = %#v, want %#v", firstReport.Args, firstArguments)
	}
	if firstReport.CWD != workingDirectory {
		t.Fatalf("first cwd = %q, want %q", firstReport.CWD, workingDirectory)
	}
	if !firstReport.EOF {
		t.Fatal("first command did not receive EOF on stdin")
	}
	if firstReport.StdoutCharDevice || firstReport.StderrCharDevice {
		t.Fatalf("first command inherited a terminal stream: %#v", firstReport)
	}
	if got := firstReport.Env["STEPAN_CHECKEXEC_PRIVATE"]; got != "present only for first command" {
		t.Fatalf("first private environment = %q", got)
	}
	if got := firstReport.Env["GOOS"]; got != "darwin" {
		t.Fatalf("first GOOS = %q", got)
	}
	if got := firstReport.Env["GOARCH"]; got != "arm64" {
		t.Fatalf("first GOARCH = %q", got)
	}

	second, err := Run(helperCommand(workingDirectory, nil, nil))
	if err != nil {
		t.Fatalf("second command: %v\nstderr: %s", err, second.Stderr)
	}
	secondReport := decodeReport(t, second.Stdout)
	if !secondReport.EOF {
		t.Fatal("second command did not receive EOF on stdin")
	}
	if secondReport.StdoutCharDevice || secondReport.StderrCharDevice {
		t.Fatalf("second command inherited a terminal stream: %#v", secondReport)
	}
	if got := secondReport.Env["STEPAN_CHECKEXEC_PRIVATE"]; got != "" {
		t.Fatalf("first command environment leaked to second command: %q", got)
	}
	if got := secondReport.Env["GOOS"]; got != "parent-goos" {
		t.Fatalf("GOOS leaked to second command: %q", got)
	}
	if got := secondReport.Env["GOARCH"]; got != "parent-goarch" {
		t.Fatalf("GOARCH leaked to second command: %q", got)
	}
	if got := os.Getenv("GOOS"); got != "parent-goos" {
		t.Fatalf("Run mutated parent GOOS: %q", got)
	}
	if got := os.Getenv("GOARCH"); got != "parent-goarch" {
		t.Fatalf("Run mutated parent GOARCH: %q", got)
	}
}

func TestRunReportsNonZeroExit(t *testing.T) {
	result, err := Run(Command{
		Program: os.Args[0],
		Args:    []string{"--", "--exit=23"},
		Env:     map[string]string{helperEnvironment: "1"},
		CWD:     t.TempDir(),
	})
	if err == nil {
		t.Fatal("non-zero command succeeded")
	}
	if result.ExitCode != 23 {
		t.Fatalf("exit code = %d, want 23", result.ExitCode)
	}
	if result.Failure != FailureExit || !errors.Is(err, ErrExit) {
		t.Fatalf("failure = %q, error = %v", result.Failure, err)
	}
}

func TestRunRejectsUnresolvedCommand(t *testing.T) {
	for _, command := range []Command{{CWD: t.TempDir()}, {Program: os.Args[0]}} {
		result, err := Run(command)
		if err == nil {
			t.Fatalf("Run(%#v) succeeded", command)
		}
		if result.Failure != FailureLaunch || !errors.Is(err, ErrLaunch) {
			t.Fatalf("Run(%#v) failure = %q, error = %v", command, result.Failure, err)
		}
	}
}

func TestRunContextTimeoutTerminatesProcessTreeAndRetainsDiagnostics(t *testing.T) {
	requireTreeTerminationSupport(t)
	parentReady, childReady := readinessPaths(t)
	result, err := RunContext(context.Background(), hangingHelperCommand(t, parentReady, childReady, time.Second))
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v\nstdout: %s\nstderr: %s", err, result.Stdout, result.Stderr)
	}
	if result.Failure != FailureTimeout {
		t.Fatalf("failure = %q, want timeout", result.Failure)
	}
	assertTerminationDiagnostics(t, result)
	assertHelperTreeStopped(t, parentReady, childReady)
}

func TestRunContextCancellationTerminatesProcessTreeAndRetainsDiagnostics(t *testing.T) {
	requireTreeTerminationSupport(t)
	parentReady, childReady := readinessPaths(t)
	runContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultChannel := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := RunContext(runContext, hangingHelperCommand(t, parentReady, childReady, 10*time.Second))
		resultChannel <- struct {
			result Result
			err    error
		}{result, err}
	}()
	awaitFile(t, parentReady)
	cancel()
	select {
	case outcome := <-resultChannel:
		if !errors.Is(outcome.err, ErrCanceled) || !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("cancellation error = %v\nstdout: %s\nstderr: %s", outcome.err, outcome.result.Stdout, outcome.result.Stderr)
		}
		if outcome.result.Failure != FailureCanceled {
			t.Fatalf("failure = %q, want canceled", outcome.result.Failure)
		}
		assertTerminationDiagnostics(t, outcome.result)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled check did not return")
	}
	assertHelperTreeStopped(t, parentReady, childReady)
}

func requireTreeTerminationSupport(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("process-tree containment is supported by this check on Windows and macOS")
	}
}

func readinessPaths(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	return filepath.Join(directory, "parent-ready"), filepath.Join(directory, "child-ready")
}

func hangingHelperCommand(t *testing.T, parentReady, childReady string, timeout time.Duration) Command {
	t.Helper()
	return Command{
		Program: os.Args[0],
		Args: []string{
			"--",
			"--hang-parent-ready=" + parentReady,
			"--hang-child-ready=" + childReady,
		},
		Env:     map[string]string{helperEnvironment: "1"},
		CWD:     t.TempDir(),
		Timeout: timeout,
	}
}

func assertTerminationDiagnostics(t *testing.T, result Result) {
	t.Helper()
	if !bytes.Contains(result.Stdout, []byte("stdout before interruption")) {
		t.Fatalf("stdout lost pre-termination diagnostic: %q", result.Stdout)
	}
	if !bytes.Contains(result.Stderr, []byte("stderr before interruption")) {
		t.Fatalf("stderr lost pre-termination diagnostic: %q", result.Stderr)
	}
}

func assertHelperTreeStopped(t *testing.T, parentReady, childReady string) {
	t.Helper()
	awaitFile(t, childReady)
	assertProcessStopped(t, awaitHelperPID(t, parentReady))
}

func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", path)
		case <-poll.C:
		}
	}
}

func awaitHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			value := strings.TrimSpace(string(data))
			if value != "" {
				pid, err := strconv.Atoi(value)
				if err != nil || pid <= 1 {
					t.Fatalf("child PID %q: %v", data, err)
				}
				return pid
			}
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for complete child PID at %s", path)
		case <-poll.C:
		}
	}
}

func helperCommand(cwd string, args []string, environment map[string]string) Command {
	commandArgs := []string{"--"}
	commandArgs = append(commandArgs, args...)
	environment = copyEnvironment(environment)
	environment[helperEnvironment] = "1"
	return Command{Program: os.Args[0], Args: commandArgs, Env: environment, CWD: cwd}
}

func copyEnvironment(environment map[string]string) map[string]string {
	copy := make(map[string]string, len(environment)+1)
	for key, value := range environment {
		copy[key] = value
	}
	return copy
}

func decodeReport(t *testing.T, stdout []byte) helperReport {
	t.Helper()
	var report helperReport
	if err := json.Unmarshal(stdout, &report); err != nil {
		t.Fatalf("decode helper output %q: %v", stdout, err)
	}
	return report
}

func runHelperProcess() int {
	args := os.Args
	separator := -1
	for index, value := range args {
		if value == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		return 2
	}
	arguments := args[separator+1:]
	for _, argument := range arguments {
		if argument == "--exit=23" {
			return 23
		}
	}
	if parentReady, childReady := argumentValue(arguments, "--exit-parent-ready="), argumentValue(arguments, "--hang-child-ready="); parentReady != "" && childReady != "" {
		if _, err := os.Stdout.WriteString("stdout before leader exit\n"); err != nil {
			return 27
		}
		if _, err := os.Stderr.WriteString("stderr before leader exit\n"); err != nil {
			return 28
		}
		child := exec.Command(os.Args[0], "--", "--hang-child-ready="+childReady)
		child.Env = append(os.Environ(), helperEnvironment+"=1")
		// The regression needs the descendant to retain the check's output
		// pipes after this helper (the process-group leader) exits.
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			return 29
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		poll := time.NewTicker(10 * time.Millisecond)
		defer poll.Stop()
		for {
			if _, err := os.Stat(childReady); err == nil {
				if err := publishHelperFile(parentReady, []byte(strconv.Itoa(child.Process.Pid))); err != nil {
					return 30
				}
				if leaderExiting := argumentValue(arguments, "--leader-exiting="); leaderExiting != "" {
					if err := publishHelperFile(leaderExiting, []byte("exiting")); err != nil {
						return 33
					}
					// The cancellation regression observes this final hand-off and
					// then waits for a still-live descendant; nothing in this helper
					// can run after the marker is visible.
					os.Exit(0)
				}
				return 0
			} else if !os.IsNotExist(err) {
				return 31
			}
			select {
			case <-deadline.C:
				return 32
			case <-poll.C:
			}
		}
	}
	if parentReady, childReady := argumentValue(arguments, "--hang-parent-ready="), argumentValue(arguments, "--hang-child-ready="); parentReady != "" && childReady != "" {
		if _, err := os.Stdout.WriteString("stdout before interruption\n"); err != nil {
			return 21
		}
		if _, err := os.Stderr.WriteString("stderr before interruption\n"); err != nil {
			return 22
		}
		child := exec.Command(os.Args[0], "--", "--hang-child-ready="+childReady)
		child.Env = append(os.Environ(), helperEnvironment+"=1")
		if err := child.Start(); err != nil {
			return 23
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		poll := time.NewTicker(10 * time.Millisecond)
		defer poll.Stop()
		for {
			if _, err := os.Stat(childReady); err == nil {
				if err := publishHelperFile(parentReady, []byte(strconv.Itoa(child.Process.Pid))); err != nil {
					return 24
				}
				time.Sleep(24 * time.Hour)
				return 0
			} else if !os.IsNotExist(err) {
				return 25
			}
			select {
			case <-deadline.C:
				return 26
			case <-poll.C:
			}
		}
	}
	if ready := argumentValue(arguments, "--hang-child-ready="); ready != "" {
		if err := publishHelperFile(ready, []byte("ready")); err != nil {
			return 20
		}
		time.Sleep(24 * time.Hour)
		return 0
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 3
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return 4
	}
	stdoutInfo, err := os.Stdout.Stat()
	if err != nil {
		return 5
	}
	stderrInfo, err := os.Stderr.Stat()
	if err != nil {
		return 6
	}
	report := helperReport{
		Args:             append([]string(nil), args[separator+1:]...),
		CWD:              workingDirectory,
		EOF:              len(stdin) == 0,
		StdoutCharDevice: stdoutInfo.Mode()&os.ModeCharDevice != 0,
		StderrCharDevice: stderrInfo.Mode()&os.ModeCharDevice != 0,
		Env: map[string]string{
			"GOOS":                     os.Getenv("GOOS"),
			"GOARCH":                   os.Getenv("GOARCH"),
			"STEPAN_CHECKEXEC_PRIVATE": os.Getenv("STEPAN_CHECKEXEC_PRIVATE"),
		},
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		return 7
	}
	return 0
}

func argumentValue(arguments []string, prefix string) string {
	for _, argument := range arguments {
		if value, found := strings.CutPrefix(argument, prefix); found {
			return value
		}
	}
	return ""
}

func publishHelperFile(path string, data []byte) (err error) {
	temporary := path + ".tmp-" + strconv.Itoa(os.Getpid())
	defer func() {
		if err != nil {
			_ = os.Remove(temporary)
		}
	}()
	if err = os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
