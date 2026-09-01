package specflow

import (
	"context"
	"errors"
)

// PlanningInteractiveController is the terminal-facing application seam. Raw
// flow input is intentionally forwarded unchanged; interpretation and durable
// state transitions belong to the controller behind this interface.
type PlanningInteractiveController interface {
	PreflightFeature() error
	StartFeature(string) (Progress, error)
	DiscoverResumable() ([]ResumableFlow, error)
	Resume(string) (Progress, error)
	Submit(string) (Progress, error)
	Close() (Progress, error)
}

type PlanningInteractiveUI interface {
	MainPrompt() (MainCommand, error)
	ReadFeatureBrief() (string, error)
	ResumePrompt([]ResumableFlow) (string, error)
	FlowPrompt(context.Context, Progress) (string, error)
	ReportError(error)
}

// RunPlanningInteractive drives only terminal navigation. It never computes a
// transition and uses the same idempotent controller Close path for /exit,
// closed input, and context cancellation (Ctrl+C).
func RunPlanningInteractive(ctx context.Context, controller PlanningInteractiveController, ui PlanningInteractiveUI) error {
	if controller == nil || ui == nil {
		return ErrCanceled
	}
	for {
		if err := ctx.Err(); err != nil {
			_, closeErr := controller.Close()
			if closeErr != nil {
				return closeErr
			}
			return err
		}
		main, err := ui.MainPrompt()
		if err != nil {
			return err
		}

		var progress Progress
		switch main.Action {
		case MainActionFeature:
			if err = controller.PreflightFeature(); err != nil {
				break
			}
			brief := main.Brief
			if main.NeedBrief {
				brief, err = ui.ReadFeatureBrief()
				if err != nil {
					return err
				}
			}
			progress, err = controller.StartFeature(brief)
		case MainActionResume:
			var flows []ResumableFlow
			flows, err = controller.DiscoverResumable()
			if err == nil {
				var featureID string
				featureID, err = ui.ResumePrompt(flows)
				if err == nil && featureID == "" {
					continue
				}
				if err == nil {
					progress, err = controller.Resume(featureID)
				}
			}
		default:
			err = ErrCanceled
		}
		if err != nil {
			ui.ReportError(err)
			continue
		}

		for progress.Event != ControllerSessionClosed {
			if err := ctx.Err(); err != nil {
				_, closeErr := controller.Close()
				if closeErr != nil {
					return closeErr
				}
				return err
			}
			var input string
			input, err = ui.FlowPrompt(ctx, progress)
			if err != nil {
				if errors.Is(err, ErrCanceled) || errors.Is(err, ErrInterrupted) {
					progress, err = controller.Close()
					break
				}
				ui.ReportError(err)
				continue
			}
			if input == "/exit" {
				progress, err = controller.Close()
			} else {
				progress, err = controller.Submit(input)
			}
			if err != nil {
				ui.ReportError(err)
			}
		}
		if err != nil {
			ui.ReportError(err)
		}
	}
}
