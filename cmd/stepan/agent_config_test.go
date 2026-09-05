package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentConfig(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want agentConfig
		err  string
	}{
		{name: "default", want: agentConfig{kind: agentCodex, executable: "codex"}},
		{name: "codex named", args: []string{"--agent-cli-name", "codex-compatible"}, want: agentConfig{kind: agentCodex, executable: "codex-compatible"}},
		{name: "claude PATH default", args: []string{"--agent", "claude"}, want: agentConfig{kind: agentClaude, executable: "claude"}},
		{name: "claude named", args: []string{"--agent", "claude", "--agent-cli-name", "claude-compatible"}, want: agentConfig{kind: agentClaude, executable: "claude-compatible"}},
		{name: "qwen PATH", args: []string{"--agent", "qwen"}, want: agentConfig{kind: agentQwen, executable: "qwen"}},
		{name: "qwen named compatible CLI", args: []string{"--agent", "qwen", "--agent-cli-name", "qwen-compatible"}, want: agentConfig{kind: agentQwen, executable: "qwen-compatible"}},
		{name: "unknown provider", args: []string{"--agent", "other"}, err: "expected codex, claude, or qwen"},
		{name: "absolute path", args: []string{"--agent-cli-name", filepath.Join(t.TempDir(), "cli")}, err: "simple PATH name"},
		{name: "forward separator", args: []string{"--agent", "qwen", "--agent-cli-name", "directory/cli"}, err: "directory separators"},
		{name: "back separator", args: []string{"--agent", "claude", "--agent-cli-name", `directory\cli`}, err: "directory separators"},
		{name: "old flag rejected", args: []string{"--agent-cli", "qwen"}, err: "flag provided but not defined"},
		{name: "model flag excluded", args: []string{"--agent", "qwen", "--model", "coder"}, err: "flag provided but not defined"},
		{name: "extra argument", args: []string{"feature"}, err: "positional"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseAgentConfig(test.args)
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("error = %v, want %q", err, test.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("config = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseAgentConfigRejectsExplicitEmptyOrWhitespaceNameForEveryProvider(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "qwen"} {
		for _, supplied := range []struct {
			name string
			args []string
		}{
			{name: "equals empty", args: []string{"--agent-cli-name="}},
			{name: "separate empty", args: []string{"--agent-cli-name", ""}},
			{name: "equals whitespace", args: []string{"--agent-cli-name= "}},
			{name: "separate whitespace", args: []string{"--agent-cli-name", " "}},
		} {
			t.Run(provider+"/"+supplied.name, func(t *testing.T) {
				args := append([]string{"--agent", provider}, supplied.args...)
				config, err := parseAgentConfig(args)
				if err == nil || !strings.Contains(err.Error(), "simple PATH name") {
					t.Fatalf("config = %#v, error = %v", config, err)
				}
				if config != (agentConfig{}) {
					t.Fatalf("invalid explicit value fell back to %#v", config)
				}
			})
		}
	}
}

func TestUsageDocumentsClosedProviderSetWithoutModelSelection(t *testing.T) {
	if !strings.Contains(usageText, "--agent codex|claude|qwen") || !strings.Contains(usageText, "--agent-cli-name <name>") {
		t.Fatalf("usage does not document the closed provider set: %s", usageText)
	}
	if strings.Contains(usageText, "--model") || strings.Contains(usageText, "--agent-cli ") || strings.Contains(usageText, "absolute") {
		t.Fatalf("usage exposes a removed selection contract: %s", usageText)
	}
}
