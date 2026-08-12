package specflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/codexapp"
)

func TestIterationOneHappyPathAndSecondIdea(t *testing.T) {
	repo := initDraftRepository(t)
	dirty := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(dirty, []byte("dirty baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := draftGit(t, repo, "rev-parse", "HEAD")
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	first := filepath.Join(repo, "docs", "specs", "acceptance-flow")
	second := filepath.Join(repo, "docs", "specs", "second-flow")
	runtime := &acceptanceRuntime{t: t, steps: []acceptanceStep{
		{InitialSchema(), InitialPrompt(`literal "brief"`), codexapp.ReadOnlyTurnPolicy(), `{"status":"NEEDS_INPUT","message":"One question?"}`, func() {
			assertMissing(t, first)
		}},
		{InitialSchema(), initialAnswerPrompt("answer"), codexapp.ReadOnlyTurnPolicy(), `{"status":"READY_TO_WRITE","spec_id":"acceptance-flow"}`, nil},
		{CreateSchema(), CreatePrompt(first), mustWritePolicy(t, first), `{"status":"WRITTEN"}`, func() {
			assertMissing(t, first)
			writeAcceptanceFile(t, filepath.Join(first, "specification.md"), "created")
			writeAcceptanceFile(t, filepath.Join(first, "details.md"), "details")
		}},
		{QuestionSchema(), QuestionPrompt("What is current?"), codexapp.ReadOnlyTurnPolicy(), `{"status":"ANSWERED","message":"manual before question"}`, func() {
			assertFile(t, filepath.Join(first, "specification.md"), "manual before question")
		}},
		{ChangeSchema(), ChangePrompt("Change color"), codexapp.ReadOnlyTurnPolicy(), `{"status":"NEEDS_INPUT","message":"Which color?"}`, func() {
			assertFile(t, filepath.Join(first, "specification.md"), "manual before change")
		}},
		{ChangeSchema(), changeAnswerPrompt("Blue"), codexapp.ReadOnlyTurnPolicy(), `{"status":"READY_TO_UPDATE"}`, func() {
			assertFile(t, filepath.Join(first, "specification.md"), "manual before answer")
		}},
		{UpdateSchema(), UpdatePrompt(first), mustWritePolicy(t, first), `{"status":"UPDATED"}`, func() {
			writeAcceptanceFile(t, filepath.Join(first, "specification.md"), "blue")
		}},
		{InitialSchema(), InitialPrompt("second idea"), codexapp.ReadOnlyTurnPolicy(), `{"status":"READY_TO_WRITE","spec_id":"second-flow"}`, nil},
		{CreateSchema(), CreatePrompt(second), mustWritePolicy(t, second), `{"status":"WRITTEN"}`, func() {
			writeAcceptanceFile(t, filepath.Join(second, "specification.md"), "second")
		}},
	}}
	starts := 0
	session := newSession(func() (appRuntime, error) { starts++; return runtime, nil })
	controller := NewController(repo, session)
	ui := &acceptanceUI{t: t, controller: controller, first: first}
	if err := RunInteractive(context.Background(), controller, ui, session.Interrupt); !errors.Is(err, ErrCanceled) {
		t.Fatalf("run error = %v", err)
	}

	if starts != 1 || len(runtime.threads) != 2 || runtime.threads[0].ID == runtime.threads[1].ID {
		t.Fatalf("process starts = %d, thread IDs = %#v", starts, runtime.threadIDs())
	}
	if runtime.step != len(runtime.steps) || runtime.interrupts != 1 {
		t.Fatalf("turn steps = %d/%d, interrupts = %d", runtime.step, len(runtime.steps), runtime.interrupts)
	}
	for index, turn := range runtime.turns[:7] {
		if turn.thread != runtime.threads[0] {
			t.Fatalf("first flow turn %d used %q", index, turn.thread.ID)
		}
	}
	for index, turn := range runtime.turns[7:] {
		if turn.thread != runtime.threads[1] {
			t.Fatalf("second flow turn %d used %q", index, turn.thread.ID)
		}
	}
	assertFile(t, filepath.Join(first, "specification.md"), "blue")
	assertFile(t, filepath.Join(second, "specification.md"), "second")
	assertFile(t, dirty, "dirty baseline\n")
	if got := draftGit(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD changed from %s to %s", head, got)
	}
	currentIndex, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil || !bytes.Equal(index, currentIndex) {
		t.Fatalf("Git index changed: %v", err)
	}
	for _, unexpected := range []string{
		filepath.Join(repo, ".stepan"),
		filepath.Join(first, "approved"),
		filepath.Join(first, ".approved"),
		filepath.Join(first, "state.json"),
	} {
		assertMissing(t, unexpected)
	}
}

func TestInterruptKeepsPartialDraftWithoutResumeState(t *testing.T) {
	repo := initDraftRepository(t)
	target := filepath.Join(repo, "docs", "specs", "interrupted-flow")
	runtime := &acceptanceRuntime{t: t, steps: []acceptanceStep{
		{InitialSchema(), InitialPrompt("brief"), codexapp.ReadOnlyTurnPolicy(), `{"status":"READY_TO_WRITE","spec_id":"interrupted-flow"}`, nil},
		{CreateSchema(), CreatePrompt(target), mustWritePolicy(t, target), `{"status":"WRITTEN"}`, func() {
			writeAcceptanceFile(t, filepath.Join(target, "specification.md"), "unfinished")
		}},
	}}
	session := newSession(func() (appRuntime, error) { return runtime, nil })
	controller := NewController(repo, session)
	if _, err := controller.StartIdea("brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StartThread(); !errors.Is(err, codexapp.ErrRuntimeClosed) {
		t.Fatalf("interrupted session resumed: %v", err)
	}
	assertFile(t, filepath.Join(target, "specification.md"), "unfinished")
	assertMissing(t, filepath.Join(repo, ".stepan"))
	assertMissing(t, filepath.Join(target, "state.json"))

	fresh := newSession(func() (appRuntime, error) { return &acceptanceRuntime{t: t}, nil })
	defer fresh.Close()
	if progress := NewController(repo, fresh).Progress(); progress != (Progress{State: StateIdle}) {
		t.Fatalf("new process resumed old flow: %#v", progress)
	}
}

type acceptanceStep struct {
	schema json.RawMessage
	prompt string
	policy codexapp.TurnPolicy
	output string
	action func()
}

type acceptanceTurn struct {
	thread  *codexapp.Thread
	prompt  string
	options codexapp.TurnOptions
}

type acceptanceRuntime struct {
	t          *testing.T
	threads    []*codexapp.Thread
	turns      []acceptanceTurn
	steps      []acceptanceStep
	step       int
	interrupts int
}

func (runtime *acceptanceRuntime) StartThread() (*codexapp.Thread, error) {
	thread := &codexapp.Thread{ID: fmt.Sprintf("thread-%d", len(runtime.threads)+1)}
	runtime.threads = append(runtime.threads, thread)
	return thread, nil
}

func (runtime *acceptanceRuntime) RunTurn(thread *codexapp.Thread, prompt string, options codexapp.TurnOptions) (json.RawMessage, error) {
	runtime.t.Helper()
	runtime.turns = append(runtime.turns, acceptanceTurn{thread, prompt, options})
	if runtime.step >= len(runtime.steps) {
		return nil, errors.New("unexpected turn")
	}
	step := runtime.steps[runtime.step]
	runtime.step++
	if prompt != step.prompt || string(options.OutputSchema) != string(step.schema) || options.Policy != step.policy {
		return nil, fmt.Errorf("turn %d contract mismatch", runtime.step)
	}
	if step.action != nil {
		step.action()
	}
	return json.RawMessage(step.output), nil
}

func (runtime *acceptanceRuntime) Interrupt() error { runtime.interrupts++; return nil }
func (*acceptanceRuntime) Close() error             { return nil }

func (runtime *acceptanceRuntime) threadIDs() []string {
	ids := make([]string, len(runtime.threads))
	for index, thread := range runtime.threads {
		ids[index] = thread.ID
	}
	return ids
}

type acceptanceUI struct {
	t          *testing.T
	controller *Controller
	first      string
	main       int
	draft      int
}

func (ui *acceptanceUI) Main() (Progress, error) {
	ui.main++
	switch ui.main {
	case 1:
		return ui.controller.StartIdea(`literal "brief"`)
	case 2:
		return ui.controller.StartIdea("second idea")
	default:
		return Progress{}, ErrCanceled
	}
}

func (ui *acceptanceUI) InitialAnswer(question string) (Progress, error) {
	if question != "One question?" {
		ui.t.Fatalf("initial question = %q", question)
	}
	assertMissing(ui.t, ui.first)
	return ui.controller.Submit("answer")
}

func (ui *acceptanceUI) Draft(ctx context.Context, progress Progress) (Progress, error) {
	ui.draft++
	switch ui.draft {
	case 1:
		if progress.Path != "docs/specs/acceptance-flow/specification.md" {
			ui.t.Fatalf("displayed path = %q", progress.Path)
		}
		writeAcceptanceFile(ui.t, filepath.Join(ui.first, "specification.md"), "manual before question")
		return ui.controller.AskQuestion("What is current?")
	case 2:
		if progress.Answer != "manual before question" {
			ui.t.Fatalf("question answer = %q", progress.Answer)
		}
		writeAcceptanceFile(ui.t, filepath.Join(ui.first, "specification.md"), "manual before change")
		return ui.controller.ProposeChange(ctx, "Change color")
	case 3:
		assertFile(ui.t, filepath.Join(ui.first, "specification.md"), "blue")
		return ui.controller.Approve()
	case 4:
		if progress.Path != "docs/specs/second-flow/specification.md" {
			ui.t.Fatalf("second displayed path = %q", progress.Path)
		}
		return ui.controller.Approve()
	default:
		return Progress{}, errors.New("unexpected draft menu")
	}
}

func (ui *acceptanceUI) ChangeAnswer(ctx context.Context, question string) (Progress, error) {
	if question != "Which color?" {
		ui.t.Fatalf("change question = %q", question)
	}
	writeAcceptanceFile(ui.t, filepath.Join(ui.first, "specification.md"), "manual before answer")
	return ui.controller.SubmitChangeAnswer(ctx, "Blue")
}

func (ui *acceptanceUI) ReportError(err error) { ui.t.Fatalf("unexpected flow error: %v", err) }

func mustWritePolicy(t *testing.T, root string) codexapp.TurnPolicy {
	t.Helper()
	policy, err := codexapp.SingleWriteRootTurnPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func writeAcceptanceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("file %q = %q, %v; want %q", path, data, err, want)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected path %q: %v", path, err)
	}
}
