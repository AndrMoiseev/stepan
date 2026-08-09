//go:build !windows

package codexexec

import (
	"errors"
	"os"
	"sync"
)

type localJob struct {
	mu      sync.Mutex
	process *os.Process
}

func newProcessJob() (*localJob, error) { return &localJob{}, nil }

func (j *localJob) Assign(process *os.Process) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.process = process
	return nil
}

func (j *localJob) Close() error {
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
