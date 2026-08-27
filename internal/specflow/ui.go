package specflow

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

type UI struct {
	controller      *Controller
	input           *bufio.Reader
	output          io.Writer
	lastDraftAnswer string
}

func NewUI(controller *Controller) *UI {
	return &UI{controller: controller, input: bufio.NewReader(os.Stdin), output: os.Stdout}
}

func (ui *UI) ReportError(err error) { ui.say("Ошибка: " + err.Error()) }

func (ui *UI) Main() (Progress, error) {
	for {
		input, err := ui.readLine("Вы > ")
		if err != nil {
			return Progress{}, err
		}
		command, err := ParseMainCommand(input)
		if err != nil {
			ui.say(err.Error())
			continue
		}
		ui.lastDraftAnswer = ""
		ui.thinking()
		if !command.NeedBrief {
			return ui.controller.StartIdea(command.Brief)
		}
		progress, err := ui.controller.StartIdea("")
		if err != nil {
			return progress, err
		}
		ui.say("Опишите идею.")
		brief, err := ui.text()
		if err != nil {
			return progress, err
		}
		ui.thinking()
		return ui.controller.Submit(brief)
	}
}

func (ui *UI) InitialAnswer(question string) (Progress, error) {
	ui.say(question)
	answer, err := ui.text()
	if err != nil {
		return ui.controller.Progress(), err
	}
	ui.thinking()
	return ui.controller.Submit(answer)
}

func (ui *UI) Draft(ctx context.Context, progress Progress) (Progress, error) {
	if progress.Answer != "" && progress.Answer != ui.lastDraftAnswer {
		ui.say(progress.Answer)
		ui.lastDraftAnswer = progress.Answer
	}
	ui.say(draftTitle(progress))
	action, err := ui.draftAction()
	if err != nil {
		return ui.controller.Progress(), err
	}
	if action == DraftApprove {
		return ui.controller.Approve()
	}
	if action == DraftQuestion {
		ui.say("Введите вопрос к спецификации.")
	} else {
		ui.say("Опишите изменение.")
	}
	value, err := ui.text()
	if err != nil {
		return ui.controller.Progress(), err
	}
	ui.thinking()
	if action == DraftQuestion {
		return ui.controller.AskQuestion(value)
	}
	return ui.controller.ProposeChange(ctx, value)
}

func (ui *UI) ChangeAnswer(ctx context.Context, question string) (Progress, error) {
	ui.say(question)
	answer, err := ui.text()
	if err != nil {
		return ui.controller.Progress(), err
	}
	ui.thinking()
	return ui.controller.SubmitChangeAnswer(ctx, answer)
}

func (ui *UI) draftAction() (DraftAction, error) {
	input, err := ui.readLine("Вы > [/approve | /question | /change] ")
	if err != nil {
		return DraftApprove, err
	}
	return ParseDraftAction(input)
}

func (ui *UI) text() (string, error) { return ui.readLine("Вы > ") }

func (ui *UI) readLine(prompt string) (string, error) {
	_, _ = fmt.Fprint(ui.output, prompt)
	value, err := ui.input.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	if err == io.EOF && value == "" {
		return "", ErrCanceled
	}
	return value, nil
}

func (ui *UI) say(message string) {
	_, _ = fmt.Fprintf(ui.output, "\nStepan > %s\n\n", message)
}

func (ui *UI) thinking() { _, _ = fmt.Fprint(ui.output, "\nStepan думает…\n\n") }

func draftTitle(progress Progress) string {
	return "Черновик спецификации: " + progress.Path
}
