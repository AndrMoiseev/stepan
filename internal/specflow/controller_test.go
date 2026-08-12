package specflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/codexapp"
)

func TestInitialClarificationQuestionAnswerReady(t *testing.T) {
	root := t.TempDir()
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"NEEDS_INPUT","message":"Which behavior?"}`},
		{output: `{"status":"READY_TO_WRITE","spec_id":"new-flow"}`},
	}}
	controller := NewController(root, runner)

	progress, err := controller.StartIdea("Build it")
	if err != nil || progress != (Progress{State: StateAwaitingAnswer, Question: "Which behavior?"}) {
		t.Fatalf("initial progress = %#v, %v", progress, err)
	}
	assertNoSpecTarget(t, root, "new-flow")

	progress, err = controller.Submit("Use the existing behavior")
	if err != nil || progress != (Progress{State: StateReadyToWrite, SpecID: "new-flow"}) {
		t.Fatalf("answer progress = %#v, %v", progress, err)
	}
	assertNoSpecTarget(t, root, "new-flow")
	if len(runner.starts) != 1 || len(runner.turns) != 2 || runner.turns[0].thread != runner.turns[1].thread {
		t.Fatalf("thread/turn calls = starts %v, turns %#v", runner.starts, runner.turns)
	}
	if runner.turns[0].prompt != InitialPrompt("Build it") {
		t.Fatalf("initial prompt = %q", runner.turns[0].prompt)
	}
	for _, fragment := range []string{
		"Не создавай и не изменяй файлы",
		"Задавай не более одного вопроса за turn.",
		"USER ANSWER:\nUse the existing behavior",
	} {
		if !strings.Contains(runner.turns[1].prompt, fragment) {
			t.Errorf("follow-up prompt misses %q", fragment)
		}
	}
	assertReadOnlyInitialTurns(t, runner.turns)
}

func TestIdeaWithoutBriefUsesExactlyNextMessage(t *testing.T) {
	runner := &fakeInitialRunner{steps: []fakeInitialStep{{output: `{"status":"READY_TO_WRITE","spec_id":"from-next-message"}`}}}
	controller := NewController(t.TempDir(), runner)

	progress, err := controller.StartIdea("")
	if err != nil || progress.State != StateAwaitingBrief || len(runner.starts) != 1 || len(runner.turns) != 0 {
		t.Fatalf("empty idea progress = %#v, starts=%d turns=%d err=%v", progress, len(runner.starts), len(runner.turns), err)
	}
	progress, err = controller.Submit("literal next message")
	if err != nil || progress != (Progress{State: StateReadyToWrite, SpecID: "from-next-message"}) {
		t.Fatalf("brief progress = %#v, %v", progress, err)
	}
	if len(runner.turns) != 1 || runner.turns[0].prompt != InitialPrompt("literal next message") {
		t.Fatalf("brief was not used literally: %#v", runner.turns)
	}
}

func TestInitialClarificationFailureEndsFlowWithoutRetry(t *testing.T) {
	processFailure := errors.New("server exited")
	for _, test := range []struct {
		name     string
		startErr error
		step     fakeInitialStep
	}{
		{"thread failure", processFailure, fakeInitialStep{}},
		{"invalid structured output", nil, fakeInitialStep{output: `{"status":"WRITTEN"}`}},
		{"process failure", nil, fakeInitialStep{err: processFailure}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeInitialRunner{startErr: test.startErr, steps: []fakeInitialStep{test.step}}
			controller := NewController(t.TempDir(), runner)
			if _, err := controller.StartIdea("brief"); err == nil {
				t.Fatal("expected flow error")
			}
			if got := controller.Progress(); got.State != StateIdle || got.Question != "" || got.SpecID != "" {
				t.Fatalf("failed flow remains active: %#v", got)
			}
			wantTurns := 1
			if test.startErr != nil {
				wantTurns = 0
			}
			if len(runner.turns) != wantTurns {
				t.Fatalf("turns = %d, want %d and no retry", len(runner.turns), wantTurns)
			}
		})
	}
}

func TestEachIdeaStartsFreshThread(t *testing.T) {
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"READY_TO_WRITE","spec_id":"first"}`},
		{output: `{"status":"NEEDS_INPUT","message":"Fresh question?"}`},
	}}
	controller := NewController(t.TempDir(), runner)
	if _, err := controller.StartIdea("first brief"); err != nil {
		t.Fatal(err)
	}
	if progress, err := controller.StartIdea("second brief"); err != nil || progress.Question != "Fresh question?" {
		t.Fatalf("second flow = %#v, %v", progress, err)
	}
	if len(runner.starts) != 2 || runner.turns[0].thread == runner.turns[1].thread || runner.turns[0].thread.ID == runner.turns[1].thread.ID {
		t.Fatalf("flows reused a thread: starts=%v turns=%#v", runner.starts, runner.turns)
	}
}

func TestCreateDraftHappyPathPreservesDirtyBaseline(t *testing.T) {
	repo := initDraftRepository(t)
	dirty := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(dirty, []byte("dirty before write\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	headBefore := draftGit(t, repo, "rev-parse", "HEAD")
	indexBefore, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "docs", "specs", "new-flow")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"READY_TO_WRITE","spec_id":"new-flow"}`},
		{output: `{"status":"WRITTEN"}`, action: func(turn fakeInitialTurn) error {
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				return fmt.Errorf("target existed before write turn: %v", err)
			}
			if turn.prompt != CreatePrompt(target) || string(turn.option.OutputSchema) != string(CreateSchema()) {
				return errors.New("wrong create prompt or schema")
			}
			wantPolicy, err := codexapp.SingleWriteRootTurnPolicy(target)
			if err != nil || turn.option.Policy != wantPolicy {
				return errors.New("write policy does not contain exactly the target")
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(target, "specification.md"), []byte("# Draft\n"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(target, "details.md"), []byte("details\n"), 0o600)
		}},
	}}
	controller := NewController(repo, runner)
	if progress, err := controller.StartIdea("brief"); err != nil || progress.State != StateReadyToWrite {
		t.Fatalf("ready progress = %#v, %v", progress, err)
	}
	progress, err := controller.CreateDraft(context.Background())
	if err != nil || progress != (Progress{State: StateDraft, SpecID: "new-flow", Path: "docs/specs/new-flow/specification.md"}) {
		t.Fatalf("draft progress = %#v, %v", progress, err)
	}
	if data, err := os.ReadFile(dirty); err != nil || string(data) != "dirty before write\n" {
		t.Fatalf("dirty baseline changed: %q, %v", data, err)
	}
	indexAfter, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil || !bytes.Equal(indexAfter, indexBefore) || draftGit(t, repo, "rev-parse", "HEAD") != headBefore {
		t.Fatalf("HEAD or index changed: %v", err)
	}
	if len(runner.turns) != 2 || runner.turns[0].thread != runner.turns[1].thread {
		t.Fatalf("create did not reuse clarification thread: %#v", runner.turns)
	}
}

func TestCreateDraftRejectsExistingTargetBeforeWrite(t *testing.T) {
	repo := initDraftRepository(t)
	runner := &fakeInitialRunner{steps: []fakeInitialStep{{output: `{"status":"READY_TO_WRITE","spec_id":"existing"}`}}}
	controller := NewController(repo, runner)
	if _, err := controller.StartIdea("brief"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "docs", "specs", "existing")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateDraft(context.Background()); err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "cleanup was not performed") {
		t.Fatalf("existing target error = %v", err)
	}
	if len(runner.turns) != 1 {
		t.Fatalf("write turn ran for existing target: %d turns", len(runner.turns))
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "keep" {
		t.Fatalf("existing target changed: %q, %v", data, err)
	}
}

func TestCreateDraftRevalidatesSpecIDBeforeWrite(t *testing.T) {
	repo := initDraftRepository(t)
	runner := &fakeInitialRunner{}
	controller := NewController(repo, runner)
	controller.state, controller.specID, controller.thread = StateReadyToWrite, "CON", &codexapp.Thread{ID: "thread"}
	if _, err := controller.CreateDraft(context.Background()); err == nil || !strings.Contains(err.Error(), "reserved Windows device name") {
		t.Fatalf("invalid spec-id error = %v", err)
	}
	if len(runner.turns) != 0 {
		t.Fatal("write turn ran for invalid spec-id")
	}
}

func TestCreateDraftFailureKeepsPartialFiles(t *testing.T) {
	turnFailure := errors.New("server crashed")
	for _, test := range []struct {
		name       string
		output     string
		turnErr    error
		write      func(repo, target string) error
		wantError  string
		wantRemain string
	}{
		{"missing entrypoint", `{"status":"WRITTEN"}`, nil, writePartial("partial.md"), "entrypoint", "partial.md"},
		{"non-regular entrypoint", `{"status":"WRITTEN"}`, nil, writeDirectoryEntrypoint, "not a regular file", "specification.md"},
		{"invalid status", `{"status":"UPDATED"}`, nil, writePartial("specification.md"), "invalid status", "specification.md"},
		{"outside write", `{"status":"WRITTEN"}`, nil, writeOutside, "outside.txt", "specification.md"},
		{"partial process failure", "", turnFailure, writePartial("partial.md"), "server crashed", "partial.md"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initDraftRepository(t)
			target := filepath.Join(repo, "docs", "specs", "broken")
			runner := &fakeInitialRunner{steps: []fakeInitialStep{
				{output: `{"status":"READY_TO_WRITE","spec_id":"broken"}`},
				{output: test.output, err: test.turnErr, action: func(fakeInitialTurn) error { return test.write(repo, target) }},
			}}
			controller := NewController(repo, runner)
			if _, err := controller.StartIdea("brief"); err != nil {
				t.Fatal(err)
			}
			progress, err := controller.CreateDraft(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.wantError) || !strings.Contains(err.Error(), "cleanup was not performed") {
				t.Fatalf("draft error = %v", err)
			}
			if test.name == "outside write" && !strings.Contains(err.Error(), "other/outside.md") {
				t.Fatalf("boundary error omits an outside path: %v", err)
			}
			if progress.State != StateIdle {
				t.Fatalf("failed flow state = %#v", progress)
			}
			if _, err := os.Lstat(filepath.Join(target, test.wantRemain)); err != nil {
				t.Fatalf("partial file was removed: %v", err)
			}
		})
	}
}

func TestCreateDraftRepeatsContainmentAfterWrite(t *testing.T) {
	repo := initDraftRepository(t)
	target := filepath.Join(repo, "docs", "specs", "escaped")
	outside := t.TempDir()
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"READY_TO_WRITE","spec_id":"escaped"}`},
		{output: `{"status":"WRITTEN"}`, action: func(fakeInitialTurn) error {
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(outside, "specification.md"), []byte("escaped"), 0o600); err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				output, err := exec.Command("cmd", "/c", "mklink", "/J", target, outside).CombinedOutput()
				if err != nil {
					return fmt.Errorf("create junction: %w: %s", err, output)
				}
				return nil
			}
			return os.Symlink(outside, target)
		}},
	}}
	controller := NewController(repo, runner)
	if _, err := controller.StartIdea("brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateDraft(context.Background()); err == nil || !strings.Contains(err.Error(), "escapes Git root") {
		t.Fatalf("post-write containment error = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "specification.md")); err != nil || string(data) != "escaped" {
		t.Fatalf("escaped partial file changed: %q, %v", data, err)
	}
}

type fakeInitialStep struct {
	output string
	err    error
	action func(fakeInitialTurn) error
}

type fakeInitialTurn struct {
	thread *codexapp.Thread
	prompt string
	option codexapp.TurnOptions
}

type fakeInitialRunner struct {
	starts   []string
	turns    []fakeInitialTurn
	steps    []fakeInitialStep
	startErr error
}

func (runner *fakeInitialRunner) StartThread() (*codexapp.Thread, error) {
	id := "thread-" + string(rune('1'+len(runner.starts)))
	runner.starts = append(runner.starts, id)
	if runner.startErr != nil {
		return nil, runner.startErr
	}
	return &codexapp.Thread{ID: id}, nil
}

func (runner *fakeInitialRunner) RunTurn(thread *codexapp.Thread, prompt string, option codexapp.TurnOptions) (json.RawMessage, error) {
	turn := fakeInitialTurn{thread: thread, prompt: prompt, option: option}
	runner.turns = append(runner.turns, turn)
	if len(runner.turns) > len(runner.steps) {
		return nil, errors.New("unexpected turn")
	}
	step := runner.steps[len(runner.turns)-1]
	if step.action != nil {
		if err := step.action(turn); err != nil {
			return nil, err
		}
	}
	return json.RawMessage(step.output), step.err
}

func assertReadOnlyInitialTurns(t *testing.T, turns []fakeInitialTurn) {
	t.Helper()
	for index, turn := range turns {
		if string(turn.option.OutputSchema) != string(InitialSchema()) {
			t.Errorf("turn %d used another schema", index)
		}
		if turn.option.Policy != codexapp.ReadOnlyTurnPolicy() {
			t.Errorf("turn %d was not read-only", index)
		}
	}
}

func assertNoSpecTarget(t *testing.T, root, specID string) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(root, "docs", "specs", specID))
	if !os.IsNotExist(err) {
		t.Fatalf("read-only clarification created a target: %v", err)
	}
}

func initDraftRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	draftGit(t, repo, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	draftGit(t, repo, "add", "-A")
	draftGit(t, repo, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "initial")
	return repo
}

func draftGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repo
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writePartial(name string) func(string, string) error {
	return func(_, target string) error {
		if err := os.MkdirAll(target, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, name), []byte("partial"), 0o600)
	}
}

func writeDirectoryEntrypoint(_, target string) error {
	return os.MkdirAll(filepath.Join(target, "specification.md"), 0o700)
}

func writeOutside(repo, target string) error {
	if err := writePartial("specification.md")(repo, target); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(repo, "outside.txt"), []byte("outside"), 0o600); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(repo, "other"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(repo, "other", "outside.md"), []byte("outside"), 0o600)
}
