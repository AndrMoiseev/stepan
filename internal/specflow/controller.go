package specflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/AndrMoiseev/stepan/internal/codexapp"
	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
)

type State uint8

const (
	StateIdle State = iota
	StateAwaitingBrief
	StateAwaitingAnswer
	StateReadyToWrite
	StateDraft
)

type Progress struct {
	State    State
	Question string
	Answer   string
	SpecID   string
	Path     string
}

type initialTurnRunner interface {
	StartThread() (*codexapp.Thread, error)
	RunTurn(*codexapp.Thread, string, codexapp.TurnOptions) (json.RawMessage, error)
}

type Controller struct {
	runner   initialTurnRunner
	root     string
	state    State
	brief    string
	thread   *codexapp.Thread
	question string
	specID   string
	path     string
}

func NewController(root string, runner initialTurnRunner) *Controller {
	return &Controller{root: root, runner: runner}
}

func (controller *Controller) StartIdea(brief string) (Progress, error) {
	controller.reset()
	if controller.runner == nil {
		return controller.Progress(), fmt.Errorf("start idea: turn runner is required")
	}
	thread, err := controller.runner.StartThread()
	if err != nil {
		return controller.Progress(), fmt.Errorf("start idea thread: %w", err)
	}
	controller.thread = thread
	controller.brief = brief
	if brief == "" {
		controller.state = StateAwaitingBrief
		return controller.Progress(), nil
	}
	return controller.run(InitialPrompt(brief))
}

func (controller *Controller) Submit(text string) (Progress, error) {
	switch controller.state {
	case StateAwaitingBrief:
		controller.brief = text
		return controller.run(InitialPrompt(text))
	case StateAwaitingAnswer:
		return controller.run(initialAnswerPrompt(text))
	default:
		return controller.Progress(), fmt.Errorf("initial clarification is not awaiting input")
	}
}

func (controller *Controller) Progress() Progress {
	return Progress{State: controller.state, Question: controller.question, SpecID: controller.specID, Path: controller.path}
}

func (controller *Controller) AskQuestion(question string) (Progress, error) {
	if controller.state != StateDraft {
		return controller.Progress(), fmt.Errorf("specification draft is not available")
	}
	output, err := controller.runner.RunTurn(controller.thread, QuestionPrompt(question), codexapp.TurnOptions{
		OutputSchema: QuestionSchema(),
		Policy:       codexapp.ReadOnlyTurnPolicy(),
	})
	if err != nil {
		controller.reset()
		return controller.Progress(), fmt.Errorf("ask specification question: %w", err)
	}
	result, err := DecodeQuestionResult(output)
	if err != nil {
		controller.reset()
		return controller.Progress(), fmt.Errorf("validate specification answer: %w", err)
	}
	progress := controller.Progress()
	progress.Answer = result.Message
	return progress, nil
}

func (controller *Controller) Approve() (Progress, error) {
	if controller.state != StateDraft {
		return controller.Progress(), fmt.Errorf("specification draft is not available")
	}
	entrypoint := filepath.Join(controller.root, filepath.FromSlash(controller.path))
	info, err := os.Lstat(entrypoint)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return controller.Progress(), fmt.Errorf("approve specification entrypoint %q: %w", entrypoint, err)
	}
	controller.reset()
	return controller.Progress(), nil
}

func (controller *Controller) CreateDraft(ctx context.Context) (Progress, error) {
	if controller.state != StateReadyToWrite {
		return controller.Progress(), fmt.Errorf("initial clarification is not ready to write")
	}
	target, err := PrepareSpecTarget(controller.root, controller.specID)
	if err != nil {
		return controller.failDraft(fmt.Errorf("prepare specification target: %w", err))
	}
	policy, err := codexapp.SingleWriteRootTurnPolicy(target.Directory)
	if err != nil {
		return controller.failDraft(fmt.Errorf("prepare write policy: %w", err))
	}
	baseline, err := gitsnapshot.Capture(ctx, controller.root)
	if err != nil {
		return controller.failDraft(fmt.Errorf("capture pre-write repository: %w", err))
	}

	output, turnErr := controller.runner.RunTurn(controller.thread, CreatePrompt(target.Directory), codexapp.TurnOptions{
		OutputSchema: CreateSchema(),
		Policy:       policy,
	})
	var postErrors []error
	if turnErr != nil {
		postErrors = append(postErrors, fmt.Errorf("run create turn: %w", turnErr))
	} else if _, err := DecodeCreateResult(output); err != nil {
		postErrors = append(postErrors, fmt.Errorf("validate create result: %w", err))
	}
	if err := CheckContainment(controller.root, target.Directory); err != nil {
		postErrors = append(postErrors, fmt.Errorf("verify specification target: %w", err))
	}
	if info, err := os.Lstat(target.Entrypoint); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		postErrors = append(postErrors, fmt.Errorf("verify specification entrypoint %q: %w", target.Entrypoint, err))
	}
	if after, err := gitsnapshot.Capture(ctx, controller.root); err != nil {
		postErrors = append(postErrors, fmt.Errorf("capture post-write repository: %w", err))
	} else if changed, err := gitsnapshot.Compare(ctx, controller.root, baseline, after); err != nil {
		postErrors = append(postErrors, fmt.Errorf("compare repository snapshots: %w", err))
	} else if err := gitsnapshot.CheckBoundary(changed, path.Join("docs", "specs", controller.specID)); err != nil {
		postErrors = append(postErrors, err)
	}
	if err := errors.Join(postErrors...); err != nil {
		return controller.failDraft(err)
	}

	controller.state = StateDraft
	controller.path = target.DisplayPath
	return controller.Progress(), nil
}

func (controller *Controller) failDraft(err error) (Progress, error) {
	controller.reset()
	return controller.Progress(), fmt.Errorf("create draft failed; cleanup was not performed: %w", err)
}

func (controller *Controller) run(prompt string) (Progress, error) {
	output, err := controller.runner.RunTurn(controller.thread, prompt, codexapp.TurnOptions{
		OutputSchema: InitialSchema(),
		Policy:       codexapp.ReadOnlyTurnPolicy(),
	})
	if err != nil {
		controller.reset()
		return controller.Progress(), fmt.Errorf("run initial clarification: %w", err)
	}
	result, err := DecodeInitialResult(output)
	if err != nil {
		controller.reset()
		return controller.Progress(), fmt.Errorf("validate initial clarification: %w", err)
	}
	controller.question = ""
	controller.specID = ""
	if result.Status == StatusNeedsInput {
		controller.state = StateAwaitingAnswer
		controller.question = result.Message
	} else {
		controller.state = StateReadyToWrite
		controller.specID = result.SpecID
	}
	return controller.Progress(), nil
}

func (controller *Controller) reset() {
	controller.state = StateIdle
	controller.brief = ""
	controller.thread = nil
	controller.question = ""
	controller.specID = ""
	controller.path = ""
}

func initialAnswerPrompt(answer string) string {
	return `Продолжи первоначальное уточнение идеи с учётом предыдущего диалога.
Не спрашивай о фактах, которые можно надёжно установить из кода и документации.
Не создавай и не изменяй файлы на этапе уточнения.

Уточняй только материальные решения, влияющие на поведение, границы или критерии
приёмки. Задавай не более одного вопроса за turn.

Когда информации достаточно, верни READY_TO_WRITE и предложи краткий spec_id
в формате [a-z0-9-]+. До отдельного разрешения Stepan ничего не записывай.

USER ANSWER:
` + answer
}
