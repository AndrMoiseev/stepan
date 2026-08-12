package specflow

import (
	"context"
	"errors"
)

type InteractiveUI interface {
	Main() (Progress, error)
	InitialAnswer(string) (Progress, error)
	Draft(context.Context, Progress) (Progress, error)
	ChangeAnswer(context.Context, string) (Progress, error)
	ReportError(error)
}

func RunInteractive(ctx context.Context, controller *Controller, ui InteractiveUI, interrupt func() error) error {
	stopInterrupt := context.AfterFunc(ctx, func() { _ = interrupt() })
	defer stopInterrupt()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress, err := ui.Main()
		for err == nil && progress.State != StateIdle {
			switch progress.State {
			case StateAwaitingAnswer:
				progress, err = ui.InitialAnswer(progress.Question)
			case StateReadyToWrite:
				progress, err = controller.CreateDraft(ctx)
			case StateDraft:
				progress, err = ui.Draft(ctx, progress)
			case StateAwaitingChangeAnswer:
				progress, err = ui.ChangeAnswer(ctx, progress.Question)
			default:
				err = errors.New("unsupported interactive flow state")
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, ErrCanceled) {
			_ = interrupt()
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ui.ReportError(err)
	}
}
