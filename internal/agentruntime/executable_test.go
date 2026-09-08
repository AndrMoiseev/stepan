package agentruntime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExecutableNameValidationAndValueSemantics(t *testing.T) {
	name, err := ParseExecutableName("compatible-agent")
	if err != nil || name.String() != "compatible-agent" {
		t.Fatalf("valid executable name = %q, %v", name, err)
	}
	for _, value := range []string{"", " ", " agent", "agent ", ".", "..", filepath.Join(t.TempDir(), "agent"), "dir/agent", `dir\agent`} {
		if _, err := ParseExecutableName(value); err == nil || !strings.Contains(err.Error(), "simple PATH name") {
			t.Errorf("invalid executable name %q error = %v", value, err)
		}
	}
	if _, err := (ExecutableName("dir/agent")).Resolve(); err == nil {
		t.Fatal("unvalidated ExecutableName resolved")
	}
}

func TestExecutableNameResolvesOnlyThroughPATH(t *testing.T) {
	directory := t.TempDir()
	filename := "compatible-agent"
	if runtime.GOOS == "windows" {
		filename += ".exe"
	}
	path := filepath.Join(directory, filename)
	if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)

	name, err := ParseExecutableName(filename)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := name.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != filepath.Clean(want) {
		t.Fatalf("resolved executable = %q, want %q", resolved, want)
	}

	missing, err := ParseExecutableName("missing-compatible-agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Resolve(); err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("missing executable error = %v", err)
	}
}
