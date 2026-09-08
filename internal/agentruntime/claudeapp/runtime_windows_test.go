//go:build windows

package claudeapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	claudecode "github.com/severity1/claude-agent-sdk-go"
	"golang.org/x/sys/windows"
)

func TestRuntimeAcceptsEquivalentShortWorkspacePath(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace directory requiring short alias")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: nowhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	shortWorkspace := shortPathForTest(t, workspace)
	config := Config{
		Executable:     installClaudeExecutable(t, "short-path-claude"),
		Workspace:      workspace,
		EnvelopeSchema: []byte(`{"type":"object"}`),
	}
	runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client {
		return &fakeClient{}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if _, err := runtime.StartThread(agentruntime.ThreadConfig{Workspace: shortWorkspace, OutputSchema: config.EnvelopeSchema}); err != nil {
		t.Fatalf("start thread with equivalent short workspace path: %v", err)
	}
}

func TestPermissionEvaluatorAcceptsShortRootAliases(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace directory requiring short alias")
	artifact := filepath.Join(t.TempDir(), "artifact directory requiring short alias")
	for _, directory := range []string{workspace, artifact} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	input := filepath.Join(workspace, "input.md")
	if err := os.WriteFile(input, []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}

	shortWorkspace := shortPathForTest(t, workspace)
	shortArtifact := shortPathForTest(t, artifact)
	policy := activePolicy{artifactRoot: shortArtifact, writableRoot: shortArtifact}
	if err := permitTool("Read", map[string]any{"file_path": filepath.Join(shortWorkspace, filepath.Base(input))}, policy, shortWorkspace); err != nil {
		t.Fatalf("read through short workspace alias: %v", err)
	}
	if err := permitTool("Write", map[string]any{"file_path": filepath.Join(shortArtifact, "draft.md")}, policy, shortWorkspace); err != nil {
		t.Fatalf("write through short artifact alias: %v", err)
	}
}

func shortPathForTest(t *testing.T, path string) string {
	t.Helper()
	longPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetShortPathName(longPath, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Skipf("Windows short path is unavailable: %v", err)
	}
	if length == 0 || length >= uint32(len(buffer)) {
		t.Fatalf("invalid Windows short path length %d", length)
	}
	shortPath := filepath.Clean(windows.UTF16ToString(buffer[:length]))
	if shortPath == filepath.Clean(path) {
		t.Skip("Windows 8.3 aliases are disabled on this volume")
	}
	return shortPath
}
