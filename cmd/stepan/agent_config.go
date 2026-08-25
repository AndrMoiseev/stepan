package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type agentKind string

const (
	agentCodex  agentKind = "codex"
	agentClaude agentKind = "claude"
)

type agentConfig struct {
	kind       agentKind
	executable string
}

func parseAgentConfig(args []string) (agentConfig, error) {
	flags := flag.NewFlagSet("stepan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	agent := flags.String("agent", string(agentCodex), "agent provider: codex or claude")
	cli := flags.String("agent-cli", "", "absolute path to the agent CLI executable")
	if err := flags.Parse(args); err != nil {
		return agentConfig{}, fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return agentConfig{}, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	config := agentConfig{kind: agentKind(*agent), executable: *cli}
	if config.kind != agentCodex && config.kind != agentClaude {
		return agentConfig{}, fmt.Errorf("unknown agent %q (expected codex or claude)", *agent)
	}
	if config.executable == "" {
		if config.kind == agentClaude {
			return agentConfig{}, errors.New("--agent-cli is required for --agent claude")
		}
		config.executable = "codex"
		return config, nil
	}
	return validateExplicitCLI(config)
}

func validateExplicitCLI(config agentConfig) (agentConfig, error) {
	path := filepath.Clean(config.executable)
	if !filepath.IsAbs(path) {
		return agentConfig{}, fmt.Errorf("--agent-cli %q must be an absolute path", config.executable)
	}
	info, err := os.Stat(path)
	if err != nil {
		return agentConfig{}, fmt.Errorf("stat --agent-cli %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return agentConfig{}, fmt.Errorf("--agent-cli %q must name a regular file", path)
	}
	config.executable = path
	return config, nil
}

const usageText = `Usage: stepan [--agent codex|claude] [--agent-cli <absolute-path>]

Without options Stepan uses codex from PATH. Claude requires an explicit CLI
path, which may point to a compatible corporate fork.
Windows: stepan --agent claude --agent-cli "C:\\Program Files\\Company\\claude-corp.exe"
macOS:   stepan --agent claude --agent-cli /Applications/Company/claude-corp`
