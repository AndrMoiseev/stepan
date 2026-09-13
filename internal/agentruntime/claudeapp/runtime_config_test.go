package claudeapp

import (
	"testing"

	claudecode "github.com/severity1/claude-agent-sdk-go"
)

func TestClaudeOptionsCarryConfiguredModelAndReasoning(t *testing.T) {
	options := claudecode.NewOptions(claudeOptions(Config{Model: "sonnet", Reasoning: string(claudecode.EffortXHigh)}, map[string]any{}, nil, func(string) {})...)
	if options.Model == nil || *options.Model != "sonnet" || options.Effort == nil || *options.Effort != string(claudecode.EffortXHigh) {
		t.Fatalf("Claude options = %#v", options)
	}
}

func TestClaudeConfigRejectsUnsupportedReasoningBeforeExecutableLookup(t *testing.T) {
	err := ValidateRuntimeConfig(Config{Executable: "missing-claude", Workspace: t.TempDir(), Reasoning: "unknown"})
	if err == nil || err.Error() != `unsupported Claude reasoning "unknown"` {
		t.Fatalf("validation error = %v", err)
	}
}
