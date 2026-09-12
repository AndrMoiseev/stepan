//go:build !windows

package impl_loop

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openControllerFileNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open controller lock without following links: %w", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func tryLockControllerFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func unlockControllerFile(file *os.File) error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }
func isControllerLockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
