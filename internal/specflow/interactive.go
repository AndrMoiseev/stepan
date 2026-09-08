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

type PlanningActivityUI interface {
	BeginActivity(string) func()
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
			stop := beginPlanningActivity(ui, "Автор начинает feature flow")
			progress, err = controller.StartFeature(brief)
			stop()
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
					stop := beginPlanningActivity(ui, "Stepan восстанавливает feature flow")
					progress, err = controller.Resume(featureID)
					stop()
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
				stop := beginPlanningActivity(ui, planningActivityLabel(input))
				progress, err = controller.Submit(input)
				stop()
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

func beginPlanningActivity(ui PlanningInteractiveUI, label string) func() {
	activity, ok := ui.(PlanningActivityUI)
	if !ok {
		return func() {}
	}
	return activity.BeginActivity(label)
}

func planningActivityLabel(input string) string {
	switch input {
	case "/review":
		return "Ревьюер проверяет документ"
	case "/apply":
		return "Автор применяет замечания ревью"
	case "/approve":
		return "Stepan фиксирует утверждение стадии"
	case "/revise-spec":
		return "Stepan возвращает спецификацию в работу"
	case "/status":
		return "Stepan обновляет состояние flow"
	default:
		return "Автор продолжает работу"
	}
}
