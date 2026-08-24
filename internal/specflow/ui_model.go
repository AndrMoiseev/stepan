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
	MainActionIdea
)

type MainCommand struct {
	Action    MainAction
	Brief     string
	NeedBrief bool
}

func ParseMainCommand(input string) (MainCommand, error) {
	if input == "/idea" {
		return MainCommand{Action: MainActionIdea, NeedBrief: true}, nil
	}
	if strings.HasPrefix(input, "/idea ") {
		brief := strings.TrimPrefix(input, "/idea ")
		return MainCommand{Action: MainActionIdea, Brief: brief, NeedBrief: strings.TrimSpace(brief) == ""}, nil
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

func (action DraftAction) NeedsText() bool {
	return action == DraftQuestion || action == DraftChange
}
