package claudeapp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	claudecode "github.com/severity1/claude-agent-sdk-go"
)

const maxDiagnosticRunes = 4096

// stderrCapture retains only a small tail of pre-prompt CLI stderr. The SDK
// otherwise discards this output, including actionable flag and authentication
// failures emitted while its control protocol is being initialized.
type stderrCapture struct {
	mu        sync.Mutex
	lines     []string
	runes     int
	truncated bool
}

func (capture *stderrCapture) Add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	characters := []rune(line)
	lineTruncated := false
	if len(characters) > maxDiagnosticRunes {
		characters = characters[len(characters)-maxDiagnosticRunes:]
		line = string(characters)
		lineTruncated = true
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.truncated = capture.truncated || lineTruncated
	capture.lines = append(capture.lines, line)
	capture.runes += len(characters)
	for capture.runes > maxDiagnosticRunes && len(capture.lines) > 1 {
		capture.runes -= len([]rune(capture.lines[0]))
		capture.lines = capture.lines[1:]
		capture.truncated = true
	}
}

func (capture *stderrCapture) String() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.lines) == 0 {
		return ""
	}
	text := strings.Join(capture.lines, "\n")
	characters := []rune(text)
	if len(characters) > maxDiagnosticRunes {
		text = string(characters[len(characters)-maxDiagnosticRunes:])
		capture.truncated = true
	}
	if capture.truncated {
		return "[earlier stderr omitted] " + text
	}
	return text
}

func describeConnectFailure(err error, elapsed time.Duration, stderr string) string {
	details := []string{fmt.Sprintf("connection attempt lasted %s", elapsed.Round(time.Millisecond))}
	errorText := err.Error()
	if strings.Contains(errorText, "initialize") && strings.Contains(errorText, "control request timeout") {
		details = append(details, "CLI did not answer the Claude Agent SDK initialize request; verify that this executable supports bidirectional --input-format stream-json control messages and does not require a TTY")
	}
	if stderr == "" {
		details = append(details, "CLI wrote no stderr before failure")
	} else {
		details = append(details, fmt.Sprintf("CLI stderr before failure: %q", stderr))
	}
	return strings.Join(details, "; ")
}

func describeTerminalResultError(result *claudecode.ResultMessage) error {
	details := make([]string, 0, 3)
	if result.Subtype != "" {
		details = append(details, "subtype="+quoteDiagnosticText(result.Subtype))
	}
	if len(result.Errors) != 0 {
		details = append(details, "errors="+quoteDiagnosticText(strings.Join(result.Errors, "; ")))
	}
	if result.Result != nil && strings.TrimSpace(*result.Result) != "" {
		details = append(details, "result="+quoteDiagnosticText(*result.Result))
	}
	if len(details) == 0 {
		return errors.New("Claude terminal result reports an error without details")
	}
	return fmt.Errorf("Claude terminal result reports an error: %s", strings.Join(details, "; "))
}

func quoteDiagnosticText(text string) string {
	text = strings.TrimSpace(text)
	characters := []rune(text)
	if len(characters) > maxDiagnosticRunes {
		const marker = " … [diagnostic truncated] … "
		available := maxDiagnosticRunes - len([]rune(marker))
		left := available / 2
		right := available - left
		text = string(characters[:left]) + marker + string(characters[len(characters)-right:])
	}
	return strconv.Quote(text)
}
