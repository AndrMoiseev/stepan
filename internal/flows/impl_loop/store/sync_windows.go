//go:build windows

package store

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

// replaceProjectionFile uses the Windows replacement operation only after the
// rebuilt SQLite file is closed and synced. MOVEFILE_WRITE_THROUGH leaves the
// old projection in place until Windows accepts the ready replacement.
func replaceProjectionFile(temporary, target string) error {
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// SQLite closes the replacement after FULL synchronous commits. Reopening an
// already-closed database read-only and calling Sync is denied by Windows, so
// publication relies on MOVEFILE_WRITE_THROUGH above for the final barrier.
func syncProjectionFile(string) error { return nil }
