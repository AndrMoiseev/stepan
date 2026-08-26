package codexapp

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestProcessPreflightPipesAndDiagnostic(t *testing.T) {
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "process-echo")
	workspace := t.TempDir()
	process := NewProcess(os.Args[0], workspace)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Stdin().Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(process.Stdout()).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("stdout = %q, %v", line, err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	if diagnostic := process.Diagnostic(); len(diagnostic) != maxDiagnosticBytes || strings.Trim(diagnostic, "e") != "" {
		t.Fatalf("diagnostic length = %d", len(diagnostic))
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal("second close:", err)
	}
	if _, err := os.Stat(workspace + `\.stepan`); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime artifact exists: %v", err)
	}
}

func TestProcessRejectsVersionBeforeStartingServer(t *testing.T) {
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "version-mismatch")
	workspace := t.TempDir()
	process := NewProcess(os.Args[0], workspace)
	err := process.Start()
	if err == nil || !strings.Contains(err.Error(), `require 0.147.0`) {
		t.Fatalf("Start error = %v", err)
	}
	if process.Stdin() != nil || process.Stdout() != nil || process.ExitCode() != nil {
		t.Fatal("App Server was created before version gate")
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessCannotStartAfterClose(t *testing.T) {
	process := NewProcess(os.Args[0], t.TempDir())
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Start(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Start error = %v", err)
	}
}

func TestProcessReportsEarlyExit(t *testing.T) {
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "early-exit")
	process := NewProcess(os.Args[0], t.TempDir())
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("Wait succeeded after nonzero exit")
	}
	if code := process.ExitCode(); code == nil || *code != 17 {
		t.Fatalf("exit code = %v", code)
	}
	if diagnostic := process.Diagnostic(); diagnostic != "early exit\r\n" && diagnostic != "early exit\n" {
		t.Fatalf("diagnostic = %q", diagnostic)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
}
