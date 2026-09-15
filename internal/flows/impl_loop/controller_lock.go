package impl_loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var beforeControllerLockOpenHook func(string)

var (
	ErrControllerBusy = errors.New("another controller owns this working copy")
	ErrOpenRun        = errors.New("working copy already has an unclosed implementation run")
)

// ControllerLease is an OS-owned exclusive controller lock. Close releases it
// explicitly; the operating system also releases it if the process exits.
type ControllerLease struct {
	file     *os.File
	workCopy string
}

func (l *ControllerLease) WorkCopy() string {
	if l == nil {
		return ""
	}
	return l.workCopy
}

func (l *ControllerLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return errors.Join(unlockControllerFile(file), file.Close())
}

// AcquireController takes controller ownership for one canonical working copy.
// It does not inspect run status, which permits resume to acquire the same lock.
func AcquireController(ctx context.Context, store *runstore.Store, workCopy string) (*ControllerLease, error) {
	if store == nil {
		return nil, fmt.Errorf("acquire controller: nil run store")
	}
	canonical, err := FindGitRoot(ctx, workCopy)
	if err != nil {
		return nil, err
	}
	locks := filepath.Join(store.Root(), "controller-locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, fmt.Errorf("create controller lock directory: %w", err)
	}
	sum := sha256.Sum256([]byte(lockIdentity(canonical)))
	file, err := openControllerLockFile(locks, filepath.Join(locks, hex.EncodeToString(sum[:])+".lock"))
	if err != nil {
		return nil, fmt.Errorf("open controller lock: %w", err)
	}
	if err := tryLockControllerFile(file); err != nil {
		_ = file.Close()
		if isControllerLockBusy(err) {
			return nil, fmt.Errorf("%w: %s", ErrControllerBusy, canonical)
		}
		return nil, fmt.Errorf("lock controller for %s: %w", canonical, err)
	}
	return &ControllerLease{file: file, workCopy: canonical}, nil
}

// ControllerOwned reports whether another process currently owns the
// controller lock for an already canonical working copy. Unlike AcquireController
// it never creates the lock directory or lock file, so startup discovery stays
// a read-only operation. A false result means a durable active run was left
// without a live owner and can only be recovered by an explicit /resume.
func ControllerOwned(ctx context.Context, store *runstore.Store, canonical string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil {
		return false, fmt.Errorf("inspect controller ownership: nil run store")
	}
	canonical = filepath.Clean(canonical)
	locks := filepath.Join(store.Root(), "controller-locks")
	directory, err := os.Open(locks)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open controller lock directory: %w", err)
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return false, fmt.Errorf("opened controller lock directory is unsafe: %s", locks)
	}
	pathInfo, err := os.Lstat(locks)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() || !os.SameFile(info, pathInfo) {
		return false, fmt.Errorf("controller lock directory is unsafe: %s", locks)
	}
	sum := sha256.Sum256([]byte(lockIdentity(canonical)))
	path := filepath.Join(locks, hex.EncodeToString(sum[:])+".lock")
	entry, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return false, fmt.Errorf("controller lock entry is unsafe: %s", path)
	}
	file, err := openExistingControllerFileNoFollow(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return false, fmt.Errorf("opened controller lock entry is unsafe: %s", path)
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return false, fmt.Errorf("controller lock entry changed while opening: %s", path)
	}
	if err := tryLockControllerFile(file); err != nil {
		if isControllerLockBusy(err) {
			return true, nil
		}
		return false, fmt.Errorf("inspect controller lock for %s: %w", canonical, err)
	}
	if err := unlockControllerFile(file); err != nil {
		return false, fmt.Errorf("release controller ownership probe: %w", err)
	}
	return false, nil
}

// AcquireNewRunController atomically reserves controller ownership and rejects
// a new run if an active or paused durable run already names this working copy.
func AcquireNewRunController(ctx context.Context, store *runstore.Store, workCopy string) (*ControllerLease, error) {
	lease, err := AcquireController(ctx, store, workCopy)
	if err != nil {
		return nil, err
	}
	if existing, err := FindUnclosedRun(ctx, store, lease.workCopy); err != nil {
		_ = lease.Close()
		return nil, err
	} else if existing != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("%w: %s", ErrOpenRun, existing.Identity.ID)
	}
	return lease, nil
}

// FindUnclosedRun is a read-only status operation and deliberately takes no
// controller lock. Paused runs remain open; closed and succeeded runs do not.
func FindUnclosedRun(ctx context.Context, store *runstore.Store, workCopy string) (*implementationstate.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := FindGitRoot(ctx, workCopy)
	if err != nil {
		return nil, err
	}
	return findUnclosedRun(ctx, store, canonical)
}

// findUnclosedRun reads durable state for an already canonical working-copy
// identity. Keeping this read-only primitive separate lets normal startup use
// the Git root it has already resolved, without another Git invocation or a
// controller lock.
func findUnclosedRun(ctx context.Context, store *runstore.Store, canonical string) (*implementationstate.Run, error) {
	if store == nil {
		return nil, fmt.Errorf("find unclosed implementation run: nil run store")
	}
	canonical = filepath.Clean(canonical)
	ids, err := store.RunIDs()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		run, err := store.Open(id)
		if err != nil {
			return nil, err
		}
		state, _, err := runstore.ReadJournalCurrent(run)
		if errors.Is(err, runstore.ErrCurrentStateUnavailable) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read run %s status: %w", id, err)
		}
		if !isUnclosedStatus(state.Status) {
			continue
		}
		if lockIdentity(filepath.Clean(state.Identity.WorkCopy)) != lockIdentity(canonical) {
			continue
		}
		if lockIdentity(state.Identity.WorkCopy) == lockIdentity(canonical) {
			return state, nil
		}
	}
	return nil, nil
}

func isUnclosedStatus(status implementationstate.RunStatus) bool {
	return status == implementationstate.RunActive || status == implementationstate.RunPaused
}

func lockIdentity(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func openControllerLockFile(directory, path string) (*os.File, error) {
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return nil, fmt.Errorf("open controller lock directory: %w", err)
	}
	defer directoryHandle.Close()
	openedDirectory, err := directoryHandle.Stat()
	if err != nil || !openedDirectory.IsDir() {
		return nil, fmt.Errorf("opened controller lock directory is unsafe: %s", directory)
	}
	pathDirectory, err := os.Lstat(directory)
	if err != nil || pathDirectory.Mode()&os.ModeSymlink != 0 || !pathDirectory.IsDir() || !os.SameFile(openedDirectory, pathDirectory) {
		return nil, fmt.Errorf("controller lock directory is unsafe: %s", directory)
	}
	if entry, err := os.Lstat(path); err == nil {
		if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
			return nil, fmt.Errorf("controller lock entry is unsafe: %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect controller lock entry: %w", err)
	}
	if beforeControllerLockOpenHook != nil {
		beforeControllerLockOpenHook(path)
	}
	file, err := openControllerFileNoFollow(path)
	if err != nil {
		return nil, err
	}
	closeOnError := func(err error) (*os.File, error) { _ = file.Close(); return nil, err }
	openedEntry, err := file.Stat()
	if err != nil || !openedEntry.Mode().IsRegular() {
		return closeOnError(fmt.Errorf("opened controller lock entry is unsafe: %s", path))
	}
	pathEntry, err := os.Lstat(path)
	if err != nil || pathEntry.Mode()&os.ModeSymlink != 0 || !pathEntry.Mode().IsRegular() || !os.SameFile(openedEntry, pathEntry) {
		return closeOnError(fmt.Errorf("controller lock entry changed while opening: %s", path))
	}
	afterDirectory, err := os.Lstat(directory)
	if err != nil || afterDirectory.Mode()&os.ModeSymlink != 0 || !afterDirectory.IsDir() || !os.SameFile(openedDirectory, afterDirectory) {
		return closeOnError(fmt.Errorf("controller lock directory changed while opening: %s", directory))
	}
	return file, nil
}
