package specflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"charm.land/huh/v2"
)

type UI struct {
	controller *Controller
	accessible bool
	input      io.Reader
	output     io.Writer
}

func NewUI(controller *Controller) *UI {
	_, accessible := os.LookupEnv("ACCESSIBLE")
	return &UI{controller: controller, accessible: accessible, input: os.Stdin, output: os.Stdout}
}

func (ui *UI) Main() (Progress, error) {
	for {
		var input string
		if err := ui.run(huh.NewInput().Title("Command").Value(&input)); err != nil {
			return Progress{}, err
		}
		command, err := ParseMainCommand(input)
		if err != nil {
			_, _ = fmt.Fprintln(ui.output, err)
			continue
		}
		if !command.NeedBrief {
			return ui.controller.StartIdea(command.Brief)
		}
		progress, err := ui.controller.StartIdea("")
		if err != nil {
			return progress, err
		}
		brief, err := ui.text("Idea brief")
		if err != nil {
			return progress, err
		}
		return ui.controller.Submit(brief)
	}
}

func (ui *UI) InitialAnswer(question string) (Progress, error) {
	answer, err := ui.text(question)
	if err != nil {
		return ui.controller.Progress(), err
	}
	return ui.controller.Submit(answer)
}

func (ui *UI) Draft(ctx context.Context, progress Progress) (Progress, error) {
	action, err := ui.draftMenu(progress)
	if err != nil {
		return ui.controller.Progress(), err
	}
	if action == DraftApprove {
		return ui.controller.Approve()
	}
	value, err := ui.text(map[DraftAction]string{DraftQuestion: "Question", DraftChange: "Change request"}[action])
	if err != nil {
		return ui.controller.Progress(), err
	}
	if action == DraftQuestion {
		return ui.controller.AskQuestion(value)
	}
	return ui.controller.ProposeChange(ctx, value)
}

func (ui *UI) ChangeAnswer(ctx context.Context, question string) (Progress, error) {
	answer, err := ui.text(question)
	if err != nil {
		return ui.controller.Progress(), err
	}
	return ui.controller.SubmitChangeAnswer(ctx, answer)
}

func (ui *UI) draftMenu(progress Progress) (DraftAction, error) {
	action := DraftApprove
	field := huh.NewSelect[DraftAction]().
		Title(draftTitle(progress)).
		Description(progress.Answer).
		Options(
			huh.NewOption("/approve", DraftApprove),
			huh.NewOption("Ask a question", DraftQuestion),
			huh.NewOption("Propose a change", DraftChange),
		).
		Value(&action)
	return action, ui.run(field)
}

func (ui *UI) text(title string) (string, error) {
	var value string
	err := ui.run(huh.NewText().Title(title).Lines(5).ExternalEditor(false).Value(&value))
	return value, err
}

func (ui *UI) run(fields ...huh.Field) error {
	err := huh.NewForm(huh.NewGroup(fields...)).
		WithAccessible(ui.accessible).
		WithInput(ui.input).
		WithOutput(ui.output).
		Run()
	return normalizeFormError(err)
}

func draftTitle(progress Progress) string {
	return "Specification: " + progress.Path
}

func normalizeFormError(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrCanceled
	}
	return err
}
