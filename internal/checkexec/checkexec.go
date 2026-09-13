// Package checkexec executes configured checks for the implementation flow.
package checkexec

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

// Command is the already-resolved command of one configured check. Program is
// executed directly; Args are never parsed as shell input. CWD must be the
// resolved working directory supplied by the configuration boundary.
type Command struct {
	Program string
	Args    []string
	Env     map[string]string
	CWD     string
}

// Result is the immediate outcome of one command invocation. It deliberately
// keeps complete process output in memory; assigning logs to run storage and
// presenting bounded diagnostics are responsibilities of a later boundary.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Run executes one configured command directly and waits for it to finish.
//
// The child inherits the environment visible to Stepan when Run starts, with
// Command.Env overriding values only in that child. Every call builds a fresh
// environment, so cross-compilation variables cannot leak into a subsequent
// check or into the Stepan process. Its standard input is the null device and
// stdout and stderr are pipes backed by non-terminal buffers.
//
// A non-zero child exit is returned as an error while preserving its exit code
// and output in Result. Timeout, cancellation, and process-tree termination
// intentionally belong to the later process-management task.
func Run(command Command) (Result, error) {
	if command.Program == "" {
		return Result{}, errors.New("check program is required")
	}
	if command.CWD == "" {
		return Result{}, errors.New("check working directory is required")
	}

	child := exec.Command(command.Program, command.Args...)
	child.Dir = command.CWD
	child.Env = commandEnvironment(os.Environ(), command.Env)
	// A nil Stdin causes os/exec to connect the child to the null device, so
	// reads see EOF and the command cannot consume Stepan's terminal input.
	child.Stdin = nil

	var stdout, stderr bytes.Buffer
	child.Stdout = &stdout
	child.Stderr = &stderr
	err := child.Run()
	result := Result{
		Stdout:   append([]byte(nil), stdout.Bytes()...),
		Stderr:   append([]byte(nil), stderr.Bytes()...),
		ExitCode: -1,
	}
	if child.ProcessState != nil {
		result.ExitCode = child.ProcessState.ExitCode()
	}
	if err != nil {
		return result, fmt.Errorf("run check %q: %w", command.Program, err)
	}
	return result, nil
}

func commandEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	names := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		key := environmentKey(name)
		values[key] = value
		names[key] = name
	}
	for name, value := range overrides {
		if name == "" {
			continue
		}
		key := environmentKey(name)
		values[key] = value
		names[key] = name
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, names[key]+"="+values[key])
	}
	return environment
}

func environmentKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
