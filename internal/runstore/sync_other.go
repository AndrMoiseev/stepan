//go:build !windows

package runstore

import (
	"os"
	"path/filepath"
)

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// publishFinalFile gives a final name to an already-synced temporary file
// without replacing an existing immutable artifact, then persists that name.
func publishFinalFile(temporary, target string) error {
	if err := os.Link(temporary, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}
