//go:build darwin

package processjob

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type Job struct {
	mu        sync.Mutex
	prepared  bool
	closed    bool
	pgid      int
	closeOnce sync.Once
	closeErr  error
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
	if j.prepared {
		return errors.New("process job is already prepared")
	}
	if command.Process != nil {
		return errors.New("process command is already started")
	}
	if command.SysProcAttr != nil && (command.SysProcAttr.Setsid || command.SysProcAttr.Setpgid || command.SysProcAttr.Foreground || command.SysProcAttr.Pgid != 0) {
		return errors.New("process command already configures a session or process group")
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	command.SysProcAttr.Pgid = 0
	j.prepared = true
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
	if !j.prepared {
		return errors.New("process job is not prepared")
	}
	if j.pgid != 0 {
		return errors.New("process job is already assigned")
	}

	pgid := process.Pid
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return fmt.Errorf("unsafe process group ID %d", pgid)
	}
	j.pgid = pgid
	actual, err := syscall.Getpgid(process.Pid)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read process group: %w", err)
	}
	if actual != pgid {
		return fmt.Errorf("process group %d does not match expected group %d", actual, pgid)
	}
	return nil
}

func (j *Job) Close() error {
	j.closeOnce.Do(func() {
		j.mu.Lock()
		j.closed = true
		pgid := j.pgid
		j.mu.Unlock()
		if pgid == 0 {
			return
		}
		if pgid <= 1 || pgid == syscall.Getpgrp() {
			j.closeErr = fmt.Errorf("refusing to kill unsafe process group %d", pgid)
			return
		}
		actual, err := syscall.Getpgid(pgid)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			j.closeErr = fmt.Errorf("read process group %d: %w", pgid, err)
			return
		}
		// The process may have already exited and its PID may have been reused.
		// Do not signal an unrelated process group in that case.
		if actual != pgid {
			return
		}
		err = syscall.Kill(-pgid, syscall.SIGKILL)
		if err == nil || errors.Is(err, syscall.ESRCH) {
			return
		}
		j.closeErr = fmt.Errorf("kill process group %d: %w", pgid, err)
	})
	return j.closeErr
}
