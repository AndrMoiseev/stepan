package specflow

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-colorable"
)

const (
	ansiReset       = "\x1b[0m"
	ansiBrightBlue  = "\x1b[94m"
	ansiBoldBlue    = "\x1b[1;34m"
	ansiBlue        = "\x1b[34m"
	ansiGreen       = "\x1b[32m"
	ansiYellow      = "\x1b[33m"
	ansiRed         = "\x1b[31m"
	ansiBoldMagenta = "\x1b[1;35m"
	ansiBoldCyan    = "\x1b[1;36m"
	ansiInputArea   = "\x1b[48;5;238;97m"
)

type UI struct {
	input            *bufio.Reader
	output           io.Writer
	activityInterval time.Duration
	color            bool
}

func NewUI() *UI {
	_, noColor := os.LookupEnv("NO_COLOR")
	color := !noColor && term.IsTerminal(os.Stdout.Fd())
	var output io.Writer = os.Stdout
	if color {
		output = colorable.NewColorableStdout()
	}
	return &UI{input: bufio.NewReader(os.Stdin), output: output, activityInterval: 120 * time.Millisecond, color: color}
}
func (u *UI) ReportError(err error) {
	if errors.Is(err, ErrRepositoryDirty) {
		u.sayStyled(ansiRed, "Нельзя начать feature flow: в Git есть незакоммиченные изменения. Закоммитьте или временно уберите их, затем повторите /feature.")
		return
	}
	u.sayStyled(ansiRed, "Ошибка: "+err.Error())
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
		if trimmed == string(RevisionRework) {
			u.say("Опишите границы доработки.")
			scope, scopeErr := u.text()
			if scopeErr != nil {
				return "", scopeErr
			}
			if strings.TrimSpace(scope) == "" {
				return "", fmt.Errorf("rework scope must not be empty")
			}
			return trimmed + " " + strings.TrimSpace(scope), nil
		}
		return trimmed, nil
	}
	if fingerprintAllowed(progress, trimmed) {
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
	if progress.Review.Message != "" {
		u.say(progress.Review.Message)
	}
	if progress.FeatureID != "" {
		u.renderStatusPanel(progress)
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
	if progress.Review.Path != "" {
		_, _ = fmt.Fprintf(u.output, "\nReview document: %s\n", progress.Review.Path)
	}
	for _, finding := range progress.Review.Findings {
		if finding.Status != FindingOpen {
			continue
		}
		heading := fmt.Sprintf("Review finding %s [%s, %s]", finding.ID, finding.Kind, finding.Severity)
		_, _ = fmt.Fprintf(u.output, "\n%s\nProblem: %s\n", u.style(findingStyle(finding.Severity), heading), finding.Problem)
		if finding.Location != "" {
			_, _ = fmt.Fprintf(u.output, "Location: %s\n", finding.Location)
		}
		if finding.Recommendation != "" {
			_, _ = fmt.Fprintf(u.output, "Recommendation: %s\n", finding.Recommendation)
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
	if len(progress.Review.FingerprintActions) > 0 {
		u.renderFingerprintTable(progress.Review.FingerprintActions)
	}
	u.renderCommandTable(progress.CommandHints)
	if progress.TextAllowed {
		_, _ = fmt.Fprintln(u.output, "Обычный текст: разрешён.")
	} else {
		_, _ = fmt.Fprintln(u.output, "Обычный текст: недоступен.")
	}
}

func (u *UI) renderCommandTable(hints []CommandHint) {
	width := 0
	for _, hint := range hints {
		if len(hint.Command) > width {
			width = len(hint.Command)
		}
	}
	_, _ = fmt.Fprintf(u.output, "\n%s\n", u.style(ansiBoldMagenta, "КОМАНДЫ"))
	for _, hint := range hints {
		command := fmt.Sprintf("%-*s", width, hint.Command)
		_, _ = fmt.Fprintf(u.output, "  %s  %s\n", u.style(ansiBoldCyan, command), hint.Description)
	}
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

func (u *UI) renderFingerprintTable(actions []ReviewFingerprintAction) {
	descriptions := map[ReviewFingerprintAction]string{
		ReviewFingerprintRerun:  "Run the review again for the current revisions.",
		ReviewFingerprintAccept: "Accept the report for the current revisions.",
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
	if u.color {
		coloredPrompt := strings.Replace(prompt, "Вы > ", " Вы › ", 1)
		_, _ = fmt.Fprint(u.output, ansiInputArea, coloredPrompt)
	} else {
		_, _ = fmt.Fprint(u.output, prompt)
	}
	value, err := u.input.ReadString('\n')
	if u.color {
		_, _ = fmt.Fprint(u.output, ansiReset)
	}
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

func (u *UI) sayStyled(style, message string) {
	_, _ = fmt.Fprintf(u.output, "\nStepan > %s\n\n", u.style(style, message))
}

func (u *UI) style(style, value string) string {
	if !u.color || value == "" {
		return value
	}
	return style + value + ansiReset
}

func (u *UI) renderStatusPanel(progress Progress) {
	border := u.style(ansiBrightBlue, "│")
	label := func(value string) string { return u.style(ansiBoldBlue, value) }
	_, _ = fmt.Fprintf(u.output, "\n%s %s  %s\n", border, label("FEATURE"), u.style("\x1b[1m", progress.FeatureID))
	_, _ = fmt.Fprintf(u.output, "%s %s    %s · %s\n", border, label("STAGE"), progress.CurrentStage, u.style(stageStatusStyle(progress.StageStatus), string(progress.StageStatus)))
	_, _ = fmt.Fprintf(u.output, "%s %s   %s\n", border, label("REVIEW"), u.style(reviewStatusStyle(progress.ReviewStatus), string(progress.ReviewStatus)))
}

func stageStatusStyle(status StageStatus) string {
	switch status {
	case StagePublished, StageCommitted:
		return ansiGreen
	case StageDrafting:
		return ansiBlue
	default:
		return ansiBrightBlue
	}
}

func reviewStatusStyle(status ReviewStatus) string {
	switch status {
	case ReviewAwaitingDecisions, ReviewAutomaticRework:
		return ansiYellow
	case ReviewAuthorDialogue:
		return ansiBoldMagenta
	case ReviewEscalated:
		return ansiRed
	case ReviewCompleted:
		return ansiGreen
	default:
		return ansiBlue
	}
}

func findingStyle(severity FindingSeverity) string {
	switch severity {
	case SeverityBlocker:
		return ansiRed
	case SeverityMajor:
		return ansiYellow
	default:
		return ansiBlue
	}
}

// BeginActivity renders a live terminal spinner until the returned function is
// called. The stop function is idempotent so every controller exit path can use
// it safely.
func (u *UI) BeginActivity(label string) func() {
	if u.output == nil {
		return func() {}
	}
	interval := u.activityInterval
	if interval <= 0 {
		interval = 120 * time.Millisecond
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	stopped := make(chan struct{})
	var once sync.Once
	_, _ = fmt.Fprintf(u.output, "\nStepan > %s %s", label, u.style(ansiYellow, frames[0]))
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		frame := 1
		for {
			select {
			case <-ticker.C:
				_, _ = fmt.Fprintf(u.output, "\rStepan > %s %s", label, u.style(ansiYellow, frames[frame%len(frames)]))
				frame++
			case <-done:
				return
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(done)
			<-stopped
			_, _ = fmt.Fprintf(u.output, "\rStepan > %s — %s\n\n", label, u.style(ansiGreen, "готово."))
		})
	}
}
