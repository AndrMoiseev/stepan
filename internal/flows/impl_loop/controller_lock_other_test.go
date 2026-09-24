//go:build !windows

package impl_loop

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
)

func TestControllerLockRejectsSymlinkEntryAndReplacementRace(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing symlink", true: "replacement race"}[race], func(t *testing.T) {
			store := mustControllerStore(t, t.TempDir())
			repository := newFilesystemWorkspace(t)
			lease, err := AcquireControllerWithWorkspace(context.Background(), testfs.Directory(repository), store, repository)
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			entry := onlyControllerLockEntry(t, store)
			replace := func(path string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "target"), path); err != nil {
					t.Fatal(err)
				}
			}
			if race {
				beforeControllerLockOpenHook = replace
				defer func() { beforeControllerLockOpenHook = nil }()
			} else {
				replace(entry)
			}
			if lease, err := AcquireControllerWithWorkspace(context.Background(), testfs.Directory(repository), store, repository); err == nil {
				_ = lease.Close()
				t.Fatal("controller followed a symlinked lock entry")
			}
		})
	}
}
