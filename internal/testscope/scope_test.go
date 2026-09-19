package testscope

import "testing"

func TestClassifyPullRequestChanges(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  Scope
	}{
		{name: "documentation only", paths: []string{"docs/guide.md"}, want: Scope{}},
		{name: "Git adapter", paths: []string{"internal/git/snapshot.go"}, want: Scope{Git: true}},
		{name: "Git wiring", paths: []string{"internal/flows/impl_loop/workspace_control.go"}, want: Scope{Git: true}},
		{name: "process adapter", paths: []string{"internal/agentruntime/codexapp/runtime.go"}, want: Scope{Process: true}},
		{name: "composition root", paths: []string{"cmd/stepan/main.go"}, want: Scope{Git: true, Process: true}},
		{name: "dependencies", paths: []string{"go.mod"}, want: Scope{Git: true, Process: true}},
		{name: "Git integration contract", paths: []string{"internal/example/feature_git_integration_test.go"}, want: Scope{Git: true}},
		{name: "process integration contract", paths: []string{"internal/example/feature_process_integration_test.go"}, want: Scope{Process: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Classify("pull_request", test.paths); got != test.want {
				t.Fatalf("scope = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestClassifyNonPullRequestConservativelyRunsIntegrations(t *testing.T) {
	for _, event := range []string{"push", "schedule", "workflow_dispatch", "unexpected"} {
		t.Run(event, func(t *testing.T) {
			if got := Classify(event, []string{"docs/guide.md"}); got != (Scope{Git: true, Process: true}) {
				t.Fatalf("scope = %#v", got)
			}
		})
	}
}
