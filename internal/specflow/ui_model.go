package specflow

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrCanceled    = errors.New("interactive input canceled")
	ErrInterrupted = errors.New("interactive input interrupted")
)

type MainAction uint8

const (
	MainActionNone MainAction = iota
	MainActionFeature
	MainActionResume
)

type MainCommand struct {
	Action    MainAction
	Brief     string
	NeedBrief bool
}

func ParseMainCommand(input string) (MainCommand, error) {
	input = strings.TrimSpace(input)
	if input == "/feature" {
		return MainCommand{Action: MainActionFeature, NeedBrief: true}, nil
	}
	if strings.HasPrefix(input, "/feature ") {
		brief := strings.TrimSpace(strings.TrimPrefix(input, "/feature "))
		return MainCommand{Action: MainActionFeature, Brief: brief, NeedBrief: strings.TrimSpace(brief) == ""}, nil
	}
	if input == "/resume" {
		return MainCommand{Action: MainActionResume}, nil
	}
	return MainCommand{}, fmt.Errorf("unknown command %q", input)
}

// ResumeRow is a stable, one-based presentation identity. Feature IDs remain
// controller identities; the row number exists only for one rendered table.
type ResumeRow struct {
	Number int
	Flow   ResumableFlow
}

// ResumeRows applies the product-level visibility boundary defensively. The
// repository already filters discovery results, but terminal callers may also
// provide snapshots directly (for example in tests or another front end).
func ResumeRows(flows []ResumableFlow) []ResumeRow {
	rows := make([]ResumeRow, 0, len(flows))
	for _, flow := range flows {
		if flow.FlowStatus != "" && flow.FlowStatus != FlowActive {
			continue
		}
		if flow.CurrentStage == StagePlan && flow.StageStatus == StageCommitted {
			continue
		}
		rows = append(rows, ResumeRow{Number: len(rows) + 1, Flow: flow})
	}
	return rows
}

func SelectResumeRow(rows []ResumeRow, input string) (ResumableFlow, error) {
	input = strings.TrimSpace(input)
	for _, row := range rows {
		if input == fmt.Sprint(row.Number) {
			return row.Flow, nil
		}
	}
	return ResumableFlow{}, fmt.Errorf("resume selection %q does not identify an available flow", input)
}

func commandAllowed(progress Progress, input string) bool {
	for _, hint := range progress.CommandHints {
		if input == hint.Command {
			return true
		}
	}
	return false
}

func revisionAllowed(progress Progress, input string) bool {
	for _, action := range progress.Revision {
		if input == string(action) {
			return true
		}
	}
	return false
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
