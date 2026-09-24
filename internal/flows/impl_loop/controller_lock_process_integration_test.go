//go:build process_integration

package impl_loop

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
)

func TestControllerLockRejectsSecondProcessButAllowsStatusReadAndReleasesOnExit(t *testing.T) {
	storeRoot := t.TempDir()
	workCopy := newFilesystemWorkspace(t)
	nestedWorkCopy := filepath.Join(workCopy, "real", "subdirectory")
	if err := os.MkdirAll(nestedWorkCopy, 0o700); err != nil {
		t.Fatal(err)
	}
	store := mustControllerStore(t, storeRoot)
	mustRecordControllerRun(t, store, "paused-run", workCopy, implstate.RunPaused)
	pausedRun, err := store.Open("paused-run")
	if err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(pausedRun.Path(), runstore.StateDatabaseFileName)
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestControllerLockHelper$")
	command.Env = append(os.Environ(), "STEPAN_LOCK_HELPER=1", "STEPAN_LOCK_STORE="+storeRoot, "STEPAN_LOCK_WORK_COPY="+workCopy)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "LOCKED" {
		t.Fatalf("helper did not acquire lock: %q (%v)", scanner.Text(), scanner.Err())
	}
	if _, err := AcquireControllerWithWorkspace(context.Background(), testfs.Directory(workCopy), store, workCopy); !errors.Is(err, ErrControllerBusy) {
		t.Fatalf("second controller error = %v", err)
	}
	current, err := FindUnclosedRunWithWorkspace(context.Background(), testfs.Directory(workCopy), store, workCopy)
	if err != nil || current == nil || current.Identity.ID != "paused-run" || current.Status != implstate.RunPaused {
		t.Fatalf("status while locked = %#v, %v", current, err)
	}
	if _, err := os.Stat(projection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status read rebuilt or changed projection: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	finished = true

	lease, err := AcquireControllerWithWorkspace(context.Background(), testfs.Directory(workCopy), store, workCopy)
	if err != nil {
		t.Fatalf("lock after helper exit: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestControllerLockHelper(t *testing.T) {
	if os.Getenv("STEPAN_LOCK_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	store, err := runstore.New(os.Getenv("STEPAN_LOCK_STORE"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := AcquireControllerWithWorkspace(context.Background(), testfs.Directory(""), store, os.Getenv("STEPAN_LOCK_WORK_COPY"))
	if err != nil {
		t.Fatal(err)
	}
	_ = lease // os.Exit below deliberately skips Close to exercise OS cleanup.
	fmt.Println("LOCKED")
	_, _ = os.Stdin.Read(make([]byte, 1))
	os.Exit(0)
}

func TestDiscoverStartupRunKeepsLiveActiveOwnerNonResumable(t *testing.T) {
	storeRoot := t.TempDir()
	workCopy := newFilesystemWorkspace(t)
	store := mustControllerStore(t, storeRoot)
	mustRecordControllerRun(t, store, "active-run", workCopy, implstate.RunActive)

	command := exec.Command(os.Args[0], "-test.run=^TestControllerLockHelper$")
	command.Env = append(os.Environ(), "STEPAN_LOCK_HELPER=1", "STEPAN_LOCK_STORE="+storeRoot, "STEPAN_LOCK_WORK_COPY="+workCopy)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "LOCKED" {
		t.Fatalf("helper did not acquire lock: %q (%v)", scanner.Text(), scanner.Err())
	}
	found, err := DiscoverStartupRun(context.Background(), store, workCopy)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.RecoveryRequired || found.Summary.Lifecycle != LifecycleActive {
		t.Fatalf("live owner startup = %#v", found)
	}
	for _, hint := range CommandsForLifecycle(found.Summary.Lifecycle) {
		if hint.Command == CommandResume {
			t.Fatalf("live active owner exposed /resume: %#v", found.Summary)
		}
	}
	if _, _, err := RecoverOwnRunWithWorkspace(context.Background(), testfs.Directory(workCopy), store, workCopy); !errors.Is(err, ErrControllerBusy) {
		t.Fatalf("recovery while a controller owns the run = %v", err)
	}
	current, err := FindUnclosedRunWithWorkspace(context.Background(), testfs.Directory(workCopy), store, workCopy)
	if err != nil || current.Status != implstate.RunActive {
		t.Fatalf("discovery changed live active state = %#v, %v", current, err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}
