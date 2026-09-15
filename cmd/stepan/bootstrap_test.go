package main

import "testing"

func TestParseBootstrapCommandRoutesBeforeImplementationLoop(t *testing.T) {
	config, err := parseAgentConfig([]string{"bootstrap", "--agent", "claude"})
	if err != nil || !config.bootstrap || config.kind != agentClaude || config.executable != "claude" {
		t.Fatalf("bootstrap command config = %#v, %v", config, err)
	}
}
