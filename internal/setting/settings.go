// Package setting owns Stepan's user and project settings schema and resolves
// effective values for consumers without exposing credentials.
package setting

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Sources are the nonsecret sections extracted from the two settings files.
// User and Project contain flows.impl_loop; the profile members are separate.
type Sources struct {
	User            json.RawMessage
	Project         json.RawMessage
	UserProfiles    json.RawMessage
	ProjectProfiles json.RawMessage
}

// Load reads both settings documents. A missing document or flow section is
// valid, so flows that do not use impl_loop can still start.
func Load(repositoryRoot string) (Sources, error) {
	return load(repositoryRoot, os.UserHomeDir, os.ReadFile)
}

func load(repositoryRoot string, home func() (string, error), read func(string) ([]byte, error)) (Sources, error) {
	directory, err := home()
	if err != nil || directory == "" {
		return Sources{}, errors.New("user settings: cannot determine home directory")
	}
	userPath := filepath.Join(directory, ".stepan", "settings.json")
	projectPath := filepath.Join(repositoryRoot, ".stepan", "settings.json")
	user, err := readDocument(userPath, read)
	if err != nil {
		return Sources{}, err
	}
	project, err := readDocument(projectPath, read)
	if err != nil {
		return Sources{}, err
	}
	return SourcesFromDocuments(userPath, user, projectPath, project)
}

func readDocument(path string, read func(string) ([]byte, error)) ([]byte, error) {
	data, err := read(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: cannot read settings file", path)
	}
	return data, nil
}

// SourcesFromDocuments validates the public schema and extracts nonsecret
// members. Bootstrap uses the same boundary before presenting a proposal.
func SourcesFromDocuments(userPath string, user []byte, projectPath string, project []byte) (Sources, error) {
	u, err := parseDocument(userPath, user, false)
	if err != nil {
		return Sources{}, err
	}
	p, err := parseDocument(projectPath, project, true)
	if err != nil {
		return Sources{}, err
	}
	return Sources{User: u.loop, Project: p.loop, UserProfiles: u.profiles, ProjectProfiles: p.profiles}, nil
}

type documentSections struct {
	profiles json.RawMessage
	loop     json.RawMessage
}

func parseDocument(path string, data []byte, project bool) (documentSections, error) {
	if data == nil {
		return documentSections{}, nil
	}
	var root map[string]json.RawMessage
	if !json.Valid(data) {
		return documentSections{}, fmt.Errorf("%s: invalid JSON", path)
	}
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return documentSections{}, fmt.Errorf("%s: expected a settings object", path)
	}
	for _, old := range []string{"implementation", "nessy"} {
		if _, ok := root[old]; ok {
			return documentSections{}, fmt.Errorf("%s: obsolete top-level section %q", path, old)
		}
	}
	agent, err := nestedObject(path, "agentruntime", root["agentruntime"])
	if err != nil {
		return documentSections{}, err
	}
	flows, err := nestedObject(path, "flows", root["flows"])
	if err != nil {
		return documentSections{}, err
	}
	if _, present := agent["profiles"]; present {
		var profiles map[string]json.RawMessage
		if err := json.Unmarshal(agent["profiles"], &profiles); err != nil || profiles == nil {
			return documentSections{}, fmt.Errorf("%s: agentruntime.profiles must be an object", path)
		}
	}
	if project {
		nessy, err := nestedObject(path, "agentruntime.nessyapp", agent["nessyapp"])
		if err != nil {
			return documentSections{}, err
		}
		if _, present := nessy["auth_token"]; present {
			return documentSections{}, fmt.Errorf("%s: agentruntime.nessyapp.auth_token is user-only", path)
		}
	}
	if _, err := nestedObject(path, "flows.impl_loop", flows["impl_loop"]); err != nil {
		return documentSections{}, err
	}
	return documentSections{profiles: copyRaw(agent["profiles"]), loop: copyRaw(flows["impl_loop"])}, nil
}

func nestedObject(path, name string, raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s: %s must be an object", path, name)
	}
	return object, nil
}
