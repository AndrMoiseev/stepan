package usersettings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNessyAuthToken(t *testing.T) {
	for _, input := range []string{``, `{`, `{} {}`, `[]`, `null`, `{}`, `{"nessy":null}`, `{"nessy":[]}`, `{"nessy":{}}`, `{"nessy":{"auth_token":null}}`, `{"nessy":{"auth_token":123}}`, `{"nessy":{"auth_token":""}}`, `{"nessy":{"auth_token":" \t\n"}}`, `{"nessy":{"auth_token":"sensitive\u0000value"}}`, `{"nessy":{"auth_token":{"sensitive":"value"}}}`} {
		token, err := parseNessyAuthToken([]byte(input))
		if token != "" || err == nil {
			t.Fatalf("invalid configuration accepted")
		}
		if !strings.Contains(err.Error(), "~/.stepan/settings.json") || !strings.Contains(err.Error(), "nessy.auth_token") || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("unsafe or incomplete error: %v", err)
		}
	}
	token, err := parseNessyAuthToken([]byte(`{"other":true,"nessy":{"auth_token":" opaque token ","future":42}}`))
	if err != nil || token != " opaque token " {
		t.Fatal("token was not preserved")
	}
}

func TestLoadFailuresAndHomePath(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".stepan", "settings.json")
	for _, failure := range []error{os.ErrNotExist, os.ErrPermission, errors.New("sensitive read failure")} {
		_, err := loadNessyAuthToken(func() (string, error) { return home, nil }, func(got string) ([]byte, error) {
			if got != path {
				t.Fatal("wrong path")
			}
			return nil, failure
		})
		if err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatal("unsafe read error")
		}
	}
	_, err := loadNessyAuthToken(func() (string, error) { return "", errors.New("sensitive") }, func(string) ([]byte, error) { t.Fatal("read after home failure"); return nil, nil })
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("unsafe home error")
	}
}

func TestHomeIsOnlySource(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("NESSY_CLI_DP_AUTH_TOKEN", "environment-token")
	t.Chdir(project)
	if err := os.Mkdir(filepath.Join(project, ".stepan"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".stepan", "settings.json"), []byte(`{"nessy":{"auth_token":"project-token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NessyAuthToken(); err == nil {
		t.Fatal("used fallback token")
	}
	if err := os.Mkdir(filepath.Join(home, ".stepan"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".stepan", "settings.json"), []byte(`{"nessy":{"auth_token":"home-token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	token, err := NessyAuthToken()
	if err != nil || token != "home-token" {
		t.Fatal("home token not selected")
	}
	t.Chdir(t.TempDir())
	token, err = NessyAuthToken()
	if err != nil || token != "home-token" {
		t.Fatal("token depends on cwd")
	}
}
