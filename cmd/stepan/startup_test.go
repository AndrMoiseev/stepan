package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
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
		Run:              &implstate.Run{Status: implstate.RunActive},
		Summary:          impl_loop.StartupSummary{Change: "change", Lifecycle: impl_loop.LifecyclePaused},
		RecoveryRequired: true,
	}
	ui := &startupUIFake{inputs: []string{"/status", "/resume"}}
	recoveries := 0
	continued := 0
	err := runDiscoveredImplementationInteractive(context.Background(), startup, ui, func(context.Context) (*impl_loop.InteractiveRun, error) {
		recoveries++
		run := &implstate.Run{Status: implstate.RunPaused}
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
			previous := productionResumeSpecification(t, false,
				implementationSpecificationDocument{Path: "openspec/changes/change/proposal.md", Content: "# Change\r\n\r\nRequirement A\r\n"},
				implementationSpecificationDocument{Path: "openspec/changes/change/design.md", Content: "# Design\n"},
				implementationSpecificationDocument{Path: "openspec/changes/change/specs/feature/spec.md", Content: "## Requirement: A\n"},
			)
			compatible := impl_loop.SpecificationChange{}
			current := productionResumeSpecification(t, true,
				implementationSpecificationDocument{Path: "openspec/changes/change/proposal.md", Content: "# Change\n\nRequirement A\n"},
				implementationSpecificationDocument{Path: "openspec/changes/change/design.md", Content: "# Design\n"},
				implementationSpecificationDocument{Path: "openspec/changes/change/specs/feature/spec.md", Content: "## Requirement: A\n"},
			)
			compatible, err := composition.ClassifySpecificationChange(previous, current)
			if err != nil || compatible.RequiresNewScope {
				t.Fatalf("JSON/line-ending-only specification change = %#v, %v", compatible, err)
			}
			scope := productionResumeSpecification(t, false,
				implementationSpecificationDocument{Path: "openspec/changes/change/proposal.md", Content: "# Change\n\nRequirement B\n"},
				implementationSpecificationDocument{Path: "openspec/changes/change/design.md", Content: "# Design\n"},
				implementationSpecificationDocument{Path: "openspec/changes/change/specs/feature/spec.md", Content: "## Requirement: A\n"},
			)
			change, err := composition.ClassifySpecificationChange(current, scope)
			if err != nil || !change.RequiresNewScope {
				t.Fatalf("semantic specification change = %#v, %v", change, err)
			}
			indeterminate := productionResumeSpecification(t, false,
				implementationSpecificationDocument{Path: "openspec/changes/change/proposal.md", Content: "# Change\n\nRequirement A\n"},
				implementationSpecificationDocument{Path: "openspec/changes/change/design.md", Content: "# Design\n\n## New structure\n"},
				implementationSpecificationDocument{Path: "openspec/changes/change/specs/feature/spec.md", Content: "## Requirement: A\n"},
			)
			if change, err := composition.ClassifySpecificationChange(current, indeterminate); err == nil || change.RequiresNewScope {
				t.Fatalf("indeterminate design change = %#v, %v", change, err)
			}
		})
	}
}

func TestProductionSpecificationClassifierGivesNewScopeDeterministicPrecedence(t *testing.T) {
	proposal := implementationSpecificationDocument{Path: "openspec/changes/change/proposal.md", Content: "# Proposal\n\nOriginal scope.\n"}
	design := implementationSpecificationDocument{Path: "openspec/changes/change/design.md", Content: "# Design\n\nOriginal design.\n"}
	specification := implementationSpecificationDocument{Path: "openspec/changes/change/specs/feature/spec.md", Content: "## Requirement\n\nOriginal behavior.\n"}
	before := productionResumeSpecification(t, false, proposal, design, specification)
	proposal.Content = "# Proposal\n\nExpanded scope.\n"
	design.Content = "# Design\n\nChanged architecture.\n"

	orders := [][]implementationSpecificationDocument{
		{proposal, design, specification},
		{design, specification, proposal},
		{specification, proposal, design},
	}
	for index := 0; index < 24; index++ {
		documents := orders[index%len(orders)]
		change, err := classifyImplementationSpecification(before, productionResumeSpecification(t, index%2 == 0, documents...))
		if err != nil || !change.RequiresNewScope {
			t.Fatalf("order %d: change=%#v err=%v", index, change, err)
		}
	}
}

func productionResumeSpecification(t *testing.T, indent bool, documents ...implementationSpecificationDocument) []byte {
	t.Helper()
	for index := range documents {
		digest := sha256.Sum256([]byte(documents[index].Content))
		documents[index].Version = hex.EncodeToString(digest[:])
	}
	var data []byte
	var err error
	if indent {
		data, err = json.MarshalIndent(documents, "", "  ")
	} else {
		data, err = json.Marshal(documents)
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}
