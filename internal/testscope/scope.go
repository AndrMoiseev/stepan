// Package testscope classifies repository changes into optional integration
// suites. The fast suite is unconditional and therefore is not represented in
// Scope.
package testscope

import (
	"path/filepath"
	"strings"
)

type Scope struct {
	Git     bool
	Process bool
}

func Classify(event string, changedPaths []string) Scope {
	if event != "pull_request" {
		return Scope{Git: true, Process: true}
	}
	var scope Scope
	for _, changedPath := range changedPaths {
		path := filepath.ToSlash(filepath.Clean(changedPath))
		if isGlobalIntegrationPath(path) {
			return Scope{Git: true, Process: true}
		}
		if isGitIntegrationPath(path) {
			scope.Git = true
		}
		if isProcessIntegrationPath(path) {
			scope.Process = true
		}
	}
	return scope
}

func isGlobalIntegrationPath(path string) bool {
	switch path {
	case "go.mod", "go.sum", ".github/workflows/ci.yml", "internal/architecture/architecture_test.go":
		return true
	}
	return strings.HasPrefix(path, "internal/testscope/") || strings.HasPrefix(path, "internal/setting/")
}

func isGitIntegrationPath(path string) bool {
	if strings.HasSuffix(path, "_git_integration_test.go") {
		return true
	}
	for _, prefix := range []string{
		"internal/git/",
		"internal/codexprobe/",
		"internal/flows/spec/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	if strings.HasPrefix(path, "cmd/stepan/") {
		return true
	}
	if !strings.HasPrefix(path, "internal/flows/impl_loop/") {
		return false
	}
	name := strings.TrimPrefix(path, "internal/flows/impl_loop/")
	for _, prefix := range []string{
		"agent_call_guard", "check_presentation", "check_workspace",
		"controlled_agent_call", "controller_lock", "git_",
		"implementer_transition", "initial_checks", "initial_tasks",
		"task_review", "workspace_",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func isProcessIntegrationPath(path string) bool {
	if strings.HasSuffix(path, "_process_integration_test.go") {
		return true
	}
	for _, prefix := range []string{
		"internal/agentruntime/codexapp/",
		"internal/agentruntime/claudeapp/",
		"internal/agentruntime/nessyapp/",
		"internal/checkexec/",
		"internal/flows/impl_loop/runtime/",
		"internal/processjob/",
		"internal/flows/impl_loop/store/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return strings.HasPrefix(path, "cmd/stepan/") || strings.HasPrefix(path, "internal/flows/impl_loop/controller_lock")
}
