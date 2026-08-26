package codexexec

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestValidateBeforeArtifacts(t *testing.T) {
	root := t.TempDir()
	schema := filepath.Join(root, "schema.json")
	if err := os.WriteFile(schema, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(root, "artifacts")
	_, err := (Config{
		Executable:  filepath.Join(root, "missing.exe"),
		Workspace:   root,
		Prompt:      []byte("test"),
		Sandbox:     ReadOnly,
		SchemaPath:  schema,
		Timeout:     time.Second,
		ArtifactDir: artifactDir,
		Nonce:       "nonce",
		CaseID:      "test",
		ConfigMode:  Isolated,
	}).Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := os.Stat(artifactDir); !os.IsNotExist(err) {
		t.Fatalf("artifact directory was created before validation: %v", err)
	}
}

func TestArgsAreSeparateForNewAndResume(t *testing.T) {
	cfg := Config{
		Workspace:   `C:\work space`,
		Sandbox:     WorkspaceWrite,
		SchemaPath:  `C:\schema dir\schema.json`,
		ArtifactDir: `C:\artifact dir`,
		ConfigMode:  Isolated,
	}
	wantNew := []string{
		"exec", "--json", "--color", "never", "-c", `default_permissions=":workspace"`,
		"-c", `approval_policy="never"`, "--output-schema", `C:\schema dir\schema.json`,
		"--output-last-message", filepath.Join(cfg.ArtifactDir, "last-message.json"), "--cd", `C:\work space`,
		"--ignore-user-config", "--ignore-rules", "-",
	}
	if got := cfg.Args(); !reflect.DeepEqual(got, wantNew) {
		t.Fatalf("new args:\n got %#v\nwant %#v", got, wantNew)
	}

	cfg.SessionID = "opaque-session"
	wantResume := []string{
		"exec", "resume", "--json", "--output-schema", `C:\schema dir\schema.json`,
		"--output-last-message", filepath.Join(cfg.ArtifactDir, "last-message.json"), "-c", `default_permissions=":workspace"`,
		"-c", `approval_policy="never"`,
		"--ignore-user-config", "--ignore-rules", "opaque-session", "-",
	}
	if got := cfg.Args(); !reflect.DeepEqual(got, wantResume) {
		t.Fatalf("resume args:\n got %#v\nwant %#v", got, wantResume)
	}
}

func TestSandboxMapsToPermissionProfile(t *testing.T) {
	tests := map[Sandbox]string{
		ReadOnly:       ":read-only",
		WorkspaceWrite: ":workspace",
	}
	for sandbox, want := range tests {
		if got := sandbox.permissionProfile(); got != want {
			t.Errorf("%s: got %q, want %q", sandbox, got, want)
		}
	}
}
