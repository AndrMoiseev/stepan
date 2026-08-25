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

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
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
			wantPolicy, err := agentruntime.SingleWriteRootTurnPolicy(target)
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
	controller.state, controller.specID, controller.thread = StateReadyToWrite, "CON", &testThread{ID: "thread"}
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

func TestDraftQuestionRereadsManualEditAndReturnsToDraft(t *testing.T) {
	repo := initDraftRepository(t)
	target := filepath.Join(repo, "docs", "specs", "question-flow")
	entrypoint := filepath.Join(target, "specification.md")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"READY_TO_WRITE","spec_id":"question-flow"}`},
		{output: `{"status":"WRITTEN"}`, action: func(fakeInitialTurn) error {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.WriteFile(entrypoint, []byte("original"), 0o600)
		}},
		{output: `{"status":"ANSWERED","message":"It uses the manual version."}`, action: func(turn fakeInitialTurn) error {
			data, err := os.ReadFile(entrypoint)
			if err != nil || string(data) != "manual edit" {
				return fmt.Errorf("question did not see current disk content: %q, %v", data, err)
			}
			if turn.prompt != QuestionPrompt("Which version?") || !strings.Contains(turn.prompt, "Сначала заново прочитай текущие файлы спецификации с\nдиска") {
				return errors.New("question prompt does not require rereading disk")
			}
			if string(turn.option.OutputSchema) != string(QuestionSchema()) || turn.option.Policy != agentruntime.ReadOnlyTurnPolicy() {
				return errors.New("question turn is not strict read-only")
			}
			return nil
		}},
	}}
	controller := NewController(repo, runner)
	if _, err := controller.StartIdea("brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, []byte("manual edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	progress, err := controller.AskQuestion("Which version?")
	want := Progress{State: StateDraft, Answer: "It uses the manual version.", SpecID: "question-flow", Path: "docs/specs/question-flow/specification.md"}
	if err != nil || progress != want {
		t.Fatalf("question progress = %#v, %v, want %#v", progress, err, want)
	}
	if len(runner.turns) != 3 || runner.turns[0].thread != runner.turns[2].thread {
		t.Fatalf("question did not use a separate turn of the same thread: %#v", runner.turns)
	}
	if data, err := os.ReadFile(entrypoint); err != nil || string(data) != "manual edit" {
		t.Fatalf("question changed the specification: %q, %v", data, err)
	}
	if got := controller.Progress(); got.Answer != "" || got.State != StateDraft || got.Path != want.Path {
		t.Fatalf("answer persisted or draft menu unavailable: %#v", got)
	}
}

func TestApproveAcceptsCurrentEntrypointAndOnlyResetsMemory(t *testing.T) {
	repo := initDraftRepository(t)
	target := filepath.Join(repo, "docs", "specs", "approve-flow")
	entrypoint := filepath.Join(target, "specification.md")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"READY_TO_WRITE","spec_id":"approve-flow"}`},
		{output: `{"status":"WRITTEN"}`, action: func(fakeInitialTurn) error {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.WriteFile(entrypoint, []byte("original"), 0o600)
		}},
	}}
	controller := NewController(repo, runner)
	if _, err := controller.StartIdea("brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, []byte("manually approved content without a required template"), 0o600); err != nil {
		t.Fatal(err)
	}
	headBefore := draftGit(t, repo, "rev-parse", "HEAD")
	indexBefore, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	turnsBefore := len(runner.turns)
	progress, err := controller.Approve()
	if err != nil || progress != (Progress{State: StateIdle}) {
		t.Fatalf("approval = %#v, %v", progress, err)
	}
	if controller.thread != nil || controller.specID != "" || controller.path != "" {
		t.Fatalf("approval retained in-memory flow: thread=%v spec=%q path=%q", controller.thread, controller.specID, controller.path)
	}
	if len(runner.turns) != turnsBefore {
		t.Fatal("approval launched Codex")
	}
	if data, err := os.ReadFile(entrypoint); err != nil || string(data) != "manually approved content without a required template" {
		t.Fatalf("approval rewrote current content: %q, %v", data, err)
	}
	indexAfter, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil || !bytes.Equal(indexBefore, indexAfter) || draftGit(t, repo, "rev-parse", "HEAD") != headBefore {
		t.Fatalf("approval changed HEAD or index: %v", err)
	}
	for _, marker := range []string{filepath.Join(repo, ".stepan"), filepath.Join(target, "approved"), filepath.Join(target, ".approved")} {
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Fatalf("approval marker/state exists at %q: %v", marker, err)
		}
	}
}

func TestApproveRequiresCurrentRegularEntrypoint(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(string) error
	}{
		{"deleted", os.Remove},
		{"directory", func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Mkdir(path, 0o700)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initDraftRepository(t)
			target := filepath.Join(repo, "docs", "specs", "approve-flow")
			entrypoint := filepath.Join(target, "specification.md")
			runner := &fakeInitialRunner{steps: []fakeInitialStep{
				{output: `{"status":"READY_TO_WRITE","spec_id":"approve-flow"}`},
				{output: `{"status":"WRITTEN"}`, action: func(fakeInitialTurn) error {
					if err := os.MkdirAll(target, 0o700); err != nil {
						return err
					}
					return os.WriteFile(entrypoint, []byte("draft"), 0o600)
				}},
			}}
			controller := NewController(repo, runner)
			if _, err := controller.StartIdea("brief"); err != nil {
				t.Fatal(err)
			}
			if _, err := controller.CreateDraft(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := test.change(entrypoint); err != nil {
				t.Fatal(err)
			}
			turnsBefore := len(runner.turns)
			progress, err := controller.Approve()
			if err == nil || progress.State != StateDraft || progress.Path == "" {
				t.Fatalf("invalid approval = %#v, %v", progress, err)
			}
			if len(runner.turns) != turnsBefore {
				t.Fatal("failed approval launched Codex")
			}
		})
	}
}

func TestQuestionInvalidResultEndsFlowWithoutRetry(t *testing.T) {
	repo := initDraftRepository(t)
	target := filepath.Join(repo, "docs", "specs", "question-flow")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"READY_TO_WRITE","spec_id":"question-flow"}`},
		{output: `{"status":"WRITTEN"}`, action: func(fakeInitialTurn) error {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(target, "specification.md"), []byte("draft"), 0o600)
		}},
		{output: `{"status":"WRITTEN"}`},
	}}
	controller := NewController(repo, runner)
	if _, err := controller.StartIdea("brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	if progress, err := controller.AskQuestion("question"); err == nil || progress.State != StateIdle || len(runner.turns) != 3 {
		t.Fatalf("invalid question result = %#v, %v; turns=%d", progress, err, len(runner.turns))
	}
}

func TestDirectChangeUsesFreshBaselineAndReturnsToDraft(t *testing.T) {
	repo := initDraftRepository(t)
	target := filepath.Join(repo, "docs", "specs", "change-flow")
	entrypoint := filepath.Join(target, "specification.md")
	manualOutside := filepath.Join(repo, "manual-before-update.txt")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"READY_TO_WRITE","spec_id":"change-flow"}`},
		{output: `{"status":"WRITTEN"}`, action: writeTurnFile(entrypoint, "original")},
		{output: `{"status":"READY_TO_UPDATE"}`, action: func(turn fakeInitialTurn) error {
			data, err := os.ReadFile(entrypoint)
			if err != nil || string(data) != "manual edit before analysis" {
				return fmt.Errorf("analysis did not read current specification: %q, %v", data, err)
			}
			if turn.prompt != ChangePrompt("Apply direct change") || string(turn.option.OutputSchema) != string(ChangeSchema()) || turn.option.Policy != agentruntime.ReadOnlyTurnPolicy() {
				return errors.New("first change analysis is not strict read-only")
			}
			// Simulate an unrelated user edit while analysis is running. The update
			// baseline must be captured after READY_TO_UPDATE and include it.
			return os.WriteFile(manualOutside, []byte("manual baseline"), 0o600)
		}},
		{output: `{"status":"UPDATED"}`, action: func(turn fakeInitialTurn) error {
			if turn.prompt != UpdatePrompt(target) || string(turn.option.OutputSchema) != string(UpdateSchema()) {
				return errors.New("wrong update prompt or schema")
			}
			policy, err := agentruntime.SingleWriteRootTurnPolicy(target)
			if err != nil || turn.option.Policy != policy {
				return errors.New("update did not receive exactly one write root")
			}
			return os.WriteFile(entrypoint, []byte("updated"), 0o600)
		}},
	}}
	controller := NewController(repo, runner)
	if _, err := controller.StartIdea("brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, []byte("manual edit before analysis"), 0o600); err != nil {
		t.Fatal(err)
	}
	progress, err := controller.ProposeChange(context.Background(), "Apply direct change")
	want := Progress{State: StateDraft, SpecID: "change-flow", Path: "docs/specs/change-flow/specification.md"}
	if err != nil || progress != want {
		t.Fatalf("direct update = %#v, %v, want %#v", progress, err, want)
	}
	if data, err := os.ReadFile(entrypoint); err != nil || string(data) != "updated" {
		t.Fatalf("specification was not updated: %q, %v", data, err)
	}
	if data, err := os.ReadFile(manualOutside); err != nil || string(data) != "manual baseline" {
		t.Fatalf("manual baseline was changed: %q, %v", data, err)
	}
	if len(runner.turns) != 4 || runner.turns[0].thread != runner.turns[3].thread {
		t.Fatalf("change did not use separate turns of the same thread: %#v", runner.turns)
	}
}

func TestChangeClarificationUsesReadOnlyTurnsThenUpdates(t *testing.T) {
	repo := initDraftRepository(t)
	target := filepath.Join(repo, "docs", "specs", "clarified-change")
	entrypoint := filepath.Join(target, "specification.md")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"READY_TO_WRITE","spec_id":"clarified-change"}`},
		{output: `{"status":"WRITTEN"}`, action: writeTurnFile(entrypoint, "original")},
		{output: `{"status":"NEEDS_INPUT","message":"Which color?"}`, action: assertChangeAnalysis(entrypoint, "manual before first analysis", "CHANGE REQUEST:\nChange the color")},
		{output: `{"status":"READY_TO_UPDATE"}`, action: assertChangeAnalysis(entrypoint, "manual before clarification answer", "USER ANSWER:\nBlue")},
		{output: `{"status":"UPDATED"}`, action: writeTurnFile(entrypoint, "blue")},
	}}
	controller := NewController(repo, runner)
	if _, err := controller.StartIdea("brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, []byte("manual before first analysis"), 0o600); err != nil {
		t.Fatal(err)
	}
	progress, err := controller.ProposeChange(context.Background(), "Change the color")
	wantQuestion := Progress{State: StateAwaitingChangeAnswer, Question: "Which color?", SpecID: "clarified-change", Path: "docs/specs/clarified-change/specification.md"}
	if err != nil || progress != wantQuestion {
		t.Fatalf("change question = %#v, %v, want %#v", progress, err, wantQuestion)
	}
	if len(runner.turns) != 3 {
		t.Fatalf("write turn ran before READY_TO_UPDATE: %d turns", len(runner.turns))
	}
	if err := os.WriteFile(entrypoint, []byte("manual before clarification answer"), 0o600); err != nil {
		t.Fatal(err)
	}
	progress, err = controller.SubmitChangeAnswer(context.Background(), "Blue")
	wantDraft := Progress{State: StateDraft, SpecID: "clarified-change", Path: "docs/specs/clarified-change/specification.md"}
	if err != nil || progress != wantDraft {
		t.Fatalf("clarified update = %#v, %v, want %#v", progress, err, wantDraft)
	}
	if len(runner.turns) != 5 || runner.turns[0].thread != runner.turns[4].thread {
		t.Fatalf("clarified update did not keep the thread: %#v", runner.turns)
	}
	if data, err := os.ReadFile(entrypoint); err != nil || string(data) != "blue" {
		t.Fatalf("clarified update did not write: %q, %v", data, err)
	}
}

func TestUpdateFailureKeepsPartialFilesAndEndsFlow(t *testing.T) {
	for _, test := range []struct {
		name      string
		output    string
		action    func(repo, target, entrypoint string) error
		wantError string
	}{
		{"outside write", `{"status":"UPDATED"}`, func(repo, target, entrypoint string) error {
			if err := os.WriteFile(entrypoint, []byte("partial update"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(repo, "outside-update.txt"), []byte("outside"), 0o600)
		}, "outside-update.txt"},
		{"missing entrypoint", `{"status":"UPDATED"}`, func(_, _, entrypoint string) error { return os.Remove(entrypoint) }, "entrypoint"},
		{"invalid updated envelope", `{"status":"WRITTEN"}`, func(_, _, entrypoint string) error {
			return os.WriteFile(entrypoint, []byte("partial update"), 0o600)
		}, "invalid status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initDraftRepository(t)
			target := filepath.Join(repo, "docs", "specs", "failed-update")
			entrypoint := filepath.Join(target, "specification.md")
			runner := &fakeInitialRunner{steps: []fakeInitialStep{
				{output: `{"status":"READY_TO_WRITE","spec_id":"failed-update"}`},
				{output: `{"status":"WRITTEN"}`, action: writeTurnFile(entrypoint, "original")},
				{output: `{"status":"READY_TO_UPDATE"}`},
				{output: test.output, action: func(fakeInitialTurn) error { return test.action(repo, target, entrypoint) }},
			}}
			controller := NewController(repo, runner)
			if _, err := controller.StartIdea("brief"); err != nil {
				t.Fatal(err)
			}
			if _, err := controller.CreateDraft(context.Background()); err != nil {
				t.Fatal(err)
			}
			progress, err := controller.ProposeChange(context.Background(), "change")
			if err == nil || !strings.Contains(err.Error(), test.wantError) || !strings.Contains(err.Error(), "cleanup was not performed") || progress.State != StateIdle {
				t.Fatalf("failed update = %#v, %v", progress, err)
			}
			if test.name != "missing entrypoint" {
				if data, err := os.ReadFile(entrypoint); err != nil || string(data) != "partial update" {
					t.Fatalf("partial update was removed: %q, %v", data, err)
				}
			} else if info, err := os.Lstat(target); err != nil || !info.IsDir() {
				t.Fatalf("partial target directory was removed: %v, %v", info, err)
			}
		})
	}
}

func TestInvalidChangeAnalysisEndsFlowWithoutUpdate(t *testing.T) {
	repo := initDraftRepository(t)
	target := filepath.Join(repo, "docs", "specs", "invalid-analysis")
	entrypoint := filepath.Join(target, "specification.md")
	runner := &fakeInitialRunner{steps: []fakeInitialStep{
		{output: `{"status":"READY_TO_WRITE","spec_id":"invalid-analysis"}`},
		{output: `{"status":"WRITTEN"}`, action: writeTurnFile(entrypoint, "draft")},
		{output: `{"status":"UPDATED"}`},
	}}
	controller := NewController(repo, runner)
	if _, err := controller.StartIdea("brief"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateDraft(context.Background()); err != nil {
		t.Fatal(err)
	}
	progress, err := controller.ProposeChange(context.Background(), "change")
	if err == nil || progress.State != StateIdle || len(runner.turns) != 3 {
		t.Fatalf("invalid analysis = %#v, %v; turns=%d", progress, err, len(runner.turns))
	}
	if data, err := os.ReadFile(entrypoint); err != nil || string(data) != "draft" {
		t.Fatalf("invalid analysis changed draft: %q, %v", data, err)
	}
}

func writeTurnFile(path, content string) func(fakeInitialTurn) error {
	return func(fakeInitialTurn) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o600)
	}
}

func assertChangeAnalysis(entrypoint, content, suffix string) func(fakeInitialTurn) error {
	return func(turn fakeInitialTurn) error {
		data, err := os.ReadFile(entrypoint)
		if err != nil || string(data) != content {
			return fmt.Errorf("analysis did not reread current file: %q, %v", data, err)
		}
		if !strings.HasSuffix(turn.prompt, suffix) || !strings.Contains(turn.prompt, "заново прочитай текущие файлы спецификации") {
			return errors.New("analysis prompt does not reread current specification")
		}
		if string(turn.option.OutputSchema) != string(ChangeSchema()) || turn.option.Policy != agentruntime.ReadOnlyTurnPolicy() {
			return errors.New("change analysis is not strict read-only")
		}
		return nil
	}
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
	starts   []string
	turns    []fakeInitialTurn
	steps    []fakeInitialStep
	startErr error
}

func (runner *fakeInitialRunner) StartThread() (agentruntime.Thread, error) {
	id := "thread-" + string(rune('1'+len(runner.starts)))
	runner.starts = append(runner.starts, id)
	if runner.startErr != nil {
		return nil, runner.startErr
	}
	return &testThread{ID: id}, nil
}

func (runner *fakeInitialRunner) RunTurn(thread agentruntime.Thread, prompt string, option agentruntime.TurnOptions) (json.RawMessage, error) {
	codexThread, ok := thread.(*testThread)
	if !ok {
		return nil, fmt.Errorf("unexpected thread type %T", thread)
	}
	turn := fakeInitialTurn{thread: codexThread, prompt: prompt, option: option}
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
		if turn.option.Policy != agentruntime.ReadOnlyTurnPolicy() {
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
