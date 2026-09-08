package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

type agentKind string

const (
	agentCodex  agentKind = "codex"
	agentClaude agentKind = "claude"
	agentNessy  agentKind = "nessy"
)

type agentConfig struct {
	kind       agentKind
	executable string
}

func parseAgentConfig(args []string) (agentConfig, error) {
	flags := flag.NewFlagSet("stepan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	agent := flags.String("agent", string(agentCodex), "agent provider: codex, claude, or nessy")
	cli := flags.String("agent-cli-name", "", "simple agent CLI executable name resolved through PATH")
	if err := flags.Parse(args); err != nil {
		return agentConfig{}, fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return agentConfig{}, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	cliSupplied := false
	flags.Visit(func(item *flag.Flag) {
		if item.Name == "agent-cli-name" {
			cliSupplied = true
		}
	})
	config := agentConfig{kind: agentKind(*agent), executable: *cli}
	if *agent == "qwen" {
		return agentConfig{}, fmt.Errorf("agent qwen was removed; use --agent nessy")
	}
	if config.kind == agentNessy && cliSupplied {
		return agentConfig{}, fmt.Errorf("--agent-cli-name is not supported for Nessy; use nessy from PATH")
	}
	if config.kind != agentCodex && config.kind != agentClaude && config.kind != agentNessy {
		return agentConfig{}, fmt.Errorf("unknown agent %q (expected codex, claude, or nessy)", *agent)
	}
	if !cliSupplied {
		switch config.kind {
		case agentClaude:
			config.executable = "claude"
		case agentNessy:
			config.executable = "nessy"
		case agentCodex:
			config.executable = "codex"
		}
		return config, nil
	}
	name, err := agentruntime.ParseExecutableName(config.executable)
	if err != nil {
		return agentConfig{}, fmt.Errorf("--agent-cli-name %q: %w", config.executable, err)
	}
	config.executable = name.String()
	return config, nil
}

const usageText = `Usage: stepan [--agent codex|claude|nessy] [--agent-cli-name <name>]

Codex and Claude allow a simple agent CLI name resolved through PATH.
Nessy always uses nessy from PATH; --agent-cli-name is not supported for Nessy.
Configure nessy.auth_token in ~/.stepan/settings.json before starting Nessy.
The configured token replaces NESSY_CLI_DP_AUTH_TOKEN for child processes.
Example: stepan --agent nessy`
