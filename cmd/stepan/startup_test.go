package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	continued := 0
	err := runDiscoveredImplementationInteractive(context.Background(), startup, ui, func(context.Context) (*impl_loop.InteractiveRun, error) {
		recoveries++
		run := &implementationstate.Run{Status: implementationstate.RunPaused}
		return &impl_loop.InteractiveRun{Run: run, Resume: func(context.Context, impl_loop.ResumeInput) (impl_loop.ResumeResult, error) {
			return impl_loop.ResumeResult{}, run.Resume()
		}}, nil
	}, func(context.Context, *impl_loop.InteractiveRun) error {
		continued++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if recoveries != 1 {
		t.Fatalf("startup recovery calls = %d, want 1 after /resume", recoveries)
	}
	if continued != 1 {
		t.Fatalf("durable continuation calls = %d, want 1 after a successful /resume", continued)
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

func TestImplementationConsoleUIPromptCancelsAndRedrawsWithOneInputPump(t *testing.T) {
	input, writer := io.Pipe()
	defer writer.Close()
	var output, errorOutput bytes.Buffer
	ui := newImplementationConsoleUIWithIO(input, &output, &errorOutput)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := ui.Prompt(ctx, impl_loop.CommandMenu{Commands: []impl_loop.CommandHint{{Command: impl_loop.CommandStatus}}})
		first <- err
	}()
	cancel()
	if err := <-first; !errors.Is(err, impl_loop.ErrInteractiveInputCanceled) {
		t.Fatalf("canceled prompt = %v", err)
	}
	second := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		value, err := ui.Prompt(context.Background(), impl_loop.CommandMenu{Commands: []impl_loop.CommandHint{{Command: impl_loop.CommandResume}}})
		second <- struct {
			value string
			err   error
		}{value, err}
	}()
	if _, err := io.WriteString(writer, "/resume\n"); err != nil {
		t.Fatal(err)
	}
	result := <-second
	if result.err != nil || result.value != "/resume\n" {
		t.Fatalf("redrawn prompt = %#v", result)
	}
	if got := output.String(); got == "" || errorOutput.Len() != 0 {
		t.Fatalf("console output = %q, errors = %q", got, errorOutput.String())
	}
}

func TestImplementationStartupCompositionProvidesEnvelopeAndScopeDecisionForEveryProvider(t *testing.T) {
	for _, kind := range []agentKind{agentCodex, agentClaude, agentNessy} {
		t.Run(string(kind), func(t *testing.T) {
			composition := implementationStartupCompositionForConfig(agentConfig{kind: kind, executable: string(kind)}, t.TempDir(), func() (string, error) { return "test-token", nil })
			if len(composition.EnvelopeSchema) == 0 || composition.Factories["codex"] == nil || composition.Factories["claude"] == nil || composition.Factories["nessy"] == nil {
				t.Fatalf("incomplete production composition: %#v", composition)
			}
			var envelope map[string]any
			if err := json.Unmarshal(composition.EnvelopeSchema, &envelope); err != nil || envelope["type"] != "object" {
				t.Fatalf("implementation envelope = %s, %v", composition.EnvelopeSchema, err)
			}
			compatible, err := composition.ClassifySpecificationChange([]byte("# Change\n\nRequirement A\n"), []byte("# Change  Requirement A"))
			if err != nil || compatible.RequiresNewScope {
				t.Fatalf("whitespace-only specification change = %#v, %v", compatible, err)
			}
			scope, err := composition.ClassifySpecificationChange([]byte("Requirement A"), []byte("Requirement B"))
			if err != nil || !scope.RequiresNewScope {
				t.Fatalf("semantic specification change = %#v, %v", scope, err)
			}
		})
	}
}
