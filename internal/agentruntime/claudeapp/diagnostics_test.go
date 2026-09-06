package claudeapp

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStderrCaptureRetainsABoundedTail(t *testing.T) {
	capture := &stderrCapture{}
	capture.Add(strings.Repeat("x", maxCapturedStderrRunes+10))
	capture.Add("final diagnostic")

	got := capture.String()
	if !strings.Contains(got, "[earlier stderr omitted]") || !strings.Contains(got, "final diagnostic") {
		t.Fatalf("captured stderr = %q", got)
	}
	if len([]rune(got)) > maxCapturedStderrRunes+len([]rune("[earlier stderr omitted] ")) {
		t.Fatalf("captured stderr is not bounded: %d runes", len([]rune(got)))
	}
}

func TestConnectDiagnosticEscapesTerminalControlCharacters(t *testing.T) {
	err := errors.New("initialize failed: control request timeout")
	got := describeConnectFailure(err, time.Minute, "\x1b[31mfailed")

	if strings.ContainsRune(got, '\x1b') || !strings.Contains(got, `\x1b`) {
		t.Fatalf("connect diagnostic = %q", got)
	}
}
