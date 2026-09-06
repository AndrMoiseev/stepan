// Package claudeapp adapts github.com/severity1/claude-agent-sdk-go v0.6.22
// (https://github.com/severity1/claude-agent-sdk-go/tree/v0.6.22) to Stepan's
// provider-neutral runtime. SDK types do not leave this package.
package claudeapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	claudecode "github.com/severity1/claude-agent-sdk-go"
)

const (
	backgroundTasksEnv = "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"
	agentViewEnv       = "CLAUDE_CODE_DISABLE_AGENT_VIEW"
)

// Config is immutable input for one Claude SDK connection.
type Config struct {
	Executable     string
	Workspace      string
	EnvelopeSchema json.RawMessage
}

func validateConfig(config Config) (Config, map[string]any, error) {
	name := config.Executable
	if name == "" {
		name = "claude"
	}
	executableName, err := agentruntime.ParseExecutableName(name)
	if err != nil {
		return Config{}, nil, fmt.Errorf("Claude executable: %w", err)
	}
	executable, err := executableName.Resolve()
	if err != nil {
		return Config{}, nil, fmt.Errorf("resolve Claude executable %q: %w", name, err)
	}
	workspace, err := canonicalDirectory(config.Workspace)
	if err != nil {
		return Config{}, nil, fmt.Errorf("canonicalize Claude workspace: %w", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return Config{}, nil, fmt.Errorf("Claude workspace is not a Git root: %w", err)
	}
	var schema map[string]any
	decoder := json.NewDecoder(bytes.NewReader(config.EnvelopeSchema))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil || schema == nil {
		return Config{}, nil, errors.New("Claude envelope schema must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, nil, errors.New("Claude envelope schema has trailing JSON")
	}
	config.Executable = executable
	config.Workspace = workspace
	config.EnvelopeSchema = append(json.RawMessage(nil), config.EnvelopeSchema...)
	return config, schema, nil
}

func claudeOptions(config Config, schema map[string]any, canUse claudecode.CanUseToolCallback, stderr func(string)) []claudecode.Option {
	settingSources := make([]claudecode.SettingSource, 0)
	return []claudecode.Option{
		claudecode.WithCLIPath(config.Executable),
		claudecode.WithCwd(config.Workspace),
		claudecode.WithTools("Read", "Write", "Edit", "Glob", "Grep"),
		claudecode.WithPermissionMode(claudecode.PermissionModeDefault),
		claudecode.WithCanUseTool(canUse),
		claudecode.WithSettingSources(settingSources...),
		claudecode.WithSkillsDisabled(),
		claudecode.WithEnv(map[string]string{backgroundTasksEnv: "1", agentViewEnv: "1"}),
		claudecode.WithJSONSchema(schema),
		claudecode.WithStderrCallback(stderr),
	}
}
