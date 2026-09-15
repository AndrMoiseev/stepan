package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestDiscoverImplementationStartupLeavesMissingStoreUntouched(t *testing.T) {
	home := t.TempDir()
	startup, store, err := openImplementationStartup(context.Background(), t.TempDir(), func() (string, error) { return home, nil })
	if err != nil || startup != nil || store != nil {
		t.Fatalf("missing startup run = %#v, %#v, %v", startup, store, err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".stepan")); !os.IsNotExist(err) {
		t.Fatalf("startup discovery created run store: %v", err)
	}
}

func TestDiscoveredImplementationStartupOnlyRecoversAfterResume(t *testing.T) {
	startup := &impl_loop.StartupRun{
		Run:              &implementationstate.Run{Status: implementationstate.RunActive},
		Summary:          impl_loop.StartupSummary{Change: "change", Lifecycle: impl_loop.LifecyclePaused},
		RecoveryRequired: true,
	}
	ui := &startupUIFake{inputs: []string{"/status", "/resume"}}
	recoveries := 0
	err := runDiscoveredImplementationInteractive(context.Background(), startup, ui, func(context.Context) (*impl_loop.InteractiveRun, error) {
		recoveries++
		return &impl_loop.InteractiveRun{Run: &implementationstate.Run{Status: implementationstate.RunPaused}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if recoveries != 1 {
		t.Fatalf("startup recovery calls = %d, want 1 after /resume", recoveries)
	}
	if len(ui.reports) == 0 {
		t.Fatal("startup did not render discovered run before prompting")
	}
}

type startupUIFake struct {
	inputs  []string
	reports []string
}

func (u *startupUIFake) Prompt(context.Context, impl_loop.CommandMenu) (string, error) {
	if len(u.inputs) == 0 {
		return "", impl_loop.ErrInteractiveInputCanceled
	}
	input := u.inputs[0]
	u.inputs = u.inputs[1:]
	return input, nil
}
func (u *startupUIFake) Report(message string) { u.reports = append(u.reports, message) }
func (*startupUIFake) ReportError(error)       {}
