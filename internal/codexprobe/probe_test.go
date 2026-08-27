package codexprobe

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime/codexapp"
)

var ParseMessage = codexapp.ParseMessage

type replayConn struct {
	mu     sync.Mutex
	ready  *sync.Cond
	lines  [][]byte
	index  int
	sent   map[string]bool
	output bytes.Buffer
}

func newReplayConn(data []byte) *replayConn {
	connection := &replayConn{sent: make(map[string]bool)}
	connection.ready = sync.NewCond(&connection.mu)
	for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		if len(line) != 0 {
			connection.lines = append(connection.lines, line)
		}
	}
	return connection
}

func (connection *replayConn) Read(buffer []byte) (int, error) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.index == len(connection.lines) {
		return 0, io.EOF
	}
	line := connection.lines[connection.index]
	if message, err := ParseMessage(bytes.TrimSpace(line)); err == nil && message.Kind == Response {
		for !connection.sent[message.ID.Key()] {
			connection.ready.Wait()
		}
	}
	connection.index++
	return copy(buffer, line), nil
}

func (connection *replayConn) Write(line []byte) (int, error) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if message, err := ParseMessage(bytes.TrimSpace(line)); err == nil && message.Kind == Request {
		connection.sent[message.ID.Key()] = true
		connection.ready.Broadcast()
	}
	return connection.output.Write(line)
}

func (*replayConn) Close() error { return nil }

func TestReplaySanitizedFixtures(t *testing.T) {
	hashes := map[string]string{
		"success.jsonl":          "bac86a0824e5d6bcb366d1f29c8485644c9315848cb23494d5e22bfeb71d9a5a",
		"approval.jsonl":         "b5343a74f8e919373f3d22813939c1f1e67f153e3a06c58ff3c4e5bf4f133597",
		"process-failure.jsonl":  "dfa3ba71a6395ed5cf609dd371e305d5918f84d7eac9a6fd27d2875db7396f79",
		"protocol-failure.jsonl": "e4527e3be4c05640044ce066953e70e29beb569eec663adf03de9c0acc3f2ee9",
	}
	localPath := regexp.MustCompile(`(?i)([a-z]:[\\/]|\\\\|/(users|home)/)`)
	for name, wantHash := range hashes {
		name, wantHash := name, wantHash
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			sum := fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))))
			if sum != wantHash {
				t.Fatalf("fixture hash = %s", sum)
			}
			lower := strings.ToLower(string(data))
			for _, forbidden := range []string{"canary", "authorization", "bearer ", "api_key", "access_token", "password", ".stepan"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("fixture contains forbidden marker %q", forbidden)
				}
			}
			if localPath.Match(data) {
				t.Fatal("fixture contains a local absolute path")
			}

			workspace := t.TempDir()
			writable := filepath.Join(workspace, "public")
			if err := os.Mkdir(writable, 0o700); err != nil {
				t.Fatal(err)
			}
			manager, cleanup := testApprovalManager(t, workspace, AccessPolicy{
				ReadableRoots: []string{workspace}, WritableRoots: []string{writable},
			}, nil)
			defer cleanup()
			connection := newReplayConn(data)
			var events bytes.Buffer
			result := Result{Outcome: Fail}
			protocolErr := runProtocol(NewTransport(connection, connection), connection, &events, Config{
				Workspace: workspace, Prompt: "replay", Nonce: "replay", OutputSchema: DefaultOutputSchema,
			}, &result, manager)
			code := 0
			var waitErr error
			if name == "process-failure.jsonl" {
				code, waitErr = 7, errors.New("exit status 7")
			}
			result.ProcessExitCode = &code
			classifyProbeResult(&result, waitErr, protocolErr)
			wantOutcome, wantClass := Pass, ""
			switch name {
			case "process-failure.jsonl":
				wantOutcome, wantClass = Fail, "process_failure"
			case "protocol-failure.jsonl":
				wantOutcome, wantClass = Fail, "protocol_failure"
			}
			if result.Outcome != wantOutcome || result.FailureClass != wantClass {
				t.Fatalf("result = %+v, protocol error = %v", result, protocolErr)
			}
			if name == "approval.jsonl" && !bytes.Contains(connection.output.Bytes(), []byte(`"id":"approval-replay","result":{"decision":"accept"}`)) {
				t.Fatalf("approval response = %s", connection.output.Bytes())
			}
		})
	}
}

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
			result, err := Run(config)
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
	result, err := Run(config)
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
			result, err := Run(fakeProbeConfig(t, test.scenario))
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
			result, err := Run(fakeProbeConfig(t, scenario))
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
		result, err := Run(config)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != Fail || result.FailureClass != "structured_output_failure" {
			t.Fatalf("result = %+v", result)
		}
	})
}

func fakeProbeConfig(t *testing.T, scenario string) Config {
	t.Helper()
	t.Setenv("GO_WANT_CODEXAPP_FAKE", scenario)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace & [role]")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return Config{
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
