package specflow

import "context"

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
