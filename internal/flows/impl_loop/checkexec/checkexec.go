// Package checkexec executes configured checks for the implementation flow.
package checkexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AndrMoiseev/stepan/internal/processjob"
)

// Command is the already-resolved command of one configured check. Program is
// executed directly; Args are never parsed as shell input. CWD must be the
// resolved working directory supplied by the configuration boundary.
type Command struct {
	Program string
	Args    []string
	Env     map[string]string
	CWD     string
	// Timeout bounds this invocation. A zero timeout leaves timeout selection to
	// the caller (the configuration boundary supplies its 600-second default).
	Timeout time.Duration
}

// FailureKind distinguishes the actionable reason an invocation did not
// succeed. In particular, a stopped check is not reported as an ordinary
// non-zero exit from the program that happened to be killed.
type FailureKind string

const (
	FailureNone           FailureKind = ""
	FailureExit           FailureKind = "exit"
	FailureTimeout        FailureKind = "timeout"
	FailureCanceled       FailureKind = "canceled"
	FailureLaunch         FailureKind = "launch"
	FailureInfrastructure FailureKind = "infrastructure"
)

const (
	postTerminationWait         = 2 * time.Second
	postTerminationFallbackWait = 2 * time.Second
)

var (
	ErrExit           = errors.New("check exited unsuccessfully")
	ErrTimeout        = errors.New("check timed out")
	ErrCanceled       = errors.New("check canceled")
	ErrLaunch         = errors.New("check launch failed")
	ErrInfrastructure = errors.New("check process supervision failed")
)

// Result is the immediate outcome of one command invocation. It deliberately
// keeps complete process output in memory; assigning logs to run storage and
// presenting bounded diagnostics are responsibilities of a later boundary.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Failure  FailureKind
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
// and output in Result.
func Run(command Command) (Result, error) {
	return RunContext(context.Background(), command)
}

// RunContext executes one configured command and stops its complete contained
// process tree on timeout or caller cancellation. Windows uses a Job Object and
// macOS uses a dedicated process group through processjob. Result retains all
// stdout and stderr observed before termination.
func RunContext(ctx context.Context, command Command) (Result, error) {
	if ctx == nil {
		return Result{ExitCode: -1, Failure: FailureInfrastructure}, checkError(command, FailureInfrastructure, ErrInfrastructure, errors.New("check context is required"))
	}
	if command.Program == "" {
		return Result{ExitCode: -1, Failure: FailureLaunch}, checkError(command, FailureLaunch, ErrLaunch, errors.New("check program is required"))
	}
	if command.CWD == "" {
		return Result{ExitCode: -1, Failure: FailureLaunch}, checkError(command, FailureLaunch, ErrLaunch, errors.New("check working directory is required"))
	}

	child := exec.Command(command.Program, command.Args...)
	child.Dir = command.CWD
	child.Env = commandEnvironment(os.Environ(), command.Env)
	// A nil Stdin causes os/exec to connect the child to the null device, so
	// reads see EOF and the command cannot consume Stepan's terminal input.
	child.Stdin = nil

	stdoutPipe, err := child.StdoutPipe()
	if err != nil {
		return Result{ExitCode: -1, Failure: FailureInfrastructure}, checkError(command, FailureInfrastructure, ErrInfrastructure, fmt.Errorf("create stdout pipe: %w", err))
	}
	stderrPipe, err := child.StderrPipe()
	if err != nil {
		_ = stdoutPipe.Close()
		return Result{ExitCode: -1, Failure: FailureInfrastructure}, checkError(command, FailureInfrastructure, ErrInfrastructure, fmt.Errorf("create stderr pipe: %w", err))
	}
	var stdout, stderr outputBuffer

	job, err := processjob.New()
	if err != nil {
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return resultFrom(child, stdout.Bytes(), stderr.Bytes(), FailureInfrastructure), checkError(command, FailureInfrastructure, ErrInfrastructure, fmt.Errorf("create process supervisor: %w", err))
	}
	if err := job.Prepare(child); err != nil {
		_ = job.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return resultFrom(child, stdout.Bytes(), stderr.Bytes(), FailureInfrastructure), checkError(command, FailureInfrastructure, ErrInfrastructure, fmt.Errorf("prepare process supervisor: %w", err))
	}
	if err := child.Start(); err != nil {
		_ = job.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return resultFrom(child, stdout.Bytes(), stderr.Bytes(), FailureLaunch), checkError(command, FailureLaunch, ErrLaunch, err)
	}
	stdoutDone := copyOutput(&stdout, stdoutPipe)
	stderrDone := copyOutput(&stderr, stderrPipe)
	if err := job.Assign(child.Process); err != nil {
		_ = job.Close()
		killAndWait(child, stdoutPipe, stderrPipe, stdoutDone, stderrDone)
		return resultFrom(child, stdout.Bytes(), stderr.Bytes(), FailureInfrastructure), checkError(command, FailureInfrastructure, ErrInfrastructure, fmt.Errorf("assign process supervisor: %w", err))
	}

	runContext, stopTimeout := context.WithCancel(ctx)
	if command.Timeout > 0 {
		runContext, stopTimeout = context.WithTimeout(ctx, command.Timeout)
	}
	defer stopTimeout()

	wait := waitForCommand(child, stdoutDone, stderrDone)

	select {
	case err := <-wait:
		closeErr := job.Close()
		return completed(command, child, stdout.Bytes(), stderr.Bytes(), err, closeErr)
	case <-runContext.Done():
		// Prefer a completed command when its completion raced with cancellation.
		select {
		case err := <-wait:
			closeErr := job.Close()
			return completed(command, child, stdout.Bytes(), stderr.Bytes(), err, closeErr)
		default:
		}
		closeErr := job.Close()
		waitErr := waitAfterTermination(child, stdoutPipe, stderrPipe, wait)
		result := resultFrom(child, stdout.Bytes(), stderr.Bytes(), cancellationFailure(ctx, runContext))
		if closeErr != nil {
			return result, checkError(command, FailureInfrastructure, ErrInfrastructure, fmt.Errorf("terminate process tree: %w", closeErr))
		}
		if result.Failure == FailureTimeout {
			return result, checkError(command, FailureTimeout, ErrTimeout, errors.Join(context.DeadlineExceeded, waitErr))
		}
		return result, checkError(command, FailureCanceled, ErrCanceled, errors.Join(context.Canceled, waitErr))
	}
}

func completed(command Command, child *exec.Cmd, stdout, stderr []byte, waitErr, closeErr error) (Result, error) {
	result := resultFrom(child, stdout, stderr, FailureNone)
	if closeErr != nil {
		result.Failure = FailureInfrastructure
		return result, checkError(command, FailureInfrastructure, ErrInfrastructure, fmt.Errorf("close process supervisor: %w", closeErr))
	}
	if waitErr != nil {
		result.Failure = FailureExit
		return result, checkError(command, FailureExit, ErrExit, waitErr)
	}
	return result, nil
}

func resultFrom(child *exec.Cmd, stdout, stderr []byte, failure FailureKind) Result {
	result := Result{
		ExitCode: -1,
		Stdout:   append([]byte(nil), stdout...),
		Stderr:   append([]byte(nil), stderr...),
		Failure:  failure,
	}
	if child.ProcessState != nil {
		result.ExitCode = child.ProcessState.ExitCode()
	}
	return result
}

func cancellationFailure(parent, combined context.Context) FailureKind {
	if errors.Is(parent.Err(), context.Canceled) {
		return FailureCanceled
	}
	if errors.Is(combined.Err(), context.DeadlineExceeded) || errors.Is(parent.Err(), context.DeadlineExceeded) {
		return FailureTimeout
	}
	return FailureCanceled
}

func copyOutput(destination io.Writer, source io.ReadCloser) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(destination, source)
		close(done)
	}()
	return done
}

func waitForCommand(command *exec.Cmd, stdoutDone, stderrDone <-chan struct{}) <-chan error {
	done := make(chan error, 1)
	go func() {
		<-stdoutDone
		<-stderrDone
		done <- command.Wait()
	}()
	return done
}

func waitAfterTermination(command *exec.Cmd, stdout, stderr io.ReadCloser, wait <-chan error) error {
	timer := time.NewTimer(postTerminationWait)
	defer timer.Stop()
	select {
	case err := <-wait:
		return err
	case <-timer.C:
		// A descendant can retain the inherited pipe after its group leader has
		// exited. Closing our read sides unblocks their copy goroutines, then a
		// direct root kill is a final reaping fallback if supervision failed.
		_ = stdout.Close()
		_ = stderr.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		fallback := time.NewTimer(postTerminationFallbackWait)
		defer fallback.Stop()
		select {
		case err := <-wait:
			return err
		case <-fallback.C:
			// The waiter remains responsible for calling Cmd.Wait and eventually
			// reaping the root. Return now rather than holding the controller
			// forever on a broken pipe or unreapable child.
			return errors.New("check did not exit after forced termination")
		}
	}
}

func killAndWait(command *exec.Cmd, stdout, stderr io.ReadCloser, stdoutDone, stderrDone <-chan struct{}) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = stdout.Close()
	_ = stderr.Close()
	<-stdoutDone
	<-stderrDone
	_ = command.Wait()
}

func checkError(command Command, kind FailureKind, sentinel, cause error) error {
	return fmt.Errorf("run check %q (%s): %w", command.Program, kind, errors.Join(sentinel, cause))
}

type outputBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *outputBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *outputBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.buffer.Bytes()...)
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
