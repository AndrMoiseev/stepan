// Command testall runs the complete local test suite with a fixed wall-clock budget.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/AndrMoiseev/stepan/internal/processjob"
)

const suiteTimeout = 120 * time.Second

func main() { os.Exit(mainCode()) }

func mainCode() int {
	interrupt, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(interrupt, suiteTimeout)
	defer cancel()
	code, err := run(ctx, "go", []string{"test", "-count=1", "-parallel=4", "-timeout=120s", "-tags=git_integration,process_integration", "./..."}, os.Stdout, os.Stderr)
	if errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(os.Stderr, "TEST SUITE TIMEOUT (120s): stop work and propose reviewing the architecture with the user. Do not retry or increase the timeout automatically.")
		return 124
	}
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "test suite interrupted")
		return 130
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return code
}

// run owns the child and its descendants until completion or cancellation.
func run(ctx context.Context, program string, args []string, stdout, stderr io.Writer) (int, error) {
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	job, err := processjob.New()
	if err != nil {
		return 1, fmt.Errorf("create test process job: %w", err)
	}
	defer job.Close()
	command := exec.Command(program, args...)
	command.Stdout, command.Stderr = stdout, stderr
	command.WaitDelay = time.Second
	if err := job.Prepare(command); err != nil {
		return 1, fmt.Errorf("prepare test process: %w", err)
	}
	if err := command.Start(); err != nil {
		return 1, fmt.Errorf("start test process: %w", err)
	}
	if err := job.Assign(command.Process); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return 1, fmt.Errorf("assign test process job: %w", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		if closeErr := job.Close(); closeErr != nil {
			return 1, fmt.Errorf("close test process job: %w", closeErr)
		}
		if err == nil {
			return 0, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), err
		}
		return 1, err
	case <-ctx.Done():
		closeErr := job.Close()
		// Also terminate the leader if closing its job failed.
		killErr := command.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		<-wait
		return 1, errors.Join(ctx.Err(), closeErr, killErr)
	}
}
