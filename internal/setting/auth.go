package setting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const nessyTokenPath = "agentruntime.nessyapp.auth_token"

// NessyAuthToken rereads the home setting only when the Nessy provider is
// selected. The returned secret must never enter an effective configuration.
func NessyAuthToken() (string, error) {
	return loadNessyAuthToken(os.UserHomeDir, os.ReadFile)
}

func loadNessyAuthToken(home func() (string, error), read func(string) ([]byte, error)) (string, error) {
	directory, err := home()
	if err != nil || directory == "" {
		return "", fmt.Errorf("~/.stepan/settings.json: %s: cannot determine home directory", nessyTokenPath)
	}
	path := filepath.Join(directory, ".stepan", "settings.json")
	data, err := readDocument(path, read)
	if err != nil {
		return "", fmt.Errorf("%s: %s: cannot read settings file", path, nessyTokenPath)
	}
	return parseNessyAuthToken(path, data)
}

func parseNessyAuthToken(path string, data []byte) (string, error) {
	fail := func(reason string) (string, error) { return "", fmt.Errorf("%s: %s: %s", path, nessyTokenPath, reason) }
	if _, err := parseDocument(path, data, false); err != nil {
		return "", err
	}
	var root map[string]json.RawMessage
	if len(data) == 0 {
		return fail("token is missing")
	}
	_ = json.Unmarshal(data, &root)
	var agent map[string]json.RawMessage
	_ = json.Unmarshal(root["agentruntime"], &agent)
	var nessy map[string]json.RawMessage
	_ = json.Unmarshal(agent["nessyapp"], &nessy)
	raw, ok := nessy["auth_token"]
	if !ok || isNull(raw) {
		return fail("token is missing")
	}
	var token string
	if err := json.Unmarshal(raw, &token); err != nil {
		return fail("expected a string token")
	}
	if strings.TrimSpace(token) == "" {
		return fail("token is empty")
	}
	if strings.ContainsRune(token, 0) {
		return fail("token cannot be represented in the child environment")
	}
	return token, nil
}
