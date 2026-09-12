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
func AcquireController(store *runstore.Store, workCopy string) (*ControllerLease, error) {
	if store == nil {
		return nil, fmt.Errorf("acquire controller: nil run store")
	}
	canonical, err := canonicalWorkCopy(workCopy)
	if err != nil {
		return nil, err
	}
	locks := filepath.Join(store.Root(), "controller-locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, fmt.Errorf("create controller lock directory: %w", err)
	}
	info, err := os.Lstat(locks)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("controller lock directory is unsafe: %s", locks)
	}
	sum := sha256.Sum256([]byte(lockIdentity(canonical)))
	file, err := os.OpenFile(filepath.Join(locks, hex.EncodeToString(sum[:])+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
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

// AcquireNewRunController atomically reserves controller ownership and rejects
// a new run if an active or paused durable run already names this working copy.
func AcquireNewRunController(ctx context.Context, store *runstore.Store, workCopy string) (*ControllerLease, error) {
	lease, err := AcquireController(store, workCopy)
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
	canonical, err := canonicalWorkCopy(workCopy)
	if err != nil {
		return nil, err
	}
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
		stateWorkCopy, err := canonicalWorkCopy(state.Identity.WorkCopy)
		if err != nil {
			return nil, fmt.Errorf("run %s has invalid working copy: %w", id, err)
		}
		if lockIdentity(stateWorkCopy) == lockIdentity(canonical) && state.Status != implementationstate.RunClosed && state.Status != implementationstate.RunSucceeded {
			return state, nil
		}
	}
	return nil, nil
}

func canonicalWorkCopy(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("working copy path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve working copy: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("canonicalize working copy: %w", err)
	}
	return filepath.Clean(canonical), nil
}

func lockIdentity(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
