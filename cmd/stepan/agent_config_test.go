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
		{name: "nessy PATH", args: []string{"--agent", "nessy"}, want: agentConfig{kind: agentNessy, executable: "nessy"}},
		{name: "nessy named compatible CLI", args: []string{"--agent", "nessy", "--agent-cli-name", "nessy-compatible"}, err: "not supported for Nessy"},
		{name: "redundant override", args: []string{"--agent", "nessy", "--agent-cli-name", "nessy"}, err: "not supported for Nessy"},
		{name: "empty override", args: []string{"--agent", "nessy", "--agent-cli-name="}, err: "not supported for Nessy"},
		{name: "old provider", args: []string{"--agent", "qwen"}, err: "use --agent nessy"},
		{name: "old fork", args: []string{"--agent", "qwen", "--agent-cli-name", "nessy"}, err: "use --agent nessy"},
		{name: "unknown provider", args: []string{"--agent", "other"}, err: "expected codex, claude, or nessy"},
		{name: "absolute path", args: []string{"--agent-cli-name", filepath.Join(t.TempDir(), "cli")}, err: "simple PATH name"},
		{name: "forward separator", args: []string{"--agent", "nessy", "--agent-cli-name", "directory/cli"}, err: "not supported for Nessy"},
		{name: "back separator", args: []string{"--agent", "claude", "--agent-cli-name", `directory\cli`}, err: "directory separators"},
		{name: "old flag rejected", args: []string{"--agent-cli", "nessy"}, err: "flag provided but not defined"},
		{name: "model flag excluded", args: []string{"--agent", "nessy", "--model", "coder"}, err: "flag provided but not defined"},
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
	for _, provider := range []string{"codex", "claude"} {
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
	if !strings.Contains(usageText, "--agent codex|claude|nessy") || !strings.Contains(usageText, "--agent-cli-name <name>") || !strings.Contains(usageText, "nessy.auth_token") {
		t.Fatalf("usage does not document the closed provider set: %s", usageText)
	}
	if strings.Contains(usageText, "--model") || strings.Contains(usageText, "--agent-cli ") || strings.Contains(usageText, "absolute") {
		t.Fatalf("usage exposes a removed selection contract: %s", usageText)
	}
}
