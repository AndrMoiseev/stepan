//go:build !windows

package codexexec

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
