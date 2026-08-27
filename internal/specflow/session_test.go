package specflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestSessionIsLazyAndReusesRuntime(t *testing.T) {
	starts := 0
	runtime := &fakeAppRuntime{}
	session := newSession(func(context.Context) (appRuntime, error) {
		starts++
		return runtime, nil
	})
	defer session.Close()
	if starts != 0 {
		t.Fatal("runtime started while idle")
	}
	if _, err := session.StartThread(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StartThread(); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || runtime.threads != 2 {
		t.Fatalf("starts = %d, threads = %d", starts, runtime.threads)
	}
}

func TestSessionRestartsAfterRuntimeError(t *testing.T) {
	crash := errors.New("crash")
	runtimes := []*fakeAppRuntime{{turnErr: crash}, {}}
	starts := 0
	session := newSession(func(context.Context) (appRuntime, error) {
		runtime := runtimes[starts]
		starts++
		return runtime, nil
	})
	defer session.Close()
	thread, err := session.StartThread()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.RunTurn(thread, "prompt", agentruntime.TurnOptions{}); !errors.Is(err, crash) {
		t.Fatalf("turn error = %v", err)
	}
	if runtimes[0].closes != 1 {
		t.Fatal("failed runtime was not closed")
	}
	if _, err := session.StartThread(); err != nil {
		t.Fatal(err)
	}
	if starts != 2 || runtimes[1].threads != 1 {
		t.Fatalf("starts = %d, replacement threads = %d", starts, runtimes[1].threads)
	}
}

func TestSessionReturnsFactoryErrorWhenRuntimeIsTypedNil(t *testing.T) {
	startErr := errors.New("start failed")
	session := newSession(func(context.Context) (appRuntime, error) {
		var runtime *fakeAppRuntime
		return runtime, startErr
	})
	defer session.Close()

	if _, err := session.StartThread(); !errors.Is(err, startErr) {
		t.Fatalf("start thread error = %v, want %v", err, startErr)
	}
}

func TestSessionInterruptUsesRuntimeInterrupt(t *testing.T) {
	runtime := &fakeAppRuntime{}
	session := newSession(func(context.Context) (appRuntime, error) { return runtime, nil })
	if _, err := session.StartThread(); err != nil {
		t.Fatal(err)
	}
	if err := session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if runtime.interrupts != 1 {
		t.Fatalf("interrupts = %d", runtime.interrupts)
	}
	if _, err := session.StartThread(); !errors.Is(err, agentruntime.ErrRuntimeClosed) {
		t.Fatalf("start after interrupt = %v", err)
	}
}

func TestSessionInterruptCancelsStartupWithoutHoldingMutex(t *testing.T) {
	started := make(chan struct{})
	session := newSession(func(ctx context.Context) (appRuntime, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	startResult := make(chan error, 1)
	go func() {
		_, err := session.StartThread()
		startResult <- err
	}()
	<-started
	if err := session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := <-startResult; !errors.Is(err, agentruntime.ErrRuntimeClosed) {
		t.Fatalf("startup result = %v", err)
	}
}

func TestInteractiveSessionReusesRuntimeForTwoApprovedFlows(t *testing.T) {
	repo := initDraftRepository(t)
	head := draftGit(t, repo, "rev-parse", "HEAD")
	runtime := &fakeAppRuntime{}
	runtime.turn = func(_ string, options agentruntime.TurnOptions) (json.RawMessage, error) {
		switch string(options.OutputSchema) {
		case string(InitialSchema()):
			id := fmt.Sprintf("flow-%d", runtime.threads)
			directory := filepath.Join(repo, "docs", "changes", "features", fmt.Sprintf("flow-%d", runtime.threads))
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(directory, "specification.md"), []byte("draft"), 0o600); err != nil {
				return nil, err
			}
			return json.RawMessage(fmt.Sprintf(`{"status":"WRITTEN","feature_id":%q}`, id)), nil
		default:
			return nil, errors.New("unexpected schema")
		}
	}
	starts := 0
	session := newSession(func(context.Context) (appRuntime, error) { starts++; return runtime, nil })
	controller := NewController(repo, session)
	ui := &controllerUI{controller: controller, ideas: []string{"one", "two"}}
	err := RunInteractive(context.Background(), controller, ui, session.Interrupt)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("run error = %v", err)
	}
	if starts != 1 || runtime.threads != 2 || runtime.interrupts != 1 {
		t.Fatalf("starts = %d, threads = %d, interrupts = %d", starts, runtime.threads, runtime.interrupts)
	}
	if got := draftGit(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD changed from %s to %s", head, got)
	}
	if content, err := os.ReadFile(filepath.Join(repo, "tracked.txt")); err != nil || string(content) != "original\n" {
		t.Fatalf("source changed: %q, %v", content, err)
	}
}

func TestInteractiveSessionRestartsAfterCrash(t *testing.T) {
	repo := initDraftRepository(t)
	crashed := &fakeAppRuntime{turnErr: agentruntime.ErrRuntimeExited}
	replacement := &fakeAppRuntime{turn: func(_ string, _ agentruntime.TurnOptions) (json.RawMessage, error) {
		directory := filepath.Join(repo, "docs", "changes", "features", "restart")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(directory, "specification.md"), []byte("draft"), 0o600); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"status":"WRITTEN","feature_id":"restart"}`), nil
	}}
	runtimes := []*fakeAppRuntime{crashed, replacement}
	starts := 0
	session := newSession(func(context.Context) (appRuntime, error) { runtime := runtimes[starts]; starts++; return runtime, nil })
	controller := NewController(repo, session)
	ui := &controllerUI{controller: controller, ideas: []string{"crash", "restart"}}
	err := RunInteractive(context.Background(), controller, ui, session.Interrupt)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("run error = %v", err)
	}
	if starts != 2 || crashed.closes != 1 || replacement.threads != 1 {
		t.Fatalf("starts = %d, crashed closes = %d, replacement threads = %d", starts, crashed.closes, replacement.threads)
	}
	if len(ui.reported) != 1 || !errors.Is(ui.reported[0], agentruntime.ErrRuntimeExited) {
		t.Fatalf("reported errors = %v", ui.reported)
	}
}

type fakeAppRuntime struct {
	threads    int
	turnErr    error
	turn       func(string, agentruntime.TurnOptions) (json.RawMessage, error)
	closes     int
	interrupts int
}

func (runtime *fakeAppRuntime) StartThread() (agentruntime.Thread, error) {
	runtime.threads++
	return &testThread{ID: "thread"}, nil
}

func (runtime *fakeAppRuntime) RunTurn(_ agentruntime.Thread, prompt string, options agentruntime.TurnOptions) (json.RawMessage, error) {
	if runtime.turn != nil {
		return runtime.turn(prompt, options)
	}
	return json.RawMessage(`{}`), runtime.turnErr
}

func (runtime *fakeAppRuntime) Interrupt() error { runtime.interrupts++; return nil }
func (runtime *fakeAppRuntime) Close() error     { runtime.closes++; return nil }

type controllerUI struct {
	controller *Controller
	ideas      []string
	mainCalls  int
	reported   []error
}

type testThread struct{ ID string }

func (ui *controllerUI) Main() (Progress, error) {
	if ui.mainCalls == len(ui.ideas) {
		return Progress{}, ErrCanceled
	}
	brief := ui.ideas[ui.mainCalls]
	ui.mainCalls++
	return ui.controller.StartIdea(brief)
}

func (ui *controllerUI) Draft(_ context.Context, _ Progress) (Progress, error) {
	return ui.controller.Approve()
}

func (*controllerUI) ChangeAnswer(context.Context, string) (Progress, error) {
	panic("unexpected change answer")
}

func (ui *controllerUI) ReportError(err error) { ui.reported = append(ui.reported, err) }
