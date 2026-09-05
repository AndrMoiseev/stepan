package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentConfig(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "corporate cli")
	if err := os.WriteFile(executable, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		want agentConfig
		err  string
	}{
		{name: "default", want: agentConfig{kind: agentCodex, executable: "codex"}},
		{name: "codex explicit", args: []string{"--agent-cli", executable}, want: agentConfig{kind: agentCodex, executable: executable}},
		{name: "claude explicit", args: []string{"--agent", "claude", "--agent-cli", executable}, want: agentConfig{kind: agentClaude, executable: executable}},
		{name: "qwen PATH", args: []string{"--agent", "qwen"}, want: agentConfig{kind: agentQwen, executable: "qwen"}},
		{name: "qwen explicit nonstandard basename", args: []string{"--agent", "qwen", "--agent-cli", executable}, want: agentConfig{kind: agentQwen, executable: executable}},
		{name: "claude needs path", args: []string{"--agent", "claude"}, err: "required"},
		{name: "unknown provider", args: []string{"--agent", "other"}, err: "expected codex, claude, or qwen"},
		{name: "relative path", args: []string{"--agent-cli", "cli"}, err: "absolute"},
		{name: "qwen relative path", args: []string{"--agent", "qwen", "--agent-cli", "cli"}, err: "absolute"},
		{name: "qwen missing path", args: []string{"--agent", "qwen", "--agent-cli", filepath.Join(t.TempDir(), "missing")}, err: "stat --agent-cli"},
		{name: "directory", args: []string{"--agent-cli", t.TempDir()}, err: "regular file"},
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

func TestUsageDocumentsClosedProviderSetWithoutModelSelection(t *testing.T) {
	if !strings.Contains(usageText, "--agent codex|claude|qwen") {
		t.Fatalf("usage does not document the closed provider set: %s", usageText)
	}
	if strings.Contains(usageText, "--model") {
		t.Fatalf("usage unexpectedly exposes model selection: %s", usageText)
	}
}
