package impl_loop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestFormatImplementationProgressShowsActiveWorkResultsAndArtifactsWithoutTranscript(t *testing.T) {
	run := &implementationstate.Run{
		Identity: implementationstate.RunIdentity{ID: "run-progress"}, Status: implementationstate.RunActive,
		Tasks: []implementationstate.Task{{ID: "12.3", Title: "show progress"}},
		Assignments: []implementationstate.Assignment{{
			ID: "assignment-12", TaskIDs: []implementationstate.TaskID{"12.3"}, Status: implementationstate.AssignmentActive,
			Operations: []implementationstate.Operation{
				{ID: "checked", Kind: implementationstate.OperationCheck, Description: "run required checks", Counter: implementationstate.CycleCounterMandatoryChecks, Attempts: []implementationstate.OperationAttempt{{Number: 1}}},
				{ID: "implement", Kind: implementationstate.OperationAgent, Description: "implement assignment", Attempts: []implementationstate.OperationAttempt{{Number: 1}}},
			},
			Results: []implementationstate.OperationResult{{ID: "checked-result", OperationID: "checked", Status: implementationstate.ResultSucceeded, Evidence: []implementationstate.EvidenceRef{{ID: "stdout-log"}, {ID: "stderr-log"}}}},
		}},
	}
	got := FormatImplementationProgress(ProgressPresentationInput{
		Run: run, Runtime: implementationconfig.Platform{OS: "windows", Architecture: "amd64"},
		StartedAt: time.Unix(100, 0), Now: time.Unix(107, 0),
		ArtifactPath: func(reference implementationstate.EvidenceRef) (string, error) {
			return `C:\runs\files\` + string(reference.ID), nil
		},
	})
	for _, want := range []string{
		"runtime platform: windows/amd64", "duration: 7s", "assignment: assignment-12 — 12.3 (show progress)",
		"current: implementer — implement assignment", "counters: mandatory_checks=1", "latest result: check succeeded",
		`C:\runs\files\stdout-log`, `C:\runs\files\stderr-log`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress panel missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "agent private transcript") {
		t.Fatalf("progress panel exposed an agent transcript: %s", got)
	}
}

func TestFormatImplementationProgressRendersPersistedCheckSummaryAndLogLinks(t *testing.T) {
	summary, err := json.Marshal(checkProgressEvidence{Results: []struct {
		Name         string             `json:"name"`
		Status       CheckStatus        `json:"status"`
		Command      string             `json:"command,omitempty"`
		Presentation *CheckPresentation `json:"presentation,omitempty"`
	}{{Name: "unit", Status: CheckSucceeded, Command: `"go" "test" ./...`, Presentation: &CheckPresentation{Duration: time.Second, Stdout: CheckLog{Path: `C:\runs\unit.stdout`}, Stderr: CheckLog{Path: `C:\runs\unit.stderr`}}}}})
	if err != nil {
		t.Fatal(err)
	}
	run := &implementationstate.Run{Identity: implementationstate.RunIdentity{ID: "run-check"}, Status: implementationstate.RunActive, RunOperations: []implementationstate.Operation{{ID: "checks", Kind: implementationstate.OperationCheck, Description: "required checks"}}, RunResults: []implementationstate.OperationResult{{ID: "checks-result", OperationID: "checks", Status: implementationstate.ResultSucceeded, Evidence: []implementationstate.EvidenceRef{{ID: "check-summary"}}}}}
	got := FormatImplementationProgress(ProgressPresentationInput{Run: run, ReadArtifact: func(reference implementationstate.EvidenceRef) ([]byte, error) {
		if reference.ID != "check-summary" {
			t.Fatalf("unexpected artifact read: %#v", reference)
		}
		return summary, nil
	}})
	for _, want := range []string{`check unit: succeeded; command "go" "test" ./... in 1s; logs C:\runs\unit.stdout, C:\runs\unit.stderr`} {
		if !strings.Contains(got, want) {
			t.Fatalf("persisted check summary missing %q: %s", want, got)
		}
	}
}

func TestFormatImplementationProgressShowsDiagnosticPauseAndTerminalSummary(t *testing.T) {
	run := &implementationstate.Run{
		Identity: implementationstate.RunIdentity{ID: "run-paused"}, Status: implementationstate.RunPaused,
		ExecutionBlock: &implementationstate.ExecutionBlock{BlockedAction: "run required checks", Diagnostic: "go is unavailable", Attempts: []string{"checked PATH"}, RequiredUserAction: "install Go"},
	}
	paused := FormatImplementationProgress(ProgressPresentationInput{Run: run, Runtime: implementationconfig.Platform{OS: "windows", Architecture: "amd64"}})
	if !strings.Contains(paused, "diagnostic pause: run required checks — go is unavailable; required: install Go") {
		t.Fatalf("diagnostic pause was not rendered: %s", paused)
	}
	run.Status = implementationstate.RunSucceeded
	terminal := FormatImplementationProgress(ProgressPresentationInput{Run: run, Runtime: implementationconfig.Platform{OS: "windows", Architecture: "amd64"}})
	if !strings.Contains(terminal, "final summary: terminal run retained for audit") {
		t.Fatalf("terminal summary was not rendered: %s", terminal)
	}
}

func TestFormatCheckProgressResultSeparatesRuntimePlatformFromCrossBuildTarget(t *testing.T) {
	host := implementationconfig.Platform{OS: "windows", Architecture: "amd64"}
	cross := FormatCheckProgressResult(CheckSetResult{Name: "darwin-build", Status: CheckSucceeded, Command: checkexec.Command{Env: map[string]string{"GOOS": "darwin", "GOARCH": "arm64", "TOKEN": "secret"}}}, host)
	for _, want := range []string{"check darwin-build: succeeded", "cross-build target darwin/arm64", "artifact build only; target runtime not accepted"} {
		if !strings.Contains(cross, want) {
			t.Fatalf("cross-build presentation missing %q: %s", want, cross)
		}
	}
	if strings.Contains(cross, "secret") || strings.Contains(cross, "runtime platform: darwin") {
		t.Fatalf("cross-build presentation leaked env or falsely changed runtime platform: %s", cross)
	}
	if same := FormatCheckProgressResult(CheckSetResult{Name: "native-build", Status: CheckSucceeded, Command: checkexec.Command{Env: map[string]string{"GOOS": "windows", "GOARCH": "amd64"}}}, host); strings.Contains(same, "cross-build") {
		t.Fatalf("native target was reported as cross-build: %s", same)
	}
}
