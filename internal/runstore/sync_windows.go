//go:build windows

package runstore

import "golang.org/x/sys/windows"

// Windows does not support Sync on directory handles. Final publication uses
// MoveFileExW with MOVEFILE_WRITE_THROUGH instead.
func syncDirectory(string) error { return nil }

// publishFinalFile deliberately omits MOVEFILE_REPLACE_EXISTING: an existing
// name fails rather than replacing another publisher's immutable artifact.
// MOVEFILE_WRITE_THROUGH makes MoveFileExW wait until the move is on disk.
func publishFinalFile(temporary, target string) error {
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
