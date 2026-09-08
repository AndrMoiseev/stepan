// Package usersettings reads provider credentials from the user's Stepan settings.
package usersettings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// NessyAuthToken reads a snapshot of the configured token, never the environment.
func NessyAuthToken() (string, error) { return loadNessyAuthToken(os.UserHomeDir, os.ReadFile) }

func settingsError(reason string) error {
	return errors.New("~/.stepan/settings.json: nessy.auth_token: " + reason)
}

func loadNessyAuthToken(home func() (string, error), read func(string) ([]byte, error)) (string, error) {
	directory, err := home()
	if err != nil || directory == "" {
		return "", settingsError("cannot determine home directory")
	}
	data, err := read(filepath.Join(directory, ".stepan", "settings.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", settingsError("settings file is missing")
		}
		return "", settingsError("cannot read settings file")
	}
	return parseNessyAuthToken(data)
}

func parseNessyAuthToken(data []byte) (string, error) {
	if !json.Valid(data) {
		return "", settingsError("invalid JSON")
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(data, &settings) != nil || settings == nil {
		return "", settingsError("expected a settings object")
	}
	var nessy map[string]json.RawMessage
	if json.Unmarshal(settings["nessy"], &nessy) != nil || nessy == nil {
		return "", settingsError("expected nessy object")
	}
	var configured *string
	if json.Unmarshal(nessy["auth_token"], &configured) != nil {
		return "", settingsError("expected a string token")
	}
	if configured == nil {
		return "", settingsError("token is missing")
	}
	token := *configured
	if strings.TrimSpace(token) == "" {
		return "", settingsError("token is empty")
	}
	if strings.ContainsRune(token, 0) {
		return "", settingsError("token cannot be represented in the child environment")
	}
	return token, nil
}
