package specflow

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLineChatWritesOnePromptPerMessage(t *testing.T) {
	var output bytes.Buffer
	ui := &UI{input: bufio.NewReader(strings.NewReader("brief\n")), output: &output}
	value, err := ui.text()
	if err != nil || value != "brief" {
		t.Fatalf("input = %q, %v", value, err)
	}
	ui.say("question")
	ui.thinking()

	for _, want := range []string{"Вы > ", "Stepan > question", "Stepan думает…"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("chat output = %q, missing %q", output.String(), want)
		}
	}
	if strings.Count(output.String(), "question") != 1 {
		t.Fatalf("chat duplicated assistant message: %q", output.String())
	}
}

func TestFlowPromptRendersOnlyProgressCommandsInControllerOrder(t *testing.T) {
	known := []string{"/review", "/apply", "/approve", "/revise-spec", "/status", "/exit"}
	tests := []struct {
		name        string
		progress    Progress
		wantCommand []string
		wantText    string
	}{
		{
			name: "drafting",
			progress: Progress{FeatureID: "feature", CurrentStage: StageIntent, StageStatus: StageDrafting, ReviewStatus: ReviewNotStarted,
				CommandHints: []CommandHint{{Command: "/status", Description: "Show status."}, {Command: "/exit", Description: "Close."}}, TextAllowed: true},
			wantCommand: []string{"/status", "/exit"}, wantText: "Обычный текст: разрешён.",
		},
		{
			name: "pending revision",
			progress: Progress{FeatureID: "feature", CurrentStage: StageSpec, StageStatus: StagePublished, ReviewStatus: ReviewNotStarted,
				CommandHints: []CommandHint{{Command: "/status", Description: "Show status."}, {Command: "/exit", Description: "Close."}},
				Revision:     []RevisionAction{RevisionApply, RevisionReject, RevisionRework}},
			wantCommand: []string{"/status", "/exit"}, wantText: "Обычный текст: недоступен.",
		},
		{
			name: "published spec",
			progress: Progress{FeatureID: "feature", CurrentStage: StageSpec, StageStatus: StagePublished, ReviewStatus: ReviewNotStarted,
				CommandHints: []CommandHint{{Command: "/review", Description: "Review."}, {Command: "/approve", Description: "Approve."}, {Command: "/status", Description: "Show status."}, {Command: "/exit", Description: "Close."}}, TextAllowed: true},
			wantCommand: []string{"/review", "/approve", "/status", "/exit"}, wantText: "Обычный текст: разрешён.",
		},
		{
			name: "awaiting review decisions",
			progress: Progress{FeatureID: "feature", CurrentStage: StageSpec, StageStatus: StagePublished, ReviewStatus: ReviewAwaitingDecisions,
				CommandHints: []CommandHint{{Command: "/apply", Description: "Apply findings."}, {Command: "/status", Description: "Show status."}, {Command: "/exit", Description: "Close."}}, TextAllowed: true},
			wantCommand: []string{"/apply", "/status", "/exit"}, wantText: "Обычный текст: разрешён.",
		},
		{
			name: "escalated review",
			progress: Progress{FeatureID: "feature", CurrentStage: StagePlan, StageStatus: StagePublished, ReviewStatus: ReviewEscalated,
				CommandHints: []CommandHint{{Command: "/review", Description: "Review."}, {Command: "/revise-spec", Description: "Revise spec."}, {Command: "/status", Description: "Show status."}, {Command: "/exit", Description: "Close."}}, TextAllowed: true},
			wantCommand: []string{"/review", "/revise-spec", "/status", "/exit"}, wantText: "Обычный текст: разрешён.",
		},
		{
			name: "committed plan",
			progress: Progress{FeatureID: "feature", CurrentStage: StagePlan, StageStatus: StageCommitted, ReviewStatus: ReviewCompleted,
				CommandHints: []CommandHint{{Command: "/status", Description: "Show status."}, {Command: "/exit", Description: "Close."}}},
			wantCommand: []string{"/status", "/exit"}, wantText: "Обычный текст: недоступен.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			ui := &UI{input: bufio.NewReader(strings.NewReader("/exit\n")), output: &output}
			_, _ = ui.FlowPrompt(context.Background(), tt.progress)
			text := output.String()
			previous := -1
			for _, command := range tt.wantCommand {
				position := strings.Index(text, command)
				if position < 0 || position <= previous {
					t.Fatalf("command order/output = %q, command %q after byte %d", text, command, previous)
				}
				previous = position
			}
			for _, command := range known {
				want := false
				for _, expected := range tt.wantCommand {
					want = want || command == expected
				}
				if strings.Contains(text, command) != want {
					t.Fatalf("command visibility %s = %v, want %v; output=%q", command, strings.Contains(text, command), want, text)
				}
			}
			if !strings.Contains(text, tt.wantText) {
				t.Fatalf("text availability missing from %q", text)
			}
			if len(tt.progress.Revision) > 0 {
				for _, action := range []string{"apply", "reject", "rework"} {
					if !strings.Contains(text, action) {
						t.Fatalf("revision action %q missing from %q", action, text)
					}
				}
			}
		})
	}
}

func TestProgressDiagnosticsAppearBeforeCommandTable(t *testing.T) {
	var output bytes.Buffer
	ui := &UI{input: bufio.NewReader(strings.NewReader("/status\n")), output: &output}
	progress := Progress{
		Diagnostics:         []DocumentDiagnostic{{Code: DiagnosticInvalidID, Message: "invalid heading"}},
		RecoveryDiagnostics: []RecoveryDiagnostic{{Path: "state.json", Message: "needs attention"}},
		CommandHints:        []CommandHint{{Command: "/status", Description: "Show status."}},
	}
	if _, err := ui.FlowPrompt(context.Background(), progress); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, diagnostic := range []string{"invalid heading", "needs attention"} {
		if strings.Index(text, diagnostic) < 0 || strings.Index(text, diagnostic) > strings.Index(text, "Command") {
			t.Fatalf("diagnostic is not before command table: %q", text)
		}
	}
}

func TestFlowPromptRejectsInputNotAdvertisedByProgress(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		progress Progress
	}{
		{name: "hidden command", input: "/approve\n", progress: Progress{CommandHints: []CommandHint{{Command: "/status", Description: "Show status."}}, TextAllowed: true}},
		{name: "ordinary text unavailable", input: "hello\n", progress: Progress{CommandHints: []CommandHint{{Command: "/status", Description: "Show status."}}}},
		{name: "revision action not offered", input: "apply\n", progress: Progress{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ui := &UI{input: bufio.NewReader(strings.NewReader(tt.input)), output: &bytes.Buffer{}}
			if _, err := ui.FlowPrompt(context.Background(), tt.progress); err == nil {
				t.Fatal("unadvertised input was accepted")
			}
		})
	}

	ui := &UI{input: bufio.NewReader(strings.NewReader("apply\n")), output: &bytes.Buffer{}}
	if input, err := ui.FlowPrompt(context.Background(), Progress{Revision: []RevisionAction{RevisionApply}}); err != nil || input != "apply" {
		t.Fatalf("advertised revision action = %q, %v", input, err)
	}
}

func TestResumeTableFiltersAndSelectsByDisplayedNumber(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 11, 12, 0, time.UTC)
	flows := []ResumableFlow{
		{FeatureID: "2026-08-30-active", FlowStatus: FlowActive, CurrentStage: StageSpec, StageStatus: StagePublished, ReviewStatus: ReviewCompleted, Updated: now},
		{FeatureID: "2026-08-30-superseded", FlowStatus: FlowSuperseded, CurrentStage: StageIntent, StageStatus: StagePublished, ReviewStatus: ReviewNotStarted, Updated: now},
		{FeatureID: "2026-08-30-done", FlowStatus: FlowActive, CurrentStage: StagePlan, StageStatus: StageCommitted, ReviewStatus: ReviewCompleted, Updated: now},
	}
	var output bytes.Buffer
	ui := &UI{input: bufio.NewReader(strings.NewReader("1\n")), output: &output}
	selected, err := ui.ResumePrompt(flows)
	if err != nil || selected != "2026-08-30-active" {
		t.Fatalf("selection = %q, %v", selected, err)
	}
	text := output.String()
	if strings.Contains(text, "superseded") || strings.Contains(text, "done") {
		t.Fatalf("resume table exposed filtered flows: %q", text)
	}
	var selectedLine string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, selected) {
			selectedLine = line
		}
	}
	if selectedLine == "" || strings.Count(selectedLine, "|") != 5 {
		t.Fatalf("resume row does not have six columns: %q", selectedLine)
	}
	if _, err := SelectResumeRow(ResumeRows(flows), "2"); err == nil {
		t.Fatal("invalid displayed number was accepted")
	}
}

func TestEmptyResumeTableIsInformational(t *testing.T) {
	var output bytes.Buffer
	ui := &UI{input: bufio.NewReader(strings.NewReader("ignored\n")), output: &output}
	selected, err := ui.ResumePrompt(nil)
	if err != nil || selected != "" {
		t.Fatalf("empty selection = %q, %v", selected, err)
	}
	if !strings.Contains(output.String(), "Нет доступных") || strings.Contains(output.String(), "№") {
		t.Fatalf("empty resume output = %q", output.String())
	}
}

func TestInvalidResumeNumberDoesNotActivateController(t *testing.T) {
	controller := &planningControllerStub{flows: []ResumableFlow{{FeatureID: "2026-08-30-only", FlowStatus: FlowActive, CurrentStage: StageIntent, StageStatus: StageDrafting}}}
	var output bytes.Buffer
	ui := &UI{input: bufio.NewReader(strings.NewReader("/resume\n2\n")), output: &output}
	err := RunPlanningInteractive(context.Background(), controller, ui)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("run error = %v", err)
	}
	if len(controller.resumed) != 0 {
		t.Fatalf("invalid number activated %#v", controller.resumed)
	}
}

func TestInteractiveExitPathsUseOneSessionCloseLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		flowInput string
		flowErr   error
	}{
		{name: "slash exit", flowInput: "/exit"},
		{name: "eof", flowErr: ErrCanceled},
		{name: "ctrl-c", flowErr: ErrInterrupted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &planningControllerStub{}
			ui := &planningUIStub{flowInput: tt.flowInput, flowErr: tt.flowErr}
			err := RunPlanningInteractive(context.Background(), controller, ui)
			if !errors.Is(err, ErrCanceled) {
				t.Fatalf("run error = %v", err)
			}
			if controller.closes != 1 || len(controller.submitted) != 0 {
				t.Fatalf("close lifecycle: closes=%d submitted=%#v", controller.closes, controller.submitted)
			}
			if ui.mainCalls != 2 {
				t.Fatalf("main prompt calls = %d, want return to main after close", ui.mainCalls)
			}
		})
	}
}

func TestInteractiveForwardsFlowInputWithoutLocalStateChanges(t *testing.T) {
	for _, input := range []string{"  preserve ordinary text  ", "/status"} {
		t.Run(input, func(t *testing.T) {
			controller := &planningControllerStub{}
			ui := &planningUIStub{flowInput: input, exitAfterInput: true}
			err := RunPlanningInteractive(context.Background(), controller, ui)
			if !errors.Is(err, ErrCanceled) {
				t.Fatalf("run error = %v", err)
			}
			if len(controller.submitted) != 1 || controller.submitted[0] != input {
				t.Fatalf("submitted = %#v, want unchanged %q", controller.submitted, input)
			}
			if controller.closes != 1 {
				t.Fatalf("closes = %d", controller.closes)
			}
		})
	}
}

type planningControllerStub struct {
	flows     []ResumableFlow
	resumed   []string
	submitted []string
	closes    int
}

func (c *planningControllerStub) StartFeature(string) (Progress, error) {
	return activeInteractiveProgress(), nil
}
func (c *planningControllerStub) DiscoverResumable() ([]ResumableFlow, error) {
	return append([]ResumableFlow(nil), c.flows...), nil
}
func (c *planningControllerStub) Resume(featureID string) (Progress, error) {
	c.resumed = append(c.resumed, featureID)
	return activeInteractiveProgress(), nil
}
func (c *planningControllerStub) Submit(input string) (Progress, error) {
	c.submitted = append(c.submitted, input)
	return activeInteractiveProgress(), nil
}
func (c *planningControllerStub) Close() (Progress, error) {
	c.closes++
	return Progress{Event: ControllerSessionClosed}, nil
}

func activeInteractiveProgress() Progress {
	return Progress{Event: ControllerAuthorStarted, FeatureID: "2026-08-30-feature", CommandHints: []CommandHint{{Command: "/exit", Description: "Close."}}}
}

type planningUIStub struct {
	mainCalls      int
	flowCalls      int
	flowInput      string
	flowErr        error
	exitAfterInput bool
}

func (u *planningUIStub) MainPrompt() (MainCommand, error) {
	u.mainCalls++
	if u.mainCalls > 1 {
		return MainCommand{}, ErrCanceled
	}
	return MainCommand{Action: MainActionFeature, Brief: "brief"}, nil
}
func (u *planningUIStub) ReadFeatureBrief() (string, error) { return "brief", nil }
func (u *planningUIStub) ResumePrompt([]ResumableFlow) (string, error) {
	return "", fmt.Errorf("unexpected resume prompt")
}
func (u *planningUIStub) FlowPrompt(context.Context, Progress) (string, error) {
	u.flowCalls++
	if u.exitAfterInput && u.flowCalls > 1 {
		return "/exit", nil
	}
	return u.flowInput, u.flowErr
}
func (u *planningUIStub) ReportError(error) {}
