package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSeparatesProfilesAndLoopWithoutToken(t *testing.T) {
	home, repository := t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(home, ".stepan", "settings.json"), `{"agentruntime":{"profiles":{"medium":{"provider":"codex","model":"m"}},"nessyapp":{"auth_token":"secret"}}}`)
	writeSettings(t, filepath.Join(repository, ".stepan", "settings.json"), `{"flows":{"impl_loop":{"roles":{"orchestrator":"medium"}}}}`)
	sources, err := load(repository, func() (string, error) { return home, nil }, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, sources.UserProfiles, `{"medium":{"provider":"codex","model":"m"}}`)
	assertJSON(t, sources.Project, `{"roles":{"orchestrator":"medium"}}`)
	if strings.Contains(string(sources.User), "secret") || strings.Contains(string(sources.UserProfiles), "secret") {
		t.Fatal("secret entered nonsecret sources")
	}
}

func TestLoadAllowsAbsentDocumentsAndFlow(t *testing.T) {
	home, repository := t.TempDir(), t.TempDir()
	sources, err := load(repository, func() (string, error) { return home, nil }, os.ReadFile)
	if err != nil || sources.User != nil || sources.Project != nil {
		t.Fatalf("absent documents: %#v, %v", sources, err)
	}
	writeSettings(t, filepath.Join(home, ".stepan", "settings.json"), `{"flows":{"spec":{}}}`)
	if _, err := load(repository, func() (string, error) { return home, nil }, os.ReadFile); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, filepath.Join(home, ".stepan", "settings.json"), `{"agentruntime":{"nessyapp":{"auth_token":5}},"flows":{}}`)
	if _, err := load(repository, func() (string, error) { return home, nil }, os.ReadFile); err != nil {
		t.Fatalf("unused invalid Nessy token blocked another flow: %v", err)
	}
}

func TestLoadRejectsLegacyAndProjectTokenWithPath(t *testing.T) {
	for _, test := range []struct{ data, want string }{
		{`{"implementation":{}}`, `implementation`},
		{`{"nessy":{}}`, `nessy`},
		{`{"agentruntime":{"nessyapp":{"auth_token":"secret"}}}`, `agentruntime.nessyapp.auth_token`},
	} {
		_, err := SourcesFromDocuments("user.json", nil, "project.json", []byte(test.data))
		if err == nil || !strings.Contains(err.Error(), "project.json") || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe or missing error: %v", err)
		}
	}
}

func TestLoadRejectsMalformedDocuments(t *testing.T) {
	for _, data := range []string{`{`, `[]`, `{"flows":null}`, `{"agentruntime":{"profiles":[]}}`} {
		if _, err := SourcesFromDocuments("user.json", []byte(data), "project.json", nil); err == nil || !strings.Contains(err.Error(), "user.json") {
			t.Fatalf("malformed %s: %v", data, err)
		}
	}
}

func writeSettings(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON mismatch: got %s, want %s", gotJSON, wantJSON)
	}
}
