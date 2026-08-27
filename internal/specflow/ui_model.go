package specflow

import (
	"errors"
	"fmt"
	"strings"
)

var ErrCanceled = errors.New("interactive input canceled")

type MainAction uint8

const (
	MainActionNone MainAction = iota
	MainActionFeature
)

type MainCommand struct {
	Action    MainAction
	Brief     string
	NeedBrief bool
}

func ParseMainCommand(input string) (MainCommand, error) {
	if input == "/feature" {
		return MainCommand{Action: MainActionFeature, NeedBrief: true}, nil
	}
	if strings.HasPrefix(input, "/feature ") {
		brief := strings.TrimPrefix(input, "/feature ")
		return MainCommand{Action: MainActionFeature, Brief: brief, NeedBrief: strings.TrimSpace(brief) == ""}, nil
	}
	return MainCommand{}, fmt.Errorf("unknown command %q", input)
}

type DraftAction string

const (
	DraftApprove  DraftAction = "/approve"
	DraftQuestion DraftAction = "question"
	DraftChange   DraftAction = "change"
)

func DraftActions() []DraftAction {
	return []DraftAction{DraftApprove, DraftQuestion, DraftChange}
}

func ParseDraftAction(input string) (DraftAction, error) {
	switch strings.TrimSpace(input) {
	case "/approve":
		return DraftApprove, nil
	case "/question", "question":
		return DraftQuestion, nil
	case "/change", "change":
		return DraftChange, nil
	default:
		return "", fmt.Errorf("unknown draft action %q; use /approve, /question, or /change", input)
	}
}

func (action DraftAction) NeedsText() bool {
	return action == DraftQuestion || action == DraftChange
}
