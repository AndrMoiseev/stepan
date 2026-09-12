//go:build !windows

package impl_loop

import (
	"errors"
	"os"
	"syscall"
)

func tryLockControllerFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func unlockControllerFile(file *os.File) error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }
func isControllerLockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
