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
		{name: "claude needs path", args: []string{"--agent", "claude"}, err: "required"},
		{name: "unknown provider", args: []string{"--agent", "other"}, err: "unknown agent"},
		{name: "relative path", args: []string{"--agent-cli", "cli"}, err: "absolute"},
		{name: "directory", args: []string{"--agent-cli", t.TempDir()}, err: "regular file"},
		{name: "extra argument", args: []string{"idea"}, err: "positional"},
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
