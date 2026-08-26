//go:build !windows && !darwin

package processjob

import (
	"errors"
	"os"
	"os/exec"
	"sync"
)

type Job struct {
	mu      sync.Mutex
	process *os.Process
	closed  bool
}

func New() (*Job, error) { return &Job{}, nil }

func (j *Job) Prepare(command *exec.Cmd) error {
	if command == nil {
		return errors.New("process command is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("process job is closed")
	}
	return nil
}

func (j *Job) Assign(process *os.Process) error {
	if process == nil {
		return errors.New("process is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("process job is closed")
	}
	j.process = process
	return nil
}

func (j *Job) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	if j.process == nil {
		return nil
	}
	err := j.process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
