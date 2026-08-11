package codexapp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunProbeVerticalPath(t *testing.T) {
	for _, test := range []struct {
		name, scenario, threadID string
	}{
		{"start", "vertical", ""},
		{"resume explicit thread", "resume", "thread-resume"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := fakeProbeConfig(t, test.scenario)
			config.ThreadID = test.threadID
			result, err := RunProbe(config)
			if err != nil {
				t.Fatal(err)
			}
			wantThread := "thread-1"
			if test.threadID != "" {
				wantThread = test.threadID
			}
			if result.Outcome != Pass || !result.HandshakeComplete || result.ThreadID != wantThread || result.TurnID != "turn-1" || result.ItemID != "item-1" || result.TerminalStatus != "completed" {
				t.Fatalf("result = %+v", result)
			}
			if result.Platform.PlatformOS != "windows" || result.Platform.UserAgent != "codex_cli_rs/0.147.0" {
				t.Fatalf("platform metadata = %+v", result.Platform)
			}
			if result.FinalOutput == nil || result.FinalOutput.Result != "ok" || result.FinalOutput.Nonce != "nonce" {
				t.Fatalf("final output = %+v", result.FinalOutput)
			}
			if result.ProcessExitCode == nil || *result.ProcessExitCode != 0 || !result.StderrNonempty {
				t.Fatalf("process classification = %+v", result)
			}
			assertProbeArtifacts(t, config.ArtifactDir)
		})
	}
}

func TestRunProbeApprovalRequests(t *testing.T) {
	config := fakeProbeConfig(t, "approvals")
	if err := os.Mkdir(filepath.Join(config.Workspace, "public"), 0o700); err != nil {
		t.Fatal(err)
	}
	config.AccessPolicy = AccessPolicy{
		ReadableRoots:     []string{config.Workspace},
		WritableRoots:     []string{filepath.Join(config.Workspace, "public")},
		AllowedCommands:   []CommandForm{{Command: "go test ./...", CWD: config.Workspace}},
		OperatorDecisions: []ApprovalKind{FileChangeApproval},
	}
	config.Operator = func(pending PendingApproval) (ApprovalDecision, error) {
		if pending.Kind != FileChangeApproval || pending.Status != "pending" {
			t.Errorf("operator pending = %+v", pending)
		}
		state, err := os.ReadFile(filepath.Join(config.ArtifactDir, "state.json"))
		if err != nil || !bytes.Contains(state, []byte(`"status":"awaiting_operator"`)) || !bytes.Contains(state, []byte(`"request_id":"opaque-file"`)) {
			t.Errorf("durable operator state = %s, %v", state, err)
		}
		time.Sleep(100 * time.Millisecond)
		return DecisionAccept, nil
	}
	result, err := RunProbe(config)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Pass {
		t.Fatalf("result = %+v", result)
	}
	journal, err := os.ReadFile(filepath.Join(config.ArtifactDir, "approvals.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(journal)
	if strings.Contains(text, "SECRET-REASON") || strings.Contains(text, `.git`) || strings.Count(text, `"type":"sent"`) != 4 || !strings.Contains(text, `"decision_source":"operator"`) {
		t.Fatalf("approval journal = %s", text)
	}
	events, err := os.ReadFile(filepath.Join(config.ArtifactDir, "normalized-events.jsonl"))
	if err != nil || strings.Contains(string(events), "SECRET-REASON") || strings.Contains(string(events), "SECRET-FILE-CONTENT") || !strings.Contains(string(events), "future/while-awaiting-operator") {
		t.Fatalf("normalized events = %s, %v", events, err)
	}
}

func TestRunProbeClassifiesLifecycleFailures(t *testing.T) {
	for _, test := range []struct {
		scenario, failure string
		code              int
	}{
		{"no-terminal", "protocol_failure", 0},
		{"nonzero", "process_failure", 7},
		{"config-denied", "config_incompatible", 0},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			result, err := RunProbe(fakeProbeConfig(t, test.scenario))
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != Fail || result.FailureClass != test.failure || result.ProcessExitCode == nil || *result.ProcessExitCode != test.code {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestRunProbeRejectsNonSchemaFinalText(t *testing.T) {
	for _, scenario := range []string{"structured-extra", "structured-missing", "structured-wrapped"} {
		t.Run(scenario, func(t *testing.T) {
			result, err := RunProbe(fakeProbeConfig(t, scenario))
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != Fail || result.FailureClass != "structured_output_failure" || result.FinalOutput != nil {
				t.Fatalf("result = %+v", result)
			}
		})
	}
	t.Run("nonce mismatch", func(t *testing.T) {
		config := fakeProbeConfig(t, "vertical")
		config.Nonce = "different"
		result, err := RunProbe(config)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != Fail || result.FailureClass != "structured_output_failure" {
			t.Fatalf("result = %+v", result)
		}
	})
}

func fakeProbeConfig(t *testing.T, scenario string) ProbeConfig {
	t.Helper()
	t.Setenv("GO_WANT_CODEXAPP_FAKE", scenario)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace & [role]")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return ProbeConfig{
		Executable: os.Args[0], Workspace: workspace, Prompt: "return the nonce", Nonce: "nonce",
		ArtifactDir: filepath.Join(root, "artifacts"), OutputSchema: append(json.RawMessage(nil), DefaultOutputSchema...),
	}
}

func assertProbeArtifacts(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"manifest.json", "stdout.jsonl", "stderr.log", "normalized-events.jsonl", "approvals.jsonl", "state.json", "result.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	stdout, err := os.ReadFile(filepath.Join(dir, "stdout.jsonl"))
	if err != nil || !bytes.Contains(stdout, []byte(`"method":"turn/completed"`)) {
		t.Fatalf("raw stdout = %q, %v", stdout, err)
	}
	stderr, err := os.ReadFile(filepath.Join(dir, "stderr.log"))
	if err != nil || string(stderr) != "warning\n" {
		t.Fatalf("stderr = %q, %v", stderr, err)
	}
	events, err := os.ReadFile(filepath.Join(dir, "normalized-events.jsonl"))
	if err != nil || !strings.Contains(string(events), `"seq":1`) || !strings.Contains(string(events), `future/notification`) {
		t.Fatalf("events = %q, %v", events, err)
	}
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest probeManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"app-server", "--stdio", "--strict-config", "-c", `approvals_reviewer="user"`}
	if strings.Join(manifest.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("argv = %q", manifest.Args)
	}
}
