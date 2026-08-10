package codexapp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
)

type Outcome string

const (
	Pass Outcome = "PASS"
	Fail Outcome = "FAIL"
)

var DefaultOutputSchema = json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"result":{"type":"string","enum":["ok"]},"nonce":{"type":"string","minLength":1}},"required":["result","nonce"],"additionalProperties":false}`)

type ProbeConfig struct {
	Executable   string
	Workspace    string
	Prompt       string
	Nonce        string
	ArtifactDir  string
	ThreadID     string
	OutputSchema json.RawMessage
}

func (config ProbeConfig) Args() []string {
	return []string{"app-server", "--stdio", "--strict-config", "-c", `approvals_reviewer="user"`}
}

func (config ProbeConfig) validate() (ProbeConfig, error) {
	var err error
	config.Executable, err = resolveExecutable(config.Executable)
	if err != nil {
		return ProbeConfig{}, err
	}
	if !filepath.IsAbs(config.Workspace) {
		return ProbeConfig{}, errors.New("workspace must be absolute")
	}
	if info, err := os.Stat(config.Workspace); err != nil || !info.IsDir() {
		return ProbeConfig{}, errors.New("workspace must be an existing directory")
	}
	if !filepath.IsAbs(config.ArtifactDir) {
		return ProbeConfig{}, errors.New("artifact directory must be absolute")
	}
	if config.Prompt == "" || strings.TrimSpace(config.Nonce) == "" {
		return ProbeConfig{}, errors.New("prompt and nonce are required")
	}
	if len(config.OutputSchema) == 0 {
		config.OutputSchema = append(json.RawMessage(nil), DefaultOutputSchema...)
	}
	var schema, supportedSchema map[string]any
	if err := json.Unmarshal(config.OutputSchema, &schema); err != nil || schema == nil {
		return ProbeConfig{}, errors.New("output schema must be a JSON object")
	}
	_ = json.Unmarshal(DefaultOutputSchema, &supportedSchema)
	if !reflect.DeepEqual(schema, supportedSchema) {
		return ProbeConfig{}, errors.New("only the iteration-0 structured output schema is supported")
	}
	return config, nil
}

type Platform struct {
	CodexHome      string `json:"codex_home"`
	PlatformFamily string `json:"platform_family"`
	PlatformOS     string `json:"platform_os"`
	UserAgent      string `json:"user_agent"`
}

type FinalOutput struct {
	Result string `json:"result"`
	Nonce  string `json:"nonce"`
}

type ProbeResult struct {
	Outcome           Outcome      `json:"outcome"`
	FailureClass      string       `json:"failure_class,omitempty"`
	Detail            string       `json:"detail,omitempty"`
	ProcessExitCode   *int         `json:"process_exit_code"`
	StderrNonempty    bool         `json:"stderr_nonempty"`
	HandshakeComplete bool         `json:"handshake_complete"`
	ThreadID          string       `json:"thread_id,omitempty"`
	TurnID            string       `json:"turn_id,omitempty"`
	ItemID            string       `json:"item_id,omitempty"`
	TerminalStatus    string       `json:"terminal_status,omitempty"`
	Platform          Platform     `json:"platform"`
	FinalOutput       *FinalOutput `json:"final_output,omitempty"`
}

type probeManifest struct {
	SchemaVersion int       `json:"schema_version"`
	StartedAt     time.Time `json:"started_at"`
	GoVersion     string    `json:"go_version"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	Executable    string    `json:"executable"`
	Workspace     string    `json:"workspace"`
	Args          []string  `json:"args"`
	PromptSHA256  string    `json:"prompt_sha256"`
	SchemaSHA256  string    `json:"output_schema_sha256"`
}

type probeState struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type protocolEvent struct {
	Seq       uint64          `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	Kind      MessageKind     `json:"kind"`
	Method    string          `json:"method,omitempty"`
	ID        string          `json:"id,omitempty"`
	Raw       json.RawMessage `json:"raw"`
}

// RunProbe executes the version-pinned stdio vertical path. Cancellation and
// approval handling are deliberately outside this iteration step.
func RunProbe(config ProbeConfig) (result ProbeResult, runErr error) {
	config, err := config.validate()
	if err != nil {
		return ProbeResult{}, err
	}
	if err := os.Mkdir(config.ArtifactDir, 0o700); err != nil {
		return ProbeResult{}, fmt.Errorf("create artifact directory: %w", err)
	}
	result.Outcome = Fail
	promptHash := sha256.Sum256([]byte(config.Prompt))
	schemaHash := sha256.Sum256(config.OutputSchema)
	manifest := probeManifest{
		SchemaVersion: 1, StartedAt: time.Now().UTC(), GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Executable: config.Executable, Workspace: config.Workspace, Args: config.Args(),
		PromptSHA256: hex.EncodeToString(promptHash[:]), SchemaSHA256: hex.EncodeToString(schemaHash[:]),
	}
	if err := writeJSONExclusive(filepath.Join(config.ArtifactDir, "manifest.json"), manifest); err != nil {
		return ProbeResult{}, err
	}
	stdoutFile, err := createArtifact(config.ArtifactDir, "stdout.jsonl")
	if err != nil {
		return ProbeResult{}, err
	}
	stderrFile, err := createArtifact(config.ArtifactDir, "stderr.log")
	if err != nil {
		stdoutFile.Close()
		return ProbeResult{}, err
	}
	eventsFile, err := createArtifact(config.ArtifactDir, "normalized-events.jsonl")
	if err != nil {
		stdoutFile.Close()
		stderrFile.Close()
		return ProbeResult{}, err
	}
	approvalsFile, err := createArtifact(config.ArtifactDir, "approvals.jsonl")
	if err != nil {
		stdoutFile.Close()
		stderrFile.Close()
		eventsFile.Close()
		return ProbeResult{}, err
	}
	_ = approvalsFile.Close()

	finish := func() (ProbeResult, error) {
		closeErr := errors.Join(stdoutFile.Close(), stderrFile.Close(), eventsFile.Close())
		status := "failed"
		if result.Outcome == Pass {
			status = "completed"
		}
		if err := writeJSONAtomic(filepath.Join(config.ArtifactDir, "state.json"), probeState{Status: status, UpdatedAt: time.Now().UTC()}); err != nil {
			return result, errors.Join(runErr, closeErr, err)
		}
		if err := writeJSONAtomic(filepath.Join(config.ArtifactDir, "result.json"), result); err != nil {
			return result, errors.Join(runErr, closeErr, err)
		}
		return result, errors.Join(runErr, closeErr)
	}

	command := exec.Command(config.Executable, config.Args()...)
	command.Dir = config.Workspace
	stdin, err := command.StdinPipe()
	if err != nil {
		result.FailureClass, result.Detail = "spawn_failure", err.Error()
		return finish()
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		result.FailureClass, result.Detail = "spawn_failure", err.Error()
		return finish()
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		result.FailureClass, result.Detail = "spawn_failure", err.Error()
		return finish()
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		result.FailureClass, result.Detail = "spawn_failure", err.Error()
		return finish()
	}
	stderrDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(stderrFile, stderr)
		stderrDone <- copyErr
	}()

	recordedStdout := io.TeeReader(stdout, stdoutFile)
	transport := NewTransport(recordedStdout, stdin)
	protocolErr := runProtocol(transport, stdin, eventsFile, config, &result)
	if protocolErr != nil {
		_ = stdin.Close()
		_, _ = io.Copy(io.Discard, transport.decoder.reader)
	}
	waitErr := command.Wait()
	stderrErr := <-stderrDone
	if command.ProcessState != nil {
		code := command.ProcessState.ExitCode()
		result.ProcessExitCode = &code
	}
	if info, err := stderrFile.Stat(); err == nil {
		result.StderrNonempty = info.Size() > 0
	}
	if result.ProcessExitCode == nil || *result.ProcessExitCode != 0 {
		result.Outcome = Fail
		result.FailureClass = "process_failure"
		result.Detail = errorDetail(waitErr, protocolErr)
	} else if protocolErr != nil {
		result.Outcome = Fail
		if result.FailureClass == "" {
			result.FailureClass = "protocol_failure"
		}
		result.Detail = protocolErr.Error()
	}
	runErr = stderrErr
	return finish()
}

func runProtocol(transport *Transport, stdin io.Closer, events io.Writer, config ProbeConfig, result *ProbeResult) error {
	if err := transport.SendRequest(IntID(1), "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "stepan", "version": "0"},
	}); err != nil {
		return err
	}
	stage := 1
	terminalSeen := false
	var seq uint64
	for {
		message, err := transport.Read()
		if errors.Is(err, io.EOF) {
			if !terminalSeen {
				return errors.New("connection closed before terminal turn/completed")
			}
			return nil
		}
		if err != nil {
			return err
		}
		seq++
		if err := appendProtocolEvent(events, seq, message); err != nil {
			return err
		}
		switch message.Kind {
		case Request:
			return fmt.Errorf("unsupported server request %q", message.Method)
		case Response:
			if message.Error != nil {
				return fmt.Errorf("%s failed: %d %s", requestName(stage), message.Error.Code, message.Error.Message)
			}
			if err := advanceProtocol(transport, message, &stage, config, result); err != nil {
				return err
			}
		case Notification:
			if message.Method != "turn/completed" {
				continue
			}
			if terminalSeen {
				return errors.New("duplicate terminal notification")
			}
			terminalSeen = true
			if stage != 5 {
				return errors.New("terminal notification arrived before turn/start response")
			}
			if err := decodeTerminal(message.Params, config.Nonce, result); err != nil {
				return err
			}
			_ = stdin.Close()
		}
	}
}

func advanceProtocol(transport *Transport, message Message, stage *int, config ProbeConfig, result *ProbeResult) error {
	if message.ID.Key() != IntID(int64(*stage)).Key() {
		return fmt.Errorf("unexpected response ID %s at stage %d", message.ID.Key(), *stage)
	}
	switch *stage {
	case 1:
		var response struct {
			CodexHome      string `json:"codexHome"`
			PlatformFamily string `json:"platformFamily"`
			PlatformOS     string `json:"platformOs"`
			UserAgent      string `json:"userAgent"`
		}
		if err := json.Unmarshal(message.Result, &response); err != nil || response.CodexHome == "" || response.PlatformFamily == "" || response.PlatformOS == "" || response.UserAgent == "" {
			return errors.New("invalid initialize response")
		}
		result.Platform = Platform(response)
		if err := transport.SendNotification("initialized", struct{}{}); err != nil {
			return err
		}
		result.HandshakeComplete = true
		if err := transport.SendRequest(IntID(2), "configRequirements/read", (*struct{})(nil)); err != nil {
			return err
		}
	case 2:
		if err := validateRequirements(message.Result); err != nil {
			result.FailureClass = "config_incompatible"
			return err
		}
		if config.ThreadID == "" {
			if err := transport.SendRequest(IntID(3), "thread/start", map[string]any{"cwd": config.Workspace}); err != nil {
				return err
			}
		} else if err := transport.SendRequest(IntID(3), "thread/resume", map[string]string{"threadId": config.ThreadID}); err != nil {
			return err
		}
	case 3:
		var response struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(message.Result, &response); err != nil || response.Thread.ID == "" {
			return errors.New("thread response has no thread.id")
		}
		if config.ThreadID != "" && response.Thread.ID != config.ThreadID {
			return errors.New("thread/resume returned a different thread.id")
		}
		result.ThreadID = response.Thread.ID
		params := map[string]any{
			"threadId":       result.ThreadID,
			"input":          []map[string]string{{"type": "text", "text": config.Prompt}},
			"cwd":            config.Workspace,
			"approvalPolicy": "on-request",
			"sandboxPolicy":  map[string]any{"type": "readOnly", "networkAccess": false},
			"outputSchema":   json.RawMessage(config.OutputSchema),
		}
		if err := transport.SendRequest(IntID(4), "turn/start", params); err != nil {
			return err
		}
	case 4:
		var response struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(message.Result, &response); err != nil || response.Turn.ID == "" {
			return errors.New("turn/start response has no turn.id")
		}
		result.TurnID = response.Turn.ID
	default:
		return errors.New("unexpected response after turn/start")
	}
	(*stage)++
	return nil
}

func validateRequirements(raw json.RawMessage) error {
	var response struct {
		Requirements *struct {
			AllowedApprovalPolicies []json.RawMessage `json:"allowedApprovalPolicies"`
			AllowedSandboxModes     []string          `json:"allowedSandboxModes"`
		} `json:"requirements"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return errors.New("invalid configRequirements/read response")
	}
	if response.Requirements == nil {
		return nil
	}
	if policies := response.Requirements.AllowedApprovalPolicies; policies != nil {
		allowed := false
		for _, policy := range policies {
			var value string
			if json.Unmarshal(policy, &value) == nil && value == "on-request" {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("required approval policy on-request is not allowed")
		}
	}
	if modes := response.Requirements.AllowedSandboxModes; modes != nil {
		allowed := false
		for _, mode := range modes {
			allowed = allowed || mode == "read-only"
		}
		if !allowed {
			return errors.New("required sandbox mode read-only is not allowed")
		}
	}
	return nil
}

func decodeTerminal(raw json.RawMessage, nonce string, result *ProbeResult) error {
	var notification struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string            `json:"id"`
			Status string            `json:"status"`
			Items  []json.RawMessage `json:"items"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &notification); err != nil {
		return errors.New("invalid turn/completed notification")
	}
	if notification.ThreadID != result.ThreadID || notification.Turn.ID != result.TurnID {
		return errors.New("terminal notification correlation IDs do not match")
	}
	result.TerminalStatus = notification.Turn.Status
	if notification.Turn.Status != "completed" {
		result.FailureClass = "turn_failure"
		return fmt.Errorf("turn terminal status is %q", notification.Turn.Status)
	}
	type agentMessage struct {
		ID    string  `json:"id"`
		Type  string  `json:"type"`
		Phase *string `json:"phase"`
		Text  string  `json:"text"`
	}
	var finals, unknownPhase []agentMessage
	for _, rawItem := range notification.Turn.Items {
		var item agentMessage
		if err := json.Unmarshal(rawItem, &item); err != nil || item.Type != "agentMessage" {
			continue
		}
		if item.ID == "" {
			return errors.New("agentMessage has no item.id")
		}
		if item.Phase != nil && *item.Phase == "final_answer" {
			finals = append(finals, item)
		} else if item.Phase == nil {
			unknownPhase = append(unknownPhase, item)
		}
	}
	if len(finals) == 0 && len(unknownPhase) == 1 {
		finals = unknownPhase
	}
	if len(finals) != 1 {
		result.FailureClass = "structured_output_failure"
		return fmt.Errorf("terminal turn has %d final agent messages", len(finals))
	}
	var output FinalOutput
	decoder := json.NewDecoder(bytes.NewBufferString(finals[0].Text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		result.FailureClass = "structured_output_failure"
		return fmt.Errorf("decode structured final output: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		result.FailureClass = "structured_output_failure"
		return errors.New("structured final output has trailing data")
	}
	if output.Result != "ok" || output.Nonce == "" || output.Nonce != nonce {
		result.FailureClass = "structured_output_failure"
		return errors.New("structured final output violates result/nonce schema")
	}
	result.ItemID = finals[0].ID
	result.FinalOutput = &output
	result.Outcome = Pass
	return nil
}

func appendProtocolEvent(writer io.Writer, seq uint64, message Message) error {
	event := protocolEvent{Seq: seq, Timestamp: time.Now().UTC(), Kind: message.Kind, Method: message.Method, ID: message.ID.Key(), Raw: message.Raw}
	return json.NewEncoder(writer).Encode(event)
}

func requestName(stage int) string {
	return [...]string{"", "initialize", "configRequirements/read", "thread/start-or-resume", "turn/start"}[stage]
}

func errorDetail(waitErr, protocolErr error) string {
	if waitErr != nil {
		return waitErr.Error()
	}
	if protocolErr != nil {
		return protocolErr.Error()
	}
	return "process did not exit successfully"
}

func resolveExecutable(name string) (string, error) {
	if name == "" {
		return "", errors.New("executable is required")
	}
	if filepath.IsAbs(name) {
		if info, err := os.Stat(name); err != nil || !info.Mode().IsRegular() {
			return "", errors.New("executable must be an existing regular file")
		}
		return filepath.Clean(name), nil
	}
	if filepath.Base(name) != name {
		return "", errors.New("executable must be an absolute path or a PATH name")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if filepath.Clean(filepath.Dir(path)) == filepath.Clean(cwd) {
		return "", errors.New("refusing executable resolved from the current directory")
	}
	return path, nil
}

func createArtifact(dir, name string) (*os.File, error) {
	return os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func writeJSONExclusive(path string, value any) error {
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
