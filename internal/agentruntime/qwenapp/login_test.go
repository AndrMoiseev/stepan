package qwenapp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestRunInteractiveLoginOwnsTerminalBeforeACP(t *testing.T) {
	workspace := makeGitRoot(t)
	metadataPath := filepath.Join(t.TempDir(), "login.json")
	t.Setenv("GO_WANT_QWENAPP_FAKE", "interactive-login")
	t.Setenv("STEPAN_QWENAPP_METADATA", metadataPath)
	input := bytes.NewBufferString("complete-login\n")
	output := &bytes.Buffer{}
	errorOutput := &bytes.Buffer{}

	if err := RunInteractiveLogin(context.Background(), testExecutableName(t), workspace, input, output, errorOutput); err != nil {
		t.Fatal(err)
	}
	metadata := readFakeMetadata(t, metadataPath)
	if len(metadata.Args) != 0 {
		t.Fatalf("interactive login args = %#v, want none", metadata.Args)
	}
	if metadata.WorkingDir != canonicalForTest(t, workspace) {
		t.Fatalf("interactive login cwd = %q", metadata.WorkingDir)
	}
	if output.String() != "login stdout\n" || errorOutput.String() != "login stderr\n" {
		t.Fatalf("interactive terminal output = %q / %q", output, errorOutput)
	}
}

func TestRunInteractiveLoginStopsBeforeACPOnFailure(t *testing.T) {
	t.Setenv("GO_WANT_QWENAPP_FAKE", "interactive-login-failure")
	err := RunInteractiveLogin(context.Background(), testExecutableName(t), makeGitRoot(t), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !errors.Is(err, agentruntime.ErrRuntimeStartup) || !strings.Contains(err.Error(), "exit code 29") {
		t.Fatalf("interactive login failure = %v", err)
	}
}

func TestRunInteractiveLoginHonorsCancellation(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	workspace := makeGitRoot(t)
	executable := testExecutableName(t)
	t.Setenv("GO_WANT_QWENAPP_FAKE", "interactive-login-wait")
	t.Setenv("STEPAN_QWENAPP_READY", ready)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunInteractiveLogin(ctx, executable, workspace, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	}()
	waitForFile(t, ready)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled interactive login = %v", err)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatal(err)
	}
}
