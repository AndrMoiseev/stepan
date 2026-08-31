package specflow

import (
	"fmt"
	"strings"
)

type CommandHint struct {
	Command     string
	Description string
}

type DocumentPath struct {
	Stage Stage
	Path  string
}

func NewCommandHint(command, description string) (CommandHint, error) {
	command = strings.TrimSpace(command)
	description = strings.TrimSpace(description)
	if command == "" || description == "" {
		return CommandHint{}, fmt.Errorf("%w: command hint requires command and description", ErrInvalidDomainValue)
	}
	return CommandHint{Command: command, Description: description}, nil
}

// Progress is the controller-owned presentation contract. CommandHints are
// ordered domain data; terminal rendering is deliberately not represented.
type Progress struct {
	FlowStatus          FlowStatus
	CurrentStage        Stage
	StageStatus         StageStatus
	ReviewStatus        ReviewStatus
	CommandHints        []CommandHint
	TextAllowed         bool
	Documents           []DocumentPath
	Diagnostics         []DocumentDiagnostic
	RecoveryDiagnostics []RecoveryDiagnostic
	Blocking            []ApprovalBlocker
	Event               ControllerEvent
	Review              ReviewResult
	Revision            []RevisionAction

	Path      string
	Message   string
	Diff      string
	FeatureID string

	// State is retained only while the original intent-only controller is
	// migrated by later plan tasks. New planning-flow code must use the
	// independent fields above.
	State State
}

func NewProgress(state FlowState, hints []CommandHint, textAllowed bool) (Progress, error) {
	stageState, ok := state.Stage(state.CurrentStage())
	if !ok {
		return Progress{}, fmt.Errorf("%w: current stage has no state", ErrInvalidDomainValue)
	}
	copyHints := append([]CommandHint(nil), hints...)
	for i, hint := range copyHints {
		if strings.TrimSpace(hint.Command) == "" || strings.TrimSpace(hint.Description) == "" {
			return Progress{}, fmt.Errorf("%w: invalid command hint at index %d", ErrInvalidDomainValue, i)
		}
	}
	return Progress{
		FlowStatus:   state.Status(),
		CurrentStage: state.CurrentStage(),
		StageStatus:  stageState.Status,
		ReviewStatus: stageState.ReviewStatus,
		CommandHints: copyHints,
		TextAllowed:  textAllowed,
	}, nil
}
