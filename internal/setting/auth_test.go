package setting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNessyAuthTokenReadsOnlyHomeDocument(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, filepath.Join(home, ".stepan", "settings.json"), `{"agentruntime":{"nessyapp":{"auth_token":"home-secret"}}}`)
	t.Setenv("NESSY_CLI_DP_AUTH_TOKEN", "inherited-secret")
	token, err := loadNessyAuthToken(func() (string, error) { return home, nil }, os.ReadFile)
	if err != nil || token != "home-secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestNessyAuthTokenRejectsMissingAndInvalidWithoutDisclosure(t *testing.T) {
	for _, data := range []string{
		`{}`, `{"agentruntime":{"nessyapp":{"auth_token":null}}}`,
		`{"agentruntime":{"nessyapp":{"auth_token":5}}}`,
		`{"agentruntime":{"nessyapp":{"auth_token":"  "}}}`,
		`{"agentruntime":{"nessyapp":{"auth_token":"secret\u0000"}}}`,
	} {
		home := t.TempDir()
		writeSettings(t, filepath.Join(home, ".stepan", "settings.json"), data)
		_, err := loadNessyAuthToken(func() (string, error) { return home, nil }, os.ReadFile)
		if err == nil || !strings.Contains(err.Error(), nessyTokenPath) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe or missing error: %v", err)
		}
	}
}
