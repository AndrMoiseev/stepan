package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This guards the user-facing JSON verbatim: use the same merge and
// preparation validators that a run applies before it can start or resume.
func TestImplementationLoopDocumentationConfigurationExamplesValidate(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate documentation example test")
	}
	document, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "docs", "implementation-loop.md"))
	if err != nil {
		t.Fatalf("read implementation-loop documentation: %v", err)
	}

	user := documentationSettings(t, string(document), "implementation-config-user-example")
	project := documentationSettings(t, string(document), "implementation-config-project-example")
	sources, err := SourcesFromDocuments("documented user settings", user, "documented project settings", project)
	if err != nil {
		t.Fatalf("read documented settings: %v", err)
	}
	configuration, err := Merge(sources)
	if err != nil {
		t.Fatalf("merge documented configuration: %v", err)
	}
	if err := configuration.ValidateLoopRoles(); err != nil {
		t.Fatalf("validate documented loop roles: %v", err)
	}
	for _, role := range loopRoles {
		if _, err := configuration.ResolveRoleProfile(role); err != nil {
			t.Fatalf("resolve documented %s profile: %v", role, err)
		}
	}
	bootstrapProfile, configured, err := configuration.BootstrapProfile()
	if err != nil || !configured || bootstrapProfile != "high" {
		t.Fatalf("resolve documented bootstrapper profile = %q, %t, %v; want high, true, nil", bootstrapProfile, configured, err)
	}
	if _, err := configuration.ResolveRoleProfile(RoleBootstrapper); err != nil {
		t.Fatalf("resolve documented bootstrapper profile: %v", err)
	}
	if _, err := configuration.ResolveLimits(); err != nil {
		t.Fatalf("resolve documented limits: %v", err)
	}

	repository := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repository, ".stepan", "rules"), 0o755); err != nil {
		t.Fatalf("create documented rules directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".stepan", "rules", "index.md"), []byte("# Rules\n"), 0o600); err != nil {
		t.Fatalf("write documented rules index: %v", err)
	}
	if _, err := configuration.SelectHostChecksIn(repository); err != nil {
		t.Fatalf("select documented host checks: %v", err)
	}
	if _, err := configuration.ValidateRulesFile(repository); err != nil {
		t.Fatalf("validate documented rules file: %v", err)
	}
}

func documentationSettings(t *testing.T, document, name string) []byte {
	t.Helper()
	start := "<!-- " + name + ":start -->"
	end := "<!-- " + name + ":end -->"
	startAt := strings.Index(document, start)
	if startAt < 0 {
		t.Fatalf("find %s JSON example", name)
	}
	section := document[startAt+len(start):]
	endAt := strings.Index(section, end)
	if endAt < 0 {
		t.Fatalf("close %s JSON example", name)
	}
	encoded, found := strings.CutPrefix(section[:endAt], "\n```json\n")
	if !found {
		t.Fatalf("open %s JSON fence", name)
	}
	closeAt := strings.Index(encoded, "\n```")
	if closeAt < 0 {
		t.Fatalf("close %s JSON fence", name)
	}
	encoded = encoded[:closeAt]
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &settings); err != nil {
		t.Fatalf("decode %s JSON example: %v", name, err)
	}
	return []byte(encoded)
}
