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
	Exited               TerminationReason = "exited"
	SpawnFailed          TerminationReason = "spawn_failed"
	OperatorCanceled     TerminationReason = "operator_canceled"
	TimedOut             TerminationReason = "timed_out"
	KilledAfterIOTimeout TerminationReason = "killed_after_io_timeout"
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
	var journal *Journal
	state := State{RunID: result.InvocationID, InvocationID: result.InvocationID, Status: "running"}
	finish := func() (Result, error) {
		result.FinishedAt = time.Now().UTC()
		result.DurationMS = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
		var finishErr error
		if journal != nil {
			state.LastSeq = journal.LastSeq()
			if result.Outcome == Pass {
				state.Status = "completed"
			} else if result.TerminationReason == OperatorCanceled || result.TerminationReason == TimedOut {
				state.Status = "interrupted"
			} else {
				state.Status = "failed"
			}
			finishErr = journal.Close()
			if err := WriteState(filepath.Join(cfg.ArtifactDir, "state.json"), state); finishErr == nil {
				finishErr = err
			}
		}
		if err := writeJSONAtomic(filepath.Join(cfg.ArtifactDir, "result.json"), result); err != nil {
			return result, err
		}
		return result, finishErr
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
	journal, err = NewJournal(filepath.Join(cfg.ArtifactDir, "events.jsonl"), result.InvocationID, result.InvocationID)
	if err != nil {
		stdout.Close()
		stderr.Close()
		return Result{}, err
	}
	if err := WriteState(filepath.Join(cfg.ArtifactDir, "state.json"), state); err != nil {
		stdout.Close()
		stderr.Close()
		journal.Close()
		return Result{}, err
	}

	job, err := newProcessJob()
	if err != nil {
		result.TerminationReason = SpawnFailed
		result.FailureClass = "job_creation_failed"
		result.Detail = err.Error()
		stdout.Close()
		stderr.Close()
		return finish()
	}
	controller := newCancelController(job.Close)
	cmd := exec.Command(cfg.Executable, cfg.Args()...)
	cmd.Dir = cfg.Workspace
	cmd.Stdin = bytes.NewReader(cfg.Prompt)
	// Wrapping the files makes os/exec create and concurrently drain separate pipes.
	cmd.Stdout = observedWriter{"stdout", stdout, journal}
	cmd.Stderr = observedWriter{"stderr", stderr, journal}
	cmd.WaitDelay = cfg.IOGrace
	if err := cmd.Start(); err != nil {
		controller.Close()
		result.TerminationReason = SpawnFailed
		result.FailureClass = "spawn_failure"
		result.Detail = err.Error()
		stdout.Close()
		stderr.Close()
		return finish()
	}
	if err := job.Assign(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		controller.Cancel(SpawnFailed)
		waitErr := cmd.Wait()
		if cmd.ProcessState != nil {
			code := cmd.ProcessState.ExitCode()
			result.ProcessExitCode = &code
		}
		result.TerminationReason = SpawnFailed
		result.FailureClass = "job_assignment_failed"
		result.Detail = err.Error()
		if waitErr != nil && result.Detail == "" {
			result.Detail = waitErr.Error()
		}
		stdout.Close()
		stderr.Close()
		return finish()
	}

	processDone := make(chan struct{})
	watchDone := make(chan struct{})
	timer := time.NewTimer(cfg.Timeout)
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			controller.Cancel(OperatorCanceled)
		case <-timer.C:
			controller.Cancel(TimedOut)
		case <-processDone:
		}
	}()
	waitErr := cmd.Wait()
	close(processDone)
	timer.Stop()
	<-watchDone
	controller.Close()
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
			if controller.Reason() == "" {
				result.TerminationReason = KilledAfterIOTimeout
			}
		}
	}
	if reason := controller.Reason(); reason != "" {
		result.TerminationReason = reason
		result.FailureClass = string(reason)
	}
	if err := controller.Err(); err != nil && result.Detail == "" {
		result.Detail = err.Error()
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

// observedWriter intentionally hides *os.File from os/exec so it uses a pipe.
type observedWriter struct {
	stream      string
	destination io.Writer
	journal     *Journal
}

func (w observedWriter) Write(data []byte) (int, error) {
	return w.journal.Observe(w.stream, w.destination, data)
}

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
	return replaceFile(temp, path)
}
