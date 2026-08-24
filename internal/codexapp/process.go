package codexapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/codexexec"
	"github.com/AndrMoiseev/stepan/internal/platformsupport"
	"github.com/AndrMoiseev/stepan/internal/processjob"
)

const (
	SupportedCodexVersion = "0.147.0"
	maxDiagnosticBytes    = 64 << 10
)

var appServerArgs = []string{"app-server", "--stdio", "--strict-config", "-c", `approvals_reviewer="user"`}

// Process owns one contained Codex App Server and its stdio pipes.
type Process struct {
	executable string
	workspace  string
	stderrCopy io.Writer

	mu         sync.Mutex
	started    bool
	closed     bool
	startErr   error
	command    *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.ReadCloser
	job        *processjob.Job
	stderrDone chan error
	diagnostic limitedBuffer

	waitOnce  sync.Once
	waitDone  chan struct{}
	waitErr   error
	exitCode  *int
	closeOnce sync.Once
	closeErr  error
}

func NewProcess(executable, workspace string) *Process {
	return newProcess(executable, workspace, nil)
}

func newProcess(executable, workspace string, stderrCopy io.Writer) *Process {
	return &Process{executable: executable, workspace: workspace, stderrCopy: stderrCopy, waitDone: make(chan struct{})}
}

func (process *Process) Start() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.closed {
		return errors.New("App Server process is closed")
	}
	if process.started || process.startErr != nil {
		if process.startErr != nil {
			return process.startErr
		}
		return errors.New("App Server process already started")
	}

	executable, err := preflight(process.executable, process.workspace)
	if err != nil {
		process.startErr = err
		return err
	}
	command := exec.Command(executable, appServerArgs...)
	command.Dir = process.workspace
	stdin, err := command.StdinPipe()
	if err != nil {
		process.startErr = err
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		process.startErr = err
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		process.startErr = err
		return err
	}
	job, err := processjob.New()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		process.startErr = fmt.Errorf("create App Server job: %w", err)
		return process.startErr
	}

	process.command, process.stdin, process.stdout, process.stderr, process.job = command, stdin, stdout, stderr, job
	if err := job.Prepare(command); err != nil {
		process.startErr = fmt.Errorf("prepare App Server containment: %w", err)
		process.closePartial()
		return process.startErr
	}
	if err := command.Start(); err != nil {
		process.startErr = fmt.Errorf("start App Server: %w", err)
		process.closePartial()
		return process.startErr
	}
	process.stderrDone = make(chan error, 1)
	go func() {
		writer := io.Writer(&process.diagnostic)
		if process.stderrCopy != nil {
			writer = io.MultiWriter(writer, process.stderrCopy)
		}
		_, err := io.Copy(writer, stderr)
		process.stderrDone <- err
	}()
	if err := job.Assign(command.Process); err != nil {
		process.startErr = fmt.Errorf("contain App Server: %w", err)
		process.closePartial()
		return process.startErr
	}
	process.started = true
	return nil
}

func preflight(executable, workspace string) (string, error) {
	if err := platformsupport.Validate(runtime.GOOS, runtime.GOARCH); err != nil {
		return "", err
	}
	executable, err := resolveExecutable(executable)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(workspace) {
		return "", errors.New("workspace must be absolute")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return "", errors.New("workspace must be an existing directory")
	}
	version, err := codexexec.Version(context.Background(), executable)
	if err != nil {
		return "", fmt.Errorf("read Codex version: %w", err)
	}
	if version != SupportedCodexVersion {
		return "", fmt.Errorf("unsupported codex-cli version %q: require %s", version, SupportedCodexVersion)
	}
	return executable, nil
}

func (process *Process) Stdin() io.WriteCloser {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.stdin
}

func (process *Process) Stdout() io.ReadCloser {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.stdout
}

func (process *Process) Diagnostic() string { return process.diagnostic.String() }

func (process *Process) ExitCode() *int {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.exitCode == nil {
		return nil
	}
	code := *process.exitCode
	return &code
}

func (process *Process) Wait() error {
	process.mu.Lock()
	started, command := process.started, process.command
	process.mu.Unlock()
	if !started {
		return errors.New("App Server process is not started")
	}
	process.waitOnce.Do(func() {
		process.waitErr = command.Wait()
		if command.ProcessState != nil {
			code := command.ProcessState.ExitCode()
			process.mu.Lock()
			process.exitCode = &code
			process.mu.Unlock()
		}
		if process.stderrDone != nil {
			if err := <-process.stderrDone; process.waitErr == nil {
				process.waitErr = err
			}
		}
		close(process.waitDone)
	})
	<-process.waitDone
	return process.waitErr
}

func (process *Process) Close() error {
	process.closeOnce.Do(func() {
		process.mu.Lock()
		started := process.started
		process.closed = true
		process.mu.Unlock()
		if !started {
			return
		}
		process.closeErr = errors.Join(closeUnlessClosed(process.job), closeUnlessClosed(process.stdin), closeUnlessClosed(process.stdout))
		_ = process.Wait()
		process.closeErr = errors.Join(process.closeErr, closeUnlessClosed(process.stderr))
	})
	return process.closeErr
}

func (process *Process) closePartial() {
	_ = closeUnlessClosed(process.job)
	_ = closeUnlessClosed(process.stdin)
	_ = closeUnlessClosed(process.stdout)
	if process.command != nil && process.command.Process != nil {
		_ = process.command.Process.Kill()
		_ = process.command.Wait()
	}
	if process.stderrDone != nil {
		<-process.stderrDone
	}
	_ = closeUnlessClosed(process.stderr)
}

type closer interface{ Close() error }

func closeUnlessClosed(value closer) error {
	if value == nil {
		return nil
	}
	err := value.Close()
	if errors.Is(err, os.ErrClosed) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

type limitedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := maxDiagnosticBytes - buffer.buffer.Len()
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		_, _ = buffer.buffer.Write(data[:remaining])
	}
	return len(data), nil
}

func (buffer *limitedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
