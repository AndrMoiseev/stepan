package claudeapp

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const maxCapturedStderrRunes = 4096

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
	if len(characters) > maxCapturedStderrRunes {
		characters = characters[len(characters)-maxCapturedStderrRunes:]
		line = string(characters)
		lineTruncated = true
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.truncated = capture.truncated || lineTruncated
	capture.lines = append(capture.lines, line)
	capture.runes += len(characters)
	for capture.runes > maxCapturedStderrRunes && len(capture.lines) > 1 {
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
	if len(characters) > maxCapturedStderrRunes {
		text = string(characters[len(characters)-maxCapturedStderrRunes:])
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
