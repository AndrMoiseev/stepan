package specflow

import (
	"context"
	"errors"
	"testing"
)

func TestRunInteractiveReturnsToMainAfterFlowError(t *testing.T) {
	flowErr := errors.New("server crashed")
	ui := &fakeInteractiveUI{main: []uiResult{{err: flowErr}, {err: ErrCanceled}}}
	interrupts := 0
	err := RunInteractive(context.Background(), nil, ui, func() error { interrupts++; return nil })
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("run error = %v", err)
	}
	if len(ui.reported) != 1 || !errors.Is(ui.reported[0], flowErr) {
		t.Fatalf("reported errors = %v", ui.reported)
	}
	if ui.mainCalls != 2 || interrupts != 1 {
		t.Fatalf("main calls = %d, interrupts = %d", ui.mainCalls, interrupts)
	}
}

func TestRunInteractiveInterruptsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	interrupts := 0
	err := RunInteractive(ctx, nil, &fakeInteractiveUI{}, func() error { interrupts++; return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, interrupts = %d", err, interrupts)
	}
}

type uiResult struct {
	progress Progress
	err      error
}

type fakeInteractiveUI struct {
	main      []uiResult
	mainCalls int
	reported  []error
}

func (ui *fakeInteractiveUI) Main() (Progress, error) {
	result := ui.main[ui.mainCalls]
	ui.mainCalls++
	return result.progress, result.err
}
func (*fakeInteractiveUI) Draft(context.Context, Progress) (Progress, error) {
	panic("unexpected draft menu")
}
func (*fakeInteractiveUI) ChangeAnswer(context.Context, string) (Progress, error) {
	panic("unexpected change answer")
}
func (ui *fakeInteractiveUI) ReportError(err error) { ui.reported = append(ui.reported, err) }
