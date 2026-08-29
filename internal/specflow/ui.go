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
	controller *Controller
	input      *bufio.Reader
	output     io.Writer
}

func NewUI(c *Controller) *UI {
	return &UI{controller: c, input: bufio.NewReader(os.Stdin), output: os.Stdout}
}
func (u *UI) ReportError(err error) { u.say("Ошибка: " + err.Error()) }
func (u *UI) Main() (Progress, error) {
	for {
		value, err := u.readLine("Вы > ")
		if err != nil {
			return Progress{}, err
		}
		command, err := ParseMainCommand(value)
		if err != nil {
			u.say(err.Error())
			continue
		}
		if command.NeedBrief {
			u.say("Опишите намерение.")
			brief, err := u.text()
			if err != nil {
				return Progress{}, err
			}
			u.thinking()
			return u.controller.StartFeature(brief)
		}
		u.thinking()
		return u.controller.StartFeature(command.Brief)
	}
}
func (u *UI) Dialogue(_ context.Context, p Progress) (Progress, error) {
	if p.Message != "" {
		u.say(p.Message)
	}
	if p.State == StateIntentPublished {
		u.say("Intent опубликован: " + p.Path + ". Продолжайте диалог или введите /approve.")
	} else if p.State == StateAwaitingBrief {
		u.say("Опишите намерение.")
	} else if p.State == StateAwaitingRework {
		u.say("Опишите доработку draft.")
	}
	value, err := u.text()
	if err != nil {
		return p, err
	}
	u.thinking()
	return u.controller.Submit(value)
}
func (u *UI) Review(_ context.Context, p Progress) (Progress, error) {
	u.say("Новый draft intent:\n" + p.Diff)
	value, err := u.readLine("Вы > [apply | reject | rework] ")
	if err != nil {
		return p, err
	}
	action, err := ParseReviewAction(value)
	if err != nil {
		u.say(err.Error())
		return p, nil
	}
	return u.controller.Review(action)
}
func (u *UI) text() (string, error) { return u.readLine("Вы > ") }
func (u *UI) readLine(prompt string) (string, error) {
	_, _ = fmt.Fprint(u.output, prompt)
	value, err := u.input.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	if err == io.EOF && value == "" {
		return "", ErrCanceled
	}
	return value, nil
}
func (u *UI) say(message string) { _, _ = fmt.Fprintf(u.output, "\nStepan > %s\n\n", message) }
func (u *UI) thinking()          { _, _ = fmt.Fprint(u.output, "\nStepan думает…\n\n") }
