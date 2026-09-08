//go:build windows

package processjob

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
	createSuspended                   = 0x00000004
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	setInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	ntdll                    = syscall.NewLazyDLL("ntdll.dll")
	ntResumeProcess          = ntdll.NewProc("NtResumeProcess")
)

type basicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type extendedLimitInformation struct {
	BasicLimitInformation basicLimitInformation
	IOInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type Job struct {
	mu       sync.Mutex
	handle   syscall.Handle
	prepared bool
	assigned bool
	closed   bool
	once     sync.Once
	err      error
}

func New() (*Job, error) {
	handle, _, callErr := createJobObjectW.Call(0, 0)
	if handle == 0 {
		return nil, windowsCallError(callErr)
	}
	job := &Job{handle: syscall.Handle(handle)}
	info := extendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	ok, _, callErr := setInformationJobObject.Call(
		handle,
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ok == 0 {
		job.Close()
		return nil, windowsCallError(callErr)
	}
	return job, nil
}

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
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= createSuspended
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
	if j.assigned {
		return errors.New("process job is already assigned")
	}
	var callErr error
	if err := process.WithHandle(func(handle uintptr) {
		ok, _, err := assignProcessToJobObject.Call(uintptr(j.handle), handle)
		if ok == 0 {
			callErr = windowsCallError(err)
			return
		}
		j.assigned = true
		status, _, _ := ntResumeProcess.Call(handle)
		if status != 0 {
			callErr = fmt.Errorf("resume assigned process: NTSTATUS 0x%08x", uint32(status))
		}
	}); err != nil {
		return err
	}
	return callErr
}

func (j *Job) Close() error {
	j.once.Do(func() {
		j.mu.Lock()
		j.closed = true
		j.err = syscall.CloseHandle(j.handle)
		j.mu.Unlock()
	})
	return j.err
}

func windowsCallError(err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return errors.New("Windows API call failed")
	}
	return err
}
