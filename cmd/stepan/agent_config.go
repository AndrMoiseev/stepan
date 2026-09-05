package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type agentKind string

const (
	agentCodex  agentKind = "codex"
	agentClaude agentKind = "claude"
	agentQwen   agentKind = "qwen"
)

type agentConfig struct {
	kind       agentKind
	executable string
}

func parseAgentConfig(args []string) (agentConfig, error) {
	flags := flag.NewFlagSet("stepan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	agent := flags.String("agent", string(agentCodex), "agent provider: codex, claude, or qwen")
	cli := flags.String("agent-cli-name", "", "simple agent CLI executable name resolved through PATH")
	if err := flags.Parse(args); err != nil {
		return agentConfig{}, fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return agentConfig{}, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	config := agentConfig{kind: agentKind(*agent), executable: *cli}
	if config.kind != agentCodex && config.kind != agentClaude && config.kind != agentQwen {
		return agentConfig{}, fmt.Errorf("unknown agent %q (expected codex, claude, or qwen)", *agent)
	}
	if config.executable == "" {
		switch config.kind {
		case agentClaude:
			config.executable = "claude"
		case agentQwen:
			config.executable = "qwen"
		case agentCodex:
			config.executable = "codex"
		}
		return config, nil
	}
	return validateCLIName(config)
}

func validateCLIName(config agentConfig) (agentConfig, error) {
	name := config.executable
	if strings.TrimSpace(name) != name || name == "." || name == ".." || filepath.IsAbs(name) || filepath.VolumeName(name) != "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return agentConfig{}, fmt.Errorf("--agent-cli-name %q must be a simple executable name without directory separators", name)
	}
	return config, nil
}

const usageText = `Usage: stepan [--agent codex|claude|qwen] [--agent-cli-name <name>]

Without --agent-cli-name, Stepan resolves the official provider name (codex,
claude, or qwen) through PATH. A supplied name may select a compatible fork in
PATH; paths and names containing directory separators are rejected.
Example: stepan --agent qwen --agent-cli-name qwen-compatible`
