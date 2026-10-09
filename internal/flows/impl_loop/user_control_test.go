package impl_loop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/checkexec"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func TestUserPauseInterruptsActiveAgentAndPersistsResumableState(t *testing.T) {
	runtime := &controlledCallRuntime{turns: []controlledTurn{{waitForInterrupt: true}}}
	call := controlledCallFixture(t, runtime)
	control, err := NewUserRunControl(call.Run, call.StateStore)
	if err != nil {
		t.Fatal(err)
	}
	call.UserControl = control

	done := make(chan error, 1)
	go func() {
		_, err := InvokeControlledAgentCall(context.Background(), call)
		done <- err
	}()
	if !runtime.waitForTurn(1, time.Second) {
		t.Fatal("agent turn did not start")
	}
	if err := control.Pause(context.Background(), "user requested pause"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrAgentCallCancelled) {
		t.Fatalf("interrupted agent call = %v", err)
	}
	if runtime.interrupts != 1 {
		t.Fatalf("agent interrupts = %d, want 1", runtime.interrupts)
	}
	if call.Run.Status != implstate.RunPaused || call.Run.PauseReason != "user requested pause" {
		t.Fatalf("paused run = %#v", call.Run)
	}
	if err := call.StateStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runstore.OpenState(call.Journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, _, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	attempts := persisted.RunOperations[0].Attempts
	if persisted.Status != implstate.RunPaused || len(attempts) != 1 || attempts[0].Outcome != implstate.AttemptInterrupted {
		t.Fatalf("durable pause and interrupted attempt = %#v", persisted)
	}
	if err := persisted.Resume(); err != nil {
		t.Fatalf("paused run did not resume: %v", err)
	}
}

func TestUserCloseInterruptsActiveCommandIsTerminalAndIsNotSuccess(t *testing.T) {
	run, stateStore, journal, _ := newInitialCheckRun(t)
	defer stateStore.Close()
	control, err := NewUserRunControl(run, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	commandDone := make(chan error, 1)
	runner := ControlledCheckRunner{Control: control, Runner: CheckRunnerFunc(func(ctx context.Context, _ checkexec.Command) (checkexec.Result, error) {
		close(started)
		<-ctx.Done()
		return checkexec.Result{ExitCode: -1, Failure: checkexec.FailureCanceled}, ctx.Err()
	})}
	go func() {
		_, err := runner.RunCheck(context.Background(), checkexec.Command{Program: "blocked-command"})
		commandDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("command did not start")
	}
	if err := control.Close(context.Background(), "user stopped run"); err != nil {
		t.Fatal(err)
	}
	if err := <-commandDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted command error = %v", err)
	}
	if run.Status != implstate.RunClosed || run.CloseReason != "user stopped run" || run.Status == implstate.RunSucceeded {
		t.Fatalf("closed run = %#v", run)
	}
	if err := run.Resume(); !errors.Is(err, implstate.ErrInvalidTransition) {
		t.Fatalf("closed run resumed: %v", err)
	}
	persisted, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != implstate.RunClosed || persisted.Status == implstate.RunSucceeded {
		t.Fatalf("durable closed state = %#v", persisted)
	}
}
