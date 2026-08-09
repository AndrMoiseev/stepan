package codexexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTransport(t *testing.T) {
	tests := []struct {
		name       string
		scenario   string
		exitCode   int
		stderrSize int
	}{
		{"large separate streams", "large", 0, 1 << 20},
		{"warning with success", "warning", 0, len("warning\n")},
		{"nonzero exit", "nonzero", 7, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GO_WANT_CODEX_HELPER", test.scenario)
			cfg := fakeConfig(t, filepath.Join(t.TempDir(), "run"))
			result, err := Run(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if result.ProcessExitCode == nil || *result.ProcessExitCode != test.exitCode {
				t.Fatalf("exit code = %v, want %d", result.ProcessExitCode, test.exitCode)
			}
			stdout, err := os.ReadFile(filepath.Join(cfg.ArtifactDir, "stdout.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			stderr, err := os.ReadFile(filepath.Join(cfg.ArtifactDir, "stderr.log"))
			if err != nil {
				t.Fatal(err)
			}
			if test.scenario == "large" && (!bytes.Equal(stdout, bytes.Repeat([]byte("o"), 1<<20)) || !bytes.Equal(stderr, bytes.Repeat([]byte("e"), 1<<20))) {
				t.Fatal("large stdout/stderr bytes were lost or mixed")
			}
			if len(stderr) != test.stderrSize {
				t.Fatalf("stderr size = %d, want %d", len(stderr), test.stderrSize)
			}
			if result.StderrNonempty != (test.stderrSize > 0) {
				t.Fatalf("stderr_nonempty = %v", result.StderrNonempty)
			}
			manifestData, err := os.ReadFile(filepath.Join(cfg.ArtifactDir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(manifestData, []byte(`"prompt"`)) || bytes.Contains(manifestData, []byte("environment")) {
				t.Fatal("manifest contains prompt or environment")
			}
			eventData, err := os.ReadFile(filepath.Join(cfg.ArtifactDir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(eventData, []byte(`"prompt"`)) || bytes.Contains(eventData, []byte("environment")) {
				t.Fatal("event journal contains prompt or environment")
			}
			assertResultWrittenLast(t, cfg.ArtifactDir)
		})
	}
}

func TestSpawnFailure(t *testing.T) {
	root := t.TempDir()
	badExecutable := filepath.Join(root, "bad.exe")
	if err := os.WriteFile(badExecutable, []byte("not a Windows executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fakeConfig(t, filepath.Join(root, "run"))
	cfg.Executable = badExecutable
	result, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.TerminationReason != SpawnFailed || result.ProcessExitCode != nil {
		t.Fatalf("unexpected spawn result: %+v", result)
	}
	assertResultWrittenLast(t, cfg.ArtifactDir)
}

func TestInheritedPipeIsBounded(t *testing.T) {
	t.Setenv("GO_WANT_CODEX_HELPER", "leave-pipe-open")
	cfg := fakeConfig(t, filepath.Join(t.TempDir(), "run"))
	cfg.IOGrace = 100 * time.Millisecond
	started := time.Now()
	result, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("inherited pipe blocked transport")
	}
	if result.ProcessExitCode == nil || *result.ProcessExitCode != 0 {
		t.Fatalf("parent exit code not preserved: %+v", result)
	}
	if result.FailureClass != "io_timeout" {
		t.Fatalf("inherited pipe classification = %+v", result)
	}
}

func TestCancellationPreservesFirstReason(t *testing.T) {
	closes := 0
	controller := newCancelController(func() error {
		closes++
		return nil
	})
	controller.Cancel(TimedOut)
	controller.Cancel(OperatorCanceled)
	controller.Close()
	if controller.Reason() != TimedOut || closes != 1 {
		t.Fatalf("reason = %q, closes = %d", controller.Reason(), closes)
	}
}

func TestMain(m *testing.M) {
	scenario := os.Getenv("GO_WANT_CODEX_HELPER")
	if scenario == "" {
		os.Exit(m.Run())
	}
	switch scenario {
	case "large":
		done := make(chan struct{}, 2)
		go func() { os.Stdout.Write(bytes.Repeat([]byte("o"), 1<<20)); done <- struct{}{} }()
		go func() { os.Stderr.Write(bytes.Repeat([]byte("e"), 1<<20)); done <- struct{}{} }()
		<-done
		<-done
		os.Exit(0)
	case "warning":
		fmt.Fprintln(os.Stderr, "warning")
		os.Exit(0)
	case "nonzero":
		os.Exit(7)
	case "structured-success":
		for i, arg := range os.Args[:len(os.Args)-1] {
			if arg == "--output-last-message" {
				if err := os.WriteFile(os.Args[i+1], []byte(`{"result":"ok","nonce":"nonce"}`), 0o600); err != nil {
					os.Exit(10)
				}
				break
			}
		}
		fmt.Fprintln(os.Stdout, `{"type":"thread.started","thread_id":"session-1"}`)
		fmt.Fprintln(os.Stdout, `{"type":"future.event"}`)
		fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":1}}`)
		fmt.Fprintln(os.Stderr, "warning")
		os.Exit(0)
	case "leave-pipe-open":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "GO_WANT_CODEX_HELPER=hold-pipe")
		cmd.Dir = os.TempDir()
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(9)
		}
		os.Exit(0)
	case "hold-pipe":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "tree-parent":
		time.Sleep(50 * time.Millisecond)
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "GO_WANT_CODEX_HELPER=tree-child")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(11)
		}
		pidFile := os.Getenv("CODEX_HELPER_PID_FILE")
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(pidFile); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(12)
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprintln(os.Stdout, `{"type":"thread.started","thread_id":"interrupted-session"}`)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "tree-child":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "GO_WANT_CODEX_HELPER=tree-grandchild")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(13)
		}
		data, err := json.Marshal([]int{os.Getppid(), os.Getpid(), cmd.Process.Pid})
		if err != nil || os.WriteFile(os.Getenv("CODEX_HELPER_PID_FILE"), data, 0o600) != nil {
			os.Exit(14)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "tree-grandchild":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		os.Exit(8)
	}
}

func fakeConfig(t *testing.T, artifactDir string) Config {
	t.Helper()
	root := t.TempDir()
	schema := filepath.Join(root, "schema.json")
	if err := os.WriteFile(schema, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		Executable: os.Args[0], CodexVersion: "fake",
		Workspace: root, Prompt: []byte("prompt"), Sandbox: ReadOnly,
		SchemaPath: schema, Timeout: time.Second, ArtifactDir: artifactDir,
		Nonce: "nonce", ConfigMode: Isolated,
	}
}

func assertResultWrittenLast(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, "result.json.tmp-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary result files remain: %v, %v", temps, err)
	}
}
