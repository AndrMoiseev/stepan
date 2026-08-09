package codexexec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReplayClassifications(t *testing.T) {
	tests := []struct {
		fixture string
		valid   bool
		class   string
		session string
	}{
		{"success.jsonl", true, "", "session-1"},
		{"invalid.jsonl", false, "invalid_jsonl", "session-1"},
		{"missing-session.jsonl", true, "missing_session_id", ""},
		{"conflicting-sessions.jsonl", true, "conflicting_session_ids", ""},
		{"missing-terminal.jsonl", true, "missing_terminal_event", "session-1"},
		{"terminal-failure.jsonl", true, "terminal_failure", "session-1"},
		{"conflicting-terminals.jsonl", true, "conflicting_terminal_events", "session-1"},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			analysis, err := ParseJSONL(filepath.Join("testdata", test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if analysis.ValidJSONL != test.valid || analysis.FailureClass != test.class {
				t.Fatalf("analysis = %+v", analysis)
			}
			if test.session != "" && (analysis.SessionID == nil || *analysis.SessionID != test.session) {
				t.Fatalf("session = %v", analysis.SessionID)
			}
		})
	}
	success, err := ParseJSONL(filepath.Join("testdata", "success.jsonl"))
	if err != nil || string(success.Usage) != `{"input_tokens":1,"output_tokens":2}` || len(success.UnknownTypes) != 1 {
		t.Fatalf("success metadata = %+v, %v", success, err)
	}
}

func TestTruncatedAndOversizedJSONL(t *testing.T) {
	root := t.TempDir()
	truncated := filepath.Join(root, "truncated.jsonl")
	if err := os.WriteFile(truncated, []byte("{\"type\":\"thread.started\",\"thread_id\":\"x\"}\n{\"type\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	analysis, err := ParseJSONL(truncated)
	if err != nil || analysis.ValidJSONL || analysis.FailureClass != "truncated_jsonl" {
		t.Fatalf("truncated analysis = %+v, %v", analysis, err)
	}

	oversized := filepath.Join(root, "oversized.jsonl")
	if err := os.WriteFile(oversized, append(bytes.Repeat([]byte("x"), MaxJSONLLineBytes+1), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	analysis, err = ParseJSONL(oversized)
	if err != nil || analysis.ValidJSONL || analysis.FailureClass != "jsonl_line_too_long" {
		t.Fatalf("oversized analysis = %+v, %v", analysis, err)
	}
}

func TestStrictFinalOutput(t *testing.T) {
	tests := []struct {
		name string
		data string
		ok   bool
	}{
		{"valid", `{"result":"ok","nonce":"n"}`, true},
		{"unknown field", `{"result":"ok","nonce":"n","extra":true}`, false},
		{"wrong enum", `{"result":"bad","nonce":"n"}`, false},
		{"wrong nonce", `{"result":"ok","nonce":"other"}`, false},
		{"missing required", `{"result":"ok"}`, false},
		{"trailing data", `{"result":"ok","nonce":"n"}{}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "last-message.json")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := ValidateFinalOutput(path, "n"); (err == nil) != test.ok {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSuccessfulClassificationAllowsStderr(t *testing.T) {
	t.Setenv("GO_WANT_CODEX_HELPER", "structured-success")
	cfg := fakeConfig(t, filepath.Join(t.TempDir(), "run"))
	result, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Pass || result.FailureClass != "" || !result.StderrNonempty || result.SessionID == nil || *result.SessionID != "session-1" {
		t.Fatalf("result = %+v", result)
	}
}

func TestClassifierUsesObservableSignalPrecedence(t *testing.T) {
	root := t.TempDir()
	stdout, err := os.ReadFile(filepath.Join("testdata", "success.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stdout.jsonl"), stdout, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "last-message.json"), []byte(`{"result":"ok","nonce":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	zero := 0
	result := Result{ProcessExitCode: &zero, TerminationReason: Exited, Outcome: Fail}
	classify(&result, Config{ArtifactDir: root, Nonce: "nonce"})
	if result.FailureClass != "invalid_final_output" {
		t.Fatalf("final-output classification = %+v", result)
	}
	seven := 7
	result = Result{ProcessExitCode: &seven, TerminationReason: Exited, Outcome: Fail}
	classify(&result, Config{ArtifactDir: root, Nonce: "nonce"})
	if result.FailureClass != "process_failure" {
		t.Fatalf("process classification = %+v", result)
	}
}
