//go:build windows

package codexexec

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	setInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
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

type windowsJob struct {
	handle syscall.Handle
	once   sync.Once
	err    error
}

func newProcessJob() (*windowsJob, error) {
	handle, _, callErr := createJobObjectW.Call(0, 0)
	if handle == 0 {
		return nil, windowsCallError(callErr)
	}
	job := &windowsJob{handle: syscall.Handle(handle)}
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

func (j *windowsJob) Assign(process *os.Process) error {
	var callErr error
	if err := process.WithHandle(func(handle uintptr) {
		ok, _, err := assignProcessToJobObject.Call(uintptr(j.handle), handle)
		if ok == 0 {
			callErr = windowsCallError(err)
		}
	}); err != nil {
		return err
	}
	return callErr
}

func (j *windowsJob) Close() error {
	j.once.Do(func() {
		j.err = syscall.CloseHandle(j.handle)
	})
	return j.err
}

func windowsCallError(err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return errors.New("Windows API call failed")
	}
	return err
}
