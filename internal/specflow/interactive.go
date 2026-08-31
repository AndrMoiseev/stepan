package specflow

import (
	"context"
	"errors"
)

type InteractiveUI interface {
	Main() (Progress, error)
	Dialogue(context.Context, Progress) (Progress, error)
	Review(context.Context, Progress) (Progress, error)
	ReportError(error)
}

func RunInteractive(ctx context.Context, controller *Controller, ui InteractiveUI, interrupt func() error) error {
	stop := context.AfterFunc(ctx, func() { _ = interrupt() })
	defer stop()
	for {
		if err := ctx.Err(); err != nil {
			controller.Cancel()
			return err
		}
		progress, err := ui.Main()
		for err == nil && progress.State != StateIdle {
			switch progress.State {
			case StateAwaitingBrief, StateDialoguing, StateIntentPublished, StateAwaitingRework:
				progress, err = ui.Dialogue(ctx, progress)
			case StateAwaitingReview:
				progress, err = ui.Review(ctx, progress)
			default:
				err = ErrCanceled
			}
		}
		if err == nil {
			continue
		}
		if err == ErrCanceled {
			controller.Cancel()
			_ = interrupt()
			return err
		}
		if ctx.Err() != nil {
			controller.Cancel()
			return ctx.Err()
		}
		ui.ReportError(err)
	}
}

// PlanningInteractiveController is the terminal-facing application seam. Raw
// flow input is intentionally forwarded unchanged; interpretation and durable
// state transitions belong to the controller behind this interface.
type PlanningInteractiveController interface {
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
