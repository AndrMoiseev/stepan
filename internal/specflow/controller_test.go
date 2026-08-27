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
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestFirstTurnCreatesDraftAndReturnsFeatureID(t *testing.T) {
	repo := initDraftRepository(t)
	dirty := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(dirty, []byte("dirty before write\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := draftGit(t, repo, "rev-parse", "HEAD")
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	features := filepath.Join(repo, "docs", "changes", "features")
	target := filepath.Join(features, "new-flow")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{{output: `{"status":"WRITTEN","feature_id":"new-flow"}`, action: func(turn fakeInitialTurn) error {
		policy, err := agentruntime.SingleWriteRootTurnPolicy(features)
		if err != nil || turn.option.Policy != policy || string(turn.option.OutputSchema) != string(InitialSchema()) {
			return errors.New("first turn has an incorrect write contract")
		}
		if turn.prompt != InitialPrompt("Build it", features) {
			return errors.New("first turn has an incorrect prompt")
		}
		return writeTestFile(filepath.Join(target, "specification.md"), "# Draft\n")
	}}}}

	progress, err := NewController(repo, runner).StartFeature("Build it")
	want := Progress{State: StateDraft, SpecID: "new-flow", Path: "docs/changes/features/new-flow/specification.md"}
	if err != nil || progress != want {
		t.Fatalf("progress = %#v, %v; want %#v", progress, err, want)
	}
	if len(runner.turns) != 1 {
		t.Fatalf("turns = %d, want one", len(runner.turns))
	}
	if data, err := os.ReadFile(dirty); err != nil || string(data) != "dirty before write\n" {
		t.Fatalf("dirty baseline changed: %q, %v", data, err)
	}
	if after, err := os.ReadFile(filepath.Join(repo, ".git", "index")); err != nil || !bytes.Equal(index, after) || draftGit(t, repo, "rev-parse", "HEAD") != head {
		t.Fatalf("HEAD or index changed: %v", err)
	}
}

func TestFirstTurnRejectsExistingFeatureAndOutsideWrites(t *testing.T) {
	for _, test := range []struct {
		name, output, want string
		setup, write       func(string) error
	}{
		{"existing feature", `{"status":"WRITTEN","feature_id":"taken"}`, "already exists", func(repo string) error {
			return writeTestFile(filepath.Join(repo, "docs", "changes", "features", "taken", "keep.md"), "keep")
		}, func(repo string) error {
			return writeTestFile(filepath.Join(repo, "docs", "changes", "features", "taken", "specification.md"), "overwritten")
		}},
		{"another feature directory", `{"status":"WRITTEN","feature_id":"chosen"}`, "docs/changes/features/other/outside.md", func(string) error { return nil }, func(repo string) error {
			if err := writeTestFile(filepath.Join(repo, "docs", "changes", "features", "chosen", "specification.md"), "draft"); err != nil {
				return err
			}
			return writeTestFile(filepath.Join(repo, "docs", "changes", "features", "other", "outside.md"), "outside")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initDraftRepository(t)
			if err := test.setup(repo); err != nil {
				t.Fatal(err)
			}
			runner := &fakeInitialRunner{steps: []fakeInitialStep{{output: test.output, action: func(fakeInitialTurn) error { return test.write(repo) }}}}
			progress, err := NewController(repo, runner).StartFeature("brief")
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "cleanup was not performed") || progress.State != StateIdle {
				t.Fatalf("progress = %#v, error = %v", progress, err)
			}
		})
	}
}

func TestFirstTurnRejectsInvalidResultButKeepsPartialFiles(t *testing.T) {
	repo := initDraftRepository(t)
	partial := filepath.Join(repo, "docs", "changes", "features", "partial", "partial.md")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{{output: `{"status":"WRITTEN"}`, action: func(fakeInitialTurn) error { return writeTestFile(partial, "partial") }}}}
	progress, err := NewController(repo, runner).StartFeature("brief")
	if err == nil || !strings.Contains(err.Error(), "requires feature_id") || progress.State != StateIdle {
		t.Fatalf("progress = %#v, error = %v", progress, err)
	}
	if data, err := os.ReadFile(partial); err != nil || string(data) != "partial" {
		t.Fatalf("partial draft was removed: %q, %v", data, err)
	}
}

func TestDraftQuestionAndChangeReuseInitialThread(t *testing.T) {
	repo := initDraftRepository(t)
	target := filepath.Join(repo, "docs", "changes", "features", "flow")
	entrypoint := filepath.Join(target, "specification.md")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"WRITTEN","feature_id":"flow"}`, action: func(fakeInitialTurn) error { return writeTestFile(entrypoint, "original") }},
		{output: `{"status":"ANSWERED","message":"answer"}`},
		{output: `{"status":"READY_TO_UPDATE"}`},
		{output: `{"status":"UPDATED"}`, action: func(turn fakeInitialTurn) error {
			if turn.prompt != UpdatePrompt(target) {
				return errors.New("incorrect update prompt")
			}
			return writeTestFile(entrypoint, "updated")
		}},
	}}
	controller := NewController(repo, runner)
	if _, err := controller.StartFeature("brief"); err != nil {
		t.Fatal(err)
	}
	if progress, err := controller.AskQuestion("question"); err != nil || progress.Answer != "answer" || progress.State != StateDraft {
		t.Fatalf("answer = %#v, %v", progress, err)
	}
	if progress, err := controller.ProposeChange(context.Background(), "change"); err != nil || progress.State != StateDraft {
		t.Fatalf("change = %#v, %v", progress, err)
	}
	if len(runner.turns) != 4 || runner.turns[0].thread != runner.turns[3].thread {
		t.Fatalf("turn threads = %#v", runner.turns)
	}
	if data, err := os.ReadFile(entrypoint); err != nil || string(data) != "updated" {
		t.Fatalf("update = %q, %v", data, err)
	}
}

func writeTestFile(file, content string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(content), 0o600)
}

type fakeInitialStep struct {
	output string
	err    error
	action func(fakeInitialTurn) error
}
type fakeInitialTurn struct {
	thread *testThread
	prompt string
	option agentruntime.TurnOptions
}
type fakeInitialRunner struct {
	starts   int
	turns    []fakeInitialTurn
	steps    []fakeInitialStep
	startErr error
}

func (runner *fakeInitialRunner) StartThread() (agentruntime.Thread, error) {
	runner.starts++
	if runner.startErr != nil {
		return nil, runner.startErr
	}
	return &testThread{ID: fmt.Sprintf("thread-%d", runner.starts)}, nil
}
func (runner *fakeInitialRunner) RunTurn(thread agentruntime.Thread, prompt string, option agentruntime.TurnOptions) (json.RawMessage, error) {
	handle, ok := thread.(*testThread)
	if !ok {
		return nil, fmt.Errorf("unexpected thread %T", thread)
	}
	turn := fakeInitialTurn{thread: handle, prompt: prompt, option: option}
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
func initDraftRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	draftGit(t, repo, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	draftGit(t, repo, "add", "-A")
	draftGit(t, repo, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "initial")
	canonical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
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
