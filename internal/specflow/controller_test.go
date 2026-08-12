package specflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	controller := NewController(runner)

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
	controller := NewController(runner)

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
			controller := NewController(runner)
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
	controller := NewController(runner)
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

type fakeInitialStep struct {
	output string
	err    error
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
	runner.turns = append(runner.turns, fakeInitialTurn{thread: thread, prompt: prompt, option: option})
	if len(runner.turns) > len(runner.steps) {
		return nil, errors.New("unexpected turn")
	}
	step := runner.steps[len(runner.turns)-1]
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
