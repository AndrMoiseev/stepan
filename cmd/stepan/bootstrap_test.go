package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop"
)

func TestParseBootstrapCommandRoutesBeforeImplementationLoop(t *testing.T) {
	config, err := parseAgentConfig([]string{"bootstrap", "--agent", "claude"})
	if err != nil || !config.bootstrap || config.kind != agentClaude || config.executable != "claude" {
		t.Fatalf("bootstrap command config = %#v, %v", config, err)
	}
}

func TestBootstrapConsoleShowsEachSafeDiffAndRequiresExplicitYes(t *testing.T) {
	output := &bytes.Buffer{}
	console := newBootstrapConsole(strings.NewReader("yes\n"), output)
	confirmed, err := console.ConfirmBootstrapConfiguration(context.Background(), []impl_loop.BootstrapConfigurationDiff{
		{Path: "user-settings.json", Diff: "--- implementation\n+++ implementation\n- {}\n+ {\"profiles\":{}}\n"},
		{Path: "project-settings.json", Diff: "--- implementation\n+++ implementation\n- {}\n+ {\"checks\":{}}\n"},
	})
	if err != nil || !confirmed {
		t.Fatalf("confirmation = %t, %v", confirmed, err)
	}
	for _, want := range []string{"user-settings.json", "project-settings.json", "Save these configuration changes?"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("console output missing %q: %s", want, output.String())
		}
	}
}
