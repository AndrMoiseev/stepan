//go:build !windows

package processjob

import (
	"errors"
	"os"
	"sync"
)

type Job struct {
	mu      sync.Mutex
	process *os.Process
}

func New() (*Job, error) { return &Job{}, nil }

func (j *Job) Assign(process *os.Process) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.process = process
	return nil
}

func (j *Job) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.process == nil {
		return nil
	}
	err := j.process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
