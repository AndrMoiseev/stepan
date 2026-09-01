package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/specflow"
)

func TestPreflightRejectsUnsupportedPlatformBeforeHandles(t *testing.T) {
	err := preflight("linux", "amd64", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "require windows/amd64") {
		t.Fatalf("preflight error = %v", err)
	}
}

func TestComposePlanningFlowBuildsDurableApplication(t *testing.T) {
	root := t.TempDir()
	session := specflow.NewSession(func(context.Context) (agentruntime.Runtime, error) { return nil, context.Canceled })
	application, registry, err := composePlanningFlow(root, session, agentConfig{kind: agentCodex, executable: "codex"})
	if err != nil || application == nil || registry == nil {
		t.Fatalf("compose planning flow = %T, %T, %v", application, registry, err)
	}
}

func TestRunRejectsClaudeWithoutCLIPathBeforeTerminalPreflight(t *testing.T) {
	if code := run(context.Background(), []string{"--agent", "claude"}); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
}

func TestPreflightRejectsRedirectedInput(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "redirected")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = preflight("windows", "amd64", file, file)
	if err == nil || !strings.Contains(err.Error(), "stdin must be a Windows console") {
		t.Fatalf("preflight error = %v", err)
	}
}
