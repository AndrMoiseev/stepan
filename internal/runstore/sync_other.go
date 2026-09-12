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

// replaceProjectionFile publishes a complete replacement only after its
// SQLite handle is closed and synced. rename(2) keeps the former projection
// available until the replacement is ready, then the directory sync persists
// the new name.
func replaceProjectionFile(temporary, target string) error {
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func syncProjectionFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
