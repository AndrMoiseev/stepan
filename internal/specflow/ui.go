package specflow

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

type UI struct {
	controller *Controller
	input      *bufio.Reader
	output     io.Writer
}

func NewUI(c *Controller) *UI {
	return &UI{controller: c, input: bufio.NewReader(os.Stdin), output: os.Stdout}
}
func (u *UI) ReportError(err error) { u.say("Ошибка: " + err.Error()) }
func (u *UI) Main() (Progress, error) {
	for {
		value, err := u.readLine("Вы > ")
		if err != nil {
			return Progress{}, err
		}
		command, err := ParseMainCommand(value)
		if err != nil {
			u.say(err.Error())
			continue
		}
		if command.NeedBrief {
			u.say("Опишите намерение.")
			brief, err := u.text()
			if err != nil {
				return Progress{}, err
			}
			u.thinking()
			return u.controller.StartFeature(brief)
		}
		u.thinking()
		return u.controller.StartFeature(command.Brief)
	}
}
func (u *UI) Dialogue(_ context.Context, p Progress) (Progress, error) {
	if p.Message != "" {
		u.say(p.Message)
	}
	if p.State == StateIntentPublished {
		u.say("Intent опубликован: " + p.Path + ". Продолжайте диалог или введите /approve.")
	} else if p.State == StateAwaitingBrief {
		u.say("Опишите намерение.")
	} else if p.State == StateAwaitingRework {
		u.say("Опишите доработку draft.")
	}
	value, err := u.text()
	if err != nil {
		return p, err
	}
	u.thinking()
	return u.controller.Submit(value)
}
func (u *UI) Review(_ context.Context, p Progress) (Progress, error) {
	u.say("Новый draft intent:\n" + p.Diff)
	value, err := u.readLine("Вы > [apply | reject | rework] ")
	if err != nil {
		return p, err
	}
	action, err := ParseReviewAction(value)
	if err != nil {
		u.say(err.Error())
		return p, nil
	}
	return u.controller.Review(action)
}

// MainPrompt renders the commands that exist outside a feature flow and reads
// one main-menu action. It does not start or resume a feature itself.
func (u *UI) MainPrompt() (MainCommand, error) {
	u.renderCommandTable([]CommandHint{
		{Command: "/feature", Description: "Start a new feature flow."},
		{Command: "/resume", Description: "Resume an available unfinished feature flow."},
	})
	_, _ = fmt.Fprintln(u.output, "Обычный текст: недоступен.")
	value, err := u.readLine("Вы > ")
	if err != nil {
		return MainCommand{}, err
	}
	return ParseMainCommand(value)
}

// ReadFeatureBrief completes the two-step /feature form. Keeping it separate
// lets an inline `/feature <brief>` avoid an unnecessary prompt.
func (u *UI) ReadFeatureBrief() (string, error) {
	u.say("Опишите намерение.")
	_, _ = fmt.Fprintln(u.output, "Обычный текст: разрешён.")
	return u.text()
}

// ResumePrompt renders exactly the selectable rows and returns the selected
// feature identity. An empty list is informational and does not fabricate a
// selectable row.
func (u *UI) ResumePrompt(flows []ResumableFlow) (string, error) {
	rows := ResumeRows(flows)
	if len(rows) == 0 {
		u.say("Нет доступных незавершённых flows.")
		return "", nil
	}
	u.renderResumeTable(rows)
	value, err := u.readLine("Вы > Номер flow: ")
	if err != nil {
		return "", err
	}
	flow, err := SelectResumeRow(rows, value)
	if err != nil {
		return "", err
	}
	return flow.FeatureID, nil
}

// FlowPrompt renders only presentation data supplied through Progress. It
// validates that slash commands and revision actions were advertised, but
// deliberately does not interpret stage or review status.
func (u *UI) FlowPrompt(_ context.Context, progress Progress) (string, error) {
	u.renderProgress(progress)
	value, err := u.readLine("Вы > ")
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "/") {
		if !commandAllowed(progress, trimmed) {
			return "", fmt.Errorf("command %q is not available in this context", trimmed)
		}
		return trimmed, nil
	}
	if revisionAllowed(progress, trimmed) {
		return trimmed, nil
	}
	if !progress.TextAllowed {
		return "", fmt.Errorf("ordinary text is not available in this context")
	}
	return value, nil
}

func (u *UI) renderProgress(progress Progress) {
	if progress.Message != "" {
		u.say(progress.Message)
	}
	if progress.FeatureID != "" {
		_, _ = fmt.Fprintf(u.output, "\nFeature: %s\nCurrent stage: %s\nStage status: %s\nReview status: %s\n", progress.FeatureID, progress.CurrentStage, progress.StageStatus, progress.ReviewStatus)
	}
	if progress.Diff != "" {
		_, _ = fmt.Fprintf(u.output, "\nDiff:\n%s\n", progress.Diff)
	}
	for _, event := range progress.Review.Progress {
		if event.Message != "" {
			_, _ = fmt.Fprintf(u.output, "\nReview: %s\n", event.Message)
		}
		if event.Diff != "" {
			_, _ = fmt.Fprintf(u.output, "\nDiff:\n%s\n", event.Diff)
		}
	}
	for _, diagnostic := range progress.Diagnostics {
		location := ""
		if diagnostic.Line > 0 {
			location = fmt.Sprintf(" line %d", diagnostic.Line)
		}
		_, _ = fmt.Fprintf(u.output, "\nDiagnostic [%s]%s: %s\n", diagnostic.Code, location, diagnostic.Message)
	}
	for _, diagnostic := range progress.RecoveryDiagnostics {
		_, _ = fmt.Fprintf(u.output, "\nRecovery diagnostic [%s]: %s\n", diagnostic.Path, diagnostic.Message)
	}
	for _, blocker := range progress.Blocking {
		_, _ = fmt.Fprintf(u.output, "\nBlocking [%s] %s: %s\n", blocker.Code, blocker.Path, blocker.Message)
	}
	if len(progress.Documents) > 0 {
		writer := tabwriter.NewWriter(u.output, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(writer, "\nStage\t| Document")
		_, _ = fmt.Fprintln(writer, "---\t|---")
		for _, document := range progress.Documents {
			_, _ = fmt.Fprintf(writer, "%s\t| %s\n", document.Stage, document.Path)
		}
		_ = writer.Flush()
	}
	if len(progress.Revision) > 0 {
		u.renderRevisionTable(progress.Revision)
	}
	u.renderCommandTable(progress.CommandHints)
	if progress.TextAllowed {
		_, _ = fmt.Fprintln(u.output, "Обычный текст: разрешён.")
	} else {
		_, _ = fmt.Fprintln(u.output, "Обычный текст: недоступен.")
	}
}

func (u *UI) renderCommandTable(hints []CommandHint) {
	writer := tabwriter.NewWriter(u.output, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "\nCommand\t| Description")
	_, _ = fmt.Fprintln(writer, "---\t|---")
	for _, hint := range hints {
		_, _ = fmt.Fprintf(writer, "%s\t| %s\n", hint.Command, hint.Description)
	}
	_ = writer.Flush()
}

func (u *UI) renderRevisionTable(actions []RevisionAction) {
	descriptions := map[RevisionAction]string{
		RevisionApply:  "Publish this revision.",
		RevisionReject: "Discard this revision.",
		RevisionRework: "Ask the author to rework this revision.",
	}
	writer := tabwriter.NewWriter(u.output, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "\nAction\t| Description")
	_, _ = fmt.Fprintln(writer, "---\t|---")
	for _, action := range actions {
		_, _ = fmt.Fprintf(writer, "%s\t| %s\n", action, descriptions[action])
	}
	_ = writer.Flush()
}

func (u *UI) renderResumeTable(rows []ResumeRow) {
	writer := tabwriter.NewWriter(u.output, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "\n№\t| Feature\t| Current stage\t| Stage status\t| Review status\t| Updated")
	_, _ = fmt.Fprintln(writer, "---\t|---\t|---\t|---\t|---\t|---")
	for _, row := range rows {
		updated := "—"
		if !row.Flow.Updated.IsZero() {
			updated = row.Flow.Updated.Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(writer, "%d\t| %s\t| %s\t| %s\t| %s\t| %s\n", row.Number, row.Flow.FeatureID, row.Flow.CurrentStage, row.Flow.StageStatus, row.Flow.ReviewStatus, updated)
	}
	_ = writer.Flush()
}
func (u *UI) text() (string, error) { return u.readLine("Вы > ") }
func (u *UI) readLine(prompt string) (string, error) {
	_, _ = fmt.Fprint(u.output, prompt)
	value, err := u.input.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	if err == io.EOF && value == "" {
		return "", ErrCanceled
	}
	return value, nil
}
func (u *UI) say(message string) { _, _ = fmt.Fprintf(u.output, "\nStepan > %s\n\n", message) }
func (u *UI) thinking()          { _, _ = fmt.Fprint(u.output, "\nStepan думает…\n\n") }
