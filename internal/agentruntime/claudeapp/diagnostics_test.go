package claudeapp

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStderrCaptureRetainsABoundedTail(t *testing.T) {
	capture := &stderrCapture{}
	capture.Add(strings.Repeat("x", maxDiagnosticRunes+10))
	capture.Add("final diagnostic")

	got := capture.String()
	if !strings.Contains(got, "[earlier stderr omitted]") || !strings.Contains(got, "final diagnostic") {
		t.Fatalf("captured stderr = %q", got)
	}
	if len([]rune(got)) > maxDiagnosticRunes+len([]rune("[earlier stderr omitted] ")) {
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

func TestTerminalResultDiagnosticIsBoundedAndEscaped(t *testing.T) {
	text := "\x1b[31m" + strings.Repeat("x", maxDiagnosticRunes+100) + " useful tail"
	quoted := quoteDiagnosticText(text)

	if strings.ContainsRune(quoted, '\x1b') || !strings.Contains(quoted, `\x1b`) || !strings.Contains(quoted, "useful tail") {
		t.Fatalf("quoted terminal diagnostic = %q", quoted)
	}
	if len([]rune(quoted)) > maxDiagnosticRunes+16 {
		t.Fatalf("quoted terminal diagnostic is not bounded: %d runes", len([]rune(quoted)))
	}
}
