package implementationconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsImplementationAtBothLevelsWithoutAuthorization(t *testing.T) {
	home, repository := t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(home, ".stepan", "settings.json"), `{"nessy":{"auth_token":"user-secret"},"implementation":{"profiles":{"medium":{"provider":"nessy"}}}}`)
	writeSettings(t, filepath.Join(repository, ".stepan", "settings.json"), `{"nessy":{"auth_token":"project-secret"},"implementation":{"roles":{"executor":"medium"}}}`)

	settings, err := load(repository, func() (string, error) { return home, nil }, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, settings.User, `{"profiles":{"medium":{"provider":"nessy"}}}`)
	assertJSON(t, settings.Project, `{"roles":{"executor":"medium"}}`)
	if strings.Contains(string(settings.User), "secret") || strings.Contains(string(settings.Project), "secret") {
		t.Fatal("authorization field was returned as implementation settings")
	}
}

func TestLoadAllowsAbsentSettingsFiles(t *testing.T) {
	home, repository := t.TempDir(), t.TempDir()

	settings, err := load(repository, func() (string, error) { return home, nil }, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if settings.User != nil || settings.Project != nil {
		t.Fatalf("expected absent sections, got %#v", settings)
	}
}

func TestLoadRejectsMalformedJSONWithoutDisclosingAuthorization(t *testing.T) {
	for _, test := range []struct {
		name    string
		user    string
		project string
		want    string
	}{
		{name: "user", user: `{"nessy":{"auth_token":"user-secret"},"implementation":`, want: "user implementation settings: invalid JSON"},
		{name: "project", project: `{"nessy":{"auth_token":"project-secret"},"implementation":`, want: "project implementation settings: invalid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, repository := t.TempDir(), t.TempDir()
			if test.user != "" {
				writeSettings(t, filepath.Join(home, ".stepan", "settings.json"), test.user)
			}
			if test.project != "" {
				writeSettings(t, filepath.Join(repository, ".stepan", "settings.json"), test.project)
			}

			_, err := load(repository, func() (string, error) { return home, nil }, os.ReadFile)
			if err == nil || err.Error() != test.want {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "auth_token") {
				t.Fatalf("authorization disclosed in diagnostic: %v", err)
			}
		})
	}
}

func TestLoadRejectsNonObjectSettingsWithoutDisclosingAuthorization(t *testing.T) {
	home, repository := t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(home, ".stepan", "settings.json"), `[]`)

	_, err := load(repository, func() (string, error) { return home, nil }, os.ReadFile)
	if err == nil || err.Error() != "user implementation settings: expected a settings object" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSanitizesHomeAndReadFailures(t *testing.T) {
	t.Run("home", func(t *testing.T) {
		_, err := load(t.TempDir(), func() (string, error) { return "", errors.New("home-secret") }, os.ReadFile)
		if err == nil || err.Error() != "user implementation settings: cannot determine home directory" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe error: %v", err)
		}
	})

	t.Run("project read", func(t *testing.T) {
		home, repository := t.TempDir(), t.TempDir()
		userPath := filepath.Join(home, ".stepan", "settings.json")
		projectPath := filepath.Join(repository, ".stepan", "settings.json")
		_, err := load(repository, func() (string, error) { return home, nil }, func(path string) ([]byte, error) {
			switch path {
			case userPath:
				return nil, os.ErrNotExist
			case projectPath:
				return nil, errors.New("project-secret")
			default:
				t.Fatalf("unexpected path: %s", path)
				return nil, nil
			}
		})
		if err == nil || err.Error() != "project implementation settings: cannot read settings file" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe error: %v", err)
		}
	})
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
		t.Fatalf("decode actual JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON mismatch: got %s, want %s", gotJSON, wantJSON)
	}
}
