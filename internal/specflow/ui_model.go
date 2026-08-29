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
func ParseReviewAction(input string) (ReviewAction, error) {
	switch strings.TrimSpace(strings.ToLower(input)) {
	case "apply", "/apply":
		return ReviewApply, nil
	case "reject", "/reject":
		return ReviewReject, nil
	case "rework", "/rework":
		return ReviewRework, nil
	default:
		return "", fmt.Errorf("unknown review action %q; use apply, reject, or rework", input)
	}
}
