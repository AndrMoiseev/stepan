package codexexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type TerminationReason string

const (
	Exited      TerminationReason = "exited"
	SpawnFailed TerminationReason = "spawn_failed"
)

type Outcome string

const (
	Pass Outcome = "PASS"
	Fail Outcome = "FAIL"
)

type Result struct {
	SchemaVersion     int               `json:"schema_version"`
	InvocationID      string            `json:"invocation_id"`
	CodexVersion      string            `json:"codex_version"`
	StartedAt         time.Time         `json:"started_at"`
	FinishedAt        time.Time         `json:"finished_at"`
	DurationMS        int64             `json:"duration_ms"`
	SessionID         *string           `json:"session_id"`
	ProcessExitCode   *int              `json:"process_exit_code"`
	TerminationReason TerminationReason `json:"termination_reason"`
	TerminalEvent     *string           `json:"terminal_event"`
	StdoutValidJSONL  bool              `json:"stdout_valid_jsonl"`
	FinalOutputValid  bool              `json:"final_output_valid"`
	StderrNonempty    bool              `json:"stderr_nonempty"`
	Outcome           Outcome           `json:"outcome"`
	FailureClass      string            `json:"failure_class,omitempty"`
	Usage             json.RawMessage   `json:"usage,omitempty"`
	Detail            string            `json:"detail,omitempty"`
}

type manifest struct {
	SchemaVersion int        `json:"schema_version"`
	InvocationID  string     `json:"invocation_id"`
	CodexVersion  string     `json:"codex_version"`
	GoVersion     string     `json:"go_version"`
	OS            string     `json:"os"`
	Arch          string     `json:"arch"`
	Executable    string     `json:"executable"`
	Workspace     string     `json:"workspace"`
	SchemaPath    string     `json:"schema_path"`
	Args          []string   `json:"args"`
	Sandbox       Sandbox    `json:"sandbox"`
	Timeout       string     `json:"timeout"`
	ConfigMode    ConfigMode `json:"config_mode"`
	PromptSHA256  string     `json:"prompt_sha256"`
}

func Version(ctx context.Context, executable string) (string, error) {
	out, err := exec.CommandContext(ctx, executable, "--version").Output()
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(out))
	version = strings.TrimPrefix(version, "codex-cli ")
	if version == "" {
		return "", errors.New("empty Codex version")
	}
	return version, nil
}

func Run(ctx context.Context, cfg Config) (Result, error) {
	cfg, err := cfg.Validate()
	if err != nil {
		return Result{}, err
	}
	if cfg.IOGrace <= 0 {
		cfg.IOGrace = 500 * time.Millisecond
	}
	started := time.Now().UTC()
	result := Result{
		SchemaVersion:     1,
		CodexVersion:      cfg.CodexVersion,
		StartedAt:         started,
		TerminationReason: Exited,
		Outcome:           Fail,
	}
	result.InvocationID, err = randomID()
	if err != nil {
		return Result{}, err
	}
	if err := os.Mkdir(cfg.ArtifactDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create artifact directory: %w", err)
	}
	finish := func() (Result, error) {
		result.FinishedAt = time.Now().UTC()
		result.DurationMS = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
		if err := writeJSONAtomic(filepath.Join(cfg.ArtifactDir, "result.json"), result); err != nil {
			return result, err
		}
		return result, nil
	}

	promptHash := sha256.Sum256(cfg.Prompt)
	m := manifest{
		SchemaVersion: 1, InvocationID: result.InvocationID,
		CodexVersion: cfg.CodexVersion, GoVersion: runtime.Version(),
		OS: runtime.GOOS, Arch: runtime.GOARCH,
		Executable: cfg.Executable, Workspace: cfg.Workspace, SchemaPath: cfg.SchemaPath,
		Args: cfg.Args(), Sandbox: cfg.Sandbox, Timeout: cfg.Timeout.String(), ConfigMode: cfg.ConfigMode,
		PromptSHA256: hex.EncodeToString(promptHash[:]),
	}
	if err := writeJSON(filepath.Join(cfg.ArtifactDir, "manifest.json"), m); err != nil {
		return Result{}, err
	}
	stdout, err := os.OpenFile(filepath.Join(cfg.ArtifactDir, "stdout.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Result{}, err
	}
	stderr, err := os.OpenFile(filepath.Join(cfg.ArtifactDir, "stderr.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		stdout.Close()
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, cfg.Executable, cfg.Args()...)
	cmd.Dir = cfg.Workspace
	cmd.Stdin = bytes.NewReader(cfg.Prompt)
	// Wrapping the files makes os/exec create and concurrently drain separate pipes.
	cmd.Stdout = writer{stdout}
	cmd.Stderr = writer{stderr}
	cmd.WaitDelay = cfg.IOGrace
	if err := cmd.Start(); err != nil {
		result.TerminationReason = SpawnFailed
		result.FailureClass = "spawn_failure"
		result.Detail = err.Error()
		stdout.Close()
		stderr.Close()
		return finish()
	}

	waitErr := cmd.Wait()
	stdoutCloseErr := stdout.Close()
	stderrCloseErr := stderr.Close()
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		result.ProcessExitCode = &code
	}
	if waitErr != nil {
		result.Detail = waitErr.Error()
		if errors.Is(waitErr, exec.ErrWaitDelay) {
			result.FailureClass = "io_timeout"
		}
	}
	if stdoutCloseErr != nil && result.Detail == "" {
		result.Detail = stdoutCloseErr.Error()
	}
	if stderrCloseErr != nil && result.Detail == "" {
		result.Detail = stderrCloseErr.Error()
	}
	if info, err := os.Stat(filepath.Join(cfg.ArtifactDir, "stderr.log")); err == nil {
		result.StderrNonempty = info.Size() > 0
	}
	classify(&result, cfg)
	return finish()
}

// writer intentionally hides *os.File from os/exec so it uses a pipe.
type writer struct{ io.Writer }

func randomID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func writeJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(value); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func writeJSONAtomic(path string, value any) error {
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := json.NewEncoder(file).Encode(value); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
