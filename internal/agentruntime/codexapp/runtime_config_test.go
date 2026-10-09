//go:build process_integration

package codexapp

import "testing"

func TestRuntimeArgsCarryConfiguredModelAndReasoning(t *testing.T) {
	args := runtimeArgs(RuntimeConfig{Model: "gpt-test", Reasoning: "high"})
	want := []string{"-c", `model="gpt-test"`, "-c", `model_reasoning_effort="high"`}
	if len(args) < len(want) {
		t.Fatalf("runtime args = %#v", args)
	}
	for index := range want {
		if args[len(args)-len(want)+index] != want[index] {
			t.Fatalf("runtime args = %#v, want suffix %#v", args, want)
		}
	}
}

func TestRuntimeConfigRejectsUnsupportedReasoningBeforeExecutableLookup(t *testing.T) {
	err := ValidateRuntimeConfig(RuntimeConfig{Executable: "missing-codex", Workspace: t.TempDir(), Reasoning: "unsupported"})
	if err == nil || err.Error() != `unsupported Codex reasoning "unsupported"` {
		t.Fatalf("validation error = %v", err)
	}
}
