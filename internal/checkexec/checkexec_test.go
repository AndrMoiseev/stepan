package checkexec

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
}

func TestRunRejectsUnresolvedCommand(t *testing.T) {
	for _, command := range []Command{{CWD: t.TempDir()}, {Program: os.Args[0]}} {
		if _, err := Run(command); err == nil {
			t.Fatalf("Run(%#v) succeeded", command)
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
	for _, argument := range args[separator+1:] {
		if argument == "--exit=23" {
			return 23
		}
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
