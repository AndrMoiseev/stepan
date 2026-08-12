package specflow

import (
	"encoding/json"
	"fmt"

	"github.com/AndrMoiseev/stepan/internal/codexapp"
)

type State uint8

const (
	StateIdle State = iota
	StateAwaitingBrief
	StateAwaitingAnswer
	StateReadyToWrite
)

type Progress struct {
	State    State
	Question string
	SpecID   string
}

type initialTurnRunner interface {
	StartThread() (*codexapp.Thread, error)
	RunTurn(*codexapp.Thread, string, codexapp.TurnOptions) (json.RawMessage, error)
}

type Controller struct {
	runner   initialTurnRunner
	state    State
	brief    string
	thread   *codexapp.Thread
	question string
	specID   string
}

func NewController(runner initialTurnRunner) *Controller {
	return &Controller{runner: runner}
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
	return Progress{State: controller.state, Question: controller.question, SpecID: controller.specID}
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
