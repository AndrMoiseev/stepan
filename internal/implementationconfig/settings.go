// Package implementationconfig loads the implementation settings sections.
//
// It keeps the user and project inputs separate. Resolving inheritance and
// validating their contents belongs to the implementation-flow configuration
// layer, not this file-loading boundary.
package implementationconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Sources contains the implementation sections read from each settings file.
// A nil section means that the corresponding file has no implementation key.
// The raw JSON is intentionally preserved for later configuration processing.
type Sources struct {
	User    json.RawMessage
	Project json.RawMessage
}

// Load reads the implementation sections from the user and project settings
// files. A missing settings file is equivalent to an absent implementation
// section.
func Load(repositoryRoot string) (Sources, error) {
	return load(repositoryRoot, os.UserHomeDir, os.ReadFile)
}

func load(repositoryRoot string, home func() (string, error), read func(string) ([]byte, error)) (Sources, error) {
	directory, err := home()
	if err != nil || directory == "" {
		return Sources{}, settingsError("user", "cannot determine home directory")
	}

	user, err := readImplementation("user", filepath.Join(directory, ".stepan", "settings.json"), read)
	if err != nil {
		return Sources{}, err
	}
	project, err := readImplementation("project", filepath.Join(repositoryRoot, ".stepan", "settings.json"), read)
	if err != nil {
		return Sources{}, err
	}
	return Sources{User: user, Project: project}, nil
}

func readImplementation(level, path string, read func(string) ([]byte, error)) (json.RawMessage, error) {
	data, err := read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, settingsError(level, "cannot read settings file")
	}
	if !json.Valid(data) {
		return nil, settingsError(level, "invalid JSON")
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil || settings == nil {
		return nil, settingsError(level, "expected a settings object")
	}
	implementation, ok := settings["implementation"]
	if !ok {
		return nil, nil
	}
	return append(json.RawMessage(nil), implementation...), nil
}

func settingsError(level, reason string) error {
	return errors.New(level + " implementation settings: " + reason)
}
