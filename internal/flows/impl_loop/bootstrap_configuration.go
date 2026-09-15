package impl_loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

// BootstrapConfigurationPaths names the two complete settings documents that
// a bootstrap proposal can change. The proposal itself contains only their
// implementation members; everything else in these documents is preserved.
type BootstrapConfigurationPaths struct {
	User    string
	Project string
}

// BootstrapConfigurationDiff is safe to display to an operator. It contains
// a deterministic representation of the implementation member only and
// redacts conventional credential-bearing keys at every level.
type BootstrapConfigurationDiff struct {
	Path string
	Diff string
}

// BootstrapConfigurationProposal is a validated, not-yet-persisted update.
// Its unexported full documents deliberately cannot be shown accidentally.
type BootstrapConfigurationProposal struct {
	Diffs   []BootstrapConfigurationDiff
	paths   BootstrapConfigurationPaths
	updated map[string][]byte
}

// BootstrapConfigurationPresenter presents every changed file and obtains
// one explicit confirmation. Returning false leaves both files untouched.
type BootstrapConfigurationPresenter interface {
	ConfirmBootstrapConfiguration(context.Context, []BootstrapConfigurationDiff) (bool, error)
}

func defaultBootstrapConfigurationPaths(repository string) (BootstrapConfigurationPaths, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return BootstrapConfigurationPaths{}, errors.New("cannot determine home directory for bootstrap settings")
	}
	return BootstrapConfigurationPaths{
		User:    filepath.Join(home, ".stepan", "settings.json"),
		Project: filepath.Join(repository, ".stepan", "settings.json"),
	}, nil
}

// PrepareBootstrapConfigurationProposal validates both proposed
// implementation objects before constructing either output document. A
// malformed proposal cannot partially update user or project settings.
func PrepareBootstrapConfigurationProposal(paths BootstrapConfigurationPaths, response AgentResponse) (BootstrapConfigurationProposal, error) {
	if response.Kind != ResponseConfigurationProposed || response.UserImplementation == nil || response.ProjectImplementation == nil {
		return BootstrapConfigurationProposal{}, errors.New("bootstrap response does not contain a configuration proposal")
	}
	if paths.User == "" || paths.Project == "" {
		return BootstrapConfigurationProposal{}, errors.New("bootstrap configuration paths are required")
	}
	user, err := bootstrapImplementationObject("user", *response.UserImplementation)
	if err != nil {
		return BootstrapConfigurationProposal{}, err
	}
	project, err := bootstrapImplementationObject("project", *response.ProjectImplementation)
	if err != nil {
		return BootstrapConfigurationProposal{}, err
	}
	if err := validateBootstrapImplementation(user, project); err != nil {
		return BootstrapConfigurationProposal{}, err
	}

	proposal := BootstrapConfigurationProposal{paths: paths, updated: make(map[string][]byte)}
	for _, input := range []struct {
		path           string
		implementation json.RawMessage
	}{
		{paths.User, user},
		{paths.Project, project},
	} {
		before, err := readBootstrapSettings(input.path)
		if err != nil {
			return BootstrapConfigurationProposal{}, err
		}
		after := copyBootstrapSettings(before)
		after["implementation"] = append(json.RawMessage(nil), input.implementation...)
		encoded, err := canonicalBootstrapSettings(after)
		if err != nil {
			return BootstrapConfigurationProposal{}, errors.New("cannot encode bootstrap settings")
		}
		proposal.updated[input.path] = encoded
		diff, changed, err := bootstrapSettingsDiff(before, after)
		if err != nil {
			return BootstrapConfigurationProposal{}, errors.New("cannot prepare safe bootstrap configuration diff")
		}
		if changed {
			proposal.Diffs = append(proposal.Diffs, BootstrapConfigurationDiff{Path: input.path, Diff: diff})
		}
	}
	return proposal, nil
}

// SaveBootstrapConfigurationProposal persists a previously validated proposal
// after the UI has confirmed it. Each complete document is replaced through a
// same-directory temporary file, so authorization and unrelated settings stay
// intact and a crash cannot leave a half-written JSON file.
func SaveBootstrapConfigurationProposal(proposal BootstrapConfigurationProposal) error {
	for _, diff := range proposal.Diffs {
		data, ok := proposal.updated[diff.Path]
		if !ok {
			return errors.New("bootstrap configuration proposal is incomplete")
		}
		if err := writeBootstrapSettings(diff.Path, data); err != nil {
			return err
		}
	}
	return nil
}

// ConfirmAndSaveBootstrapConfiguration is the only mutating proposal path.
// It gives the presenter the exact safe diff before it writes either file.
func ConfirmAndSaveBootstrapConfiguration(ctx context.Context, proposal BootstrapConfigurationProposal, confirm func(context.Context, []BootstrapConfigurationDiff) (bool, error)) (bool, error) {
	if len(proposal.Diffs) == 0 {
		return false, nil
	}
	if confirm == nil {
		return false, errors.New("bootstrap configuration confirmation is required")
	}
	accepted, err := confirm(ctx, append([]BootstrapConfigurationDiff(nil), proposal.Diffs...))
	if err != nil || !accepted {
		return false, err
	}
	if err := SaveBootstrapConfigurationProposal(proposal); err != nil {
		return false, err
	}
	return true, nil
}

func bootstrapImplementationObject(level, value string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("bootstrap %s implementation proposal must be a JSON object", level)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("bootstrap %s implementation proposal must contain one JSON value", level)
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("bootstrap %s implementation proposal cannot be encoded", level)
	}
	return encoded, nil
}

func validateBootstrapImplementation(user, project json.RawMessage) error {
	if _, err := bootstrapImplementationFields("user", user); err != nil {
		return err
	}
	projectFields, err := bootstrapImplementationFields("project", project)
	if err != nil {
		return err
	}
	configuration, err := implementationconfig.Merge(implementationconfig.Sources{User: user, Project: project})
	if err != nil {
		return fmt.Errorf("validate bootstrap configuration: %w", err)
	}
	if _, err := configuration.ResolveLimits(); err != nil {
		return fmt.Errorf("validate bootstrap configuration: %w", err)
	}
	for name, raw := range configuration.Profiles {
		if _, err := decodeBootstrapProfile(name, raw); err != nil {
			return err
		}
	}
	if _, checksPresent := projectFields["checks"]; checksPresent {
		if _, err := configuration.SelectHostChecks(); err != nil {
			return fmt.Errorf("validate bootstrap configuration: %w", err)
		}
	} else if _, requiredPresent := projectFields["required_checks"]; requiredPresent {
		if _, err := configuration.SelectHostChecks(); err != nil {
			return fmt.Errorf("validate bootstrap configuration: %w", err)
		}
	}
	for _, field := range []string{"rules_file", "main_branch"} {
		raw, present := projectFields[field]
		if !present {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
			return fmt.Errorf("validate bootstrap configuration: project implementation %s must be a nonempty string", field)
		}
	}
	return nil
}

func bootstrapImplementationFields(level string, raw json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("bootstrap %s implementation proposal must be a JSON object", level)
	}
	allowed := map[string]bool{"profiles": true, "roles": true, "limits": true, "checks": true, "required_checks": true, "rules_file": true, "main_branch": true}
	for name := range fields {
		if !allowed[name] {
			return nil, fmt.Errorf("bootstrap %s implementation proposal has unsupported field %q", level, name)
		}
	}
	return fields, nil
}

func decodeBootstrapProfile(name string, raw json.RawMessage) (implementationconfig.RuntimeProfile, error) {
	// ResolveRoleProfile intentionally only resolves profiles selected by a
	// role. Bootstrap validates every proposed profile so dormant malformed
	// profiles cannot be written for a later run to trip over.
	configuration := implementationconfig.Configuration{Profiles: map[string]json.RawMessage{name: raw}, Roles: map[string]string{"bootstrapper": name}}
	profile, err := configuration.ResolveRoleProfile(implementationconfig.RoleBootstrapper)
	if err != nil {
		return implementationconfig.RuntimeProfile{}, fmt.Errorf("validate bootstrap configuration: %w", err)
	}
	return profile, nil
}

func readBootstrapSettings(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, errors.New("cannot read bootstrap settings")
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil || settings == nil {
		return nil, errors.New("bootstrap settings must be a JSON object")
	}
	return settings, nil
}

func copyBootstrapSettings(settings map[string]json.RawMessage) map[string]json.RawMessage {
	copy := make(map[string]json.RawMessage, len(settings)+1)
	for key, value := range settings {
		copy[key] = append(json.RawMessage(nil), value...)
	}
	return copy
}

func canonicalBootstrapSettings(settings map[string]json.RawMessage) ([]byte, error) {
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func bootstrapSettingsDiff(before, after map[string]json.RawMessage) (string, bool, error) {
	oldImplementation := before["implementation"]
	newImplementation := after["implementation"]
	oldSafe, err := safeBootstrapJSON(oldImplementation)
	if err != nil {
		return "", false, err
	}
	newSafe, err := safeBootstrapJSON(newImplementation)
	if err != nil {
		return "", false, err
	}
	if bytes.Equal(oldSafe, newSafe) {
		return "", false, nil
	}
	return "--- implementation\n+++ implementation\n- " + string(oldSafe) + "\n+ " + string(newSafe) + "\n", true, nil
}

var bootstrapProposalSensitiveName = regexp.MustCompile(`(?i)(auth(?:orization)?|token|secret|password|api[_-]?key|credential)`)

func safeBootstrapJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte("{}"), nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	value = redactBootstrapProposalSecrets(value)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func redactBootstrapProposalSecrets(value any) any {
	switch current := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(current))
		for key, item := range current {
			if bootstrapProposalSensitiveName.MatchString(key) {
				redacted[key] = "[redacted]"
				continue
			}
			redacted[key] = redactBootstrapProposalSecrets(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(current))
		for index, item := range current {
			redacted[index] = redactBootstrapProposalSecrets(item)
		}
		return redacted
	default:
		return current
	}
}

func writeBootstrapSettings(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("cannot create bootstrap settings directory")
	}
	permissions := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		permissions = info.Mode().Perm()
	}
	temporary, err := os.CreateTemp(directory, ".settings-*.tmp")
	if err != nil {
		return errors.New("cannot prepare bootstrap settings write")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(permissions); err != nil {
		_ = temporary.Close()
		return errors.New("cannot prepare bootstrap settings permissions")
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("cannot write bootstrap settings")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("cannot finalize bootstrap settings")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("cannot replace bootstrap settings")
	}
	return nil
}
