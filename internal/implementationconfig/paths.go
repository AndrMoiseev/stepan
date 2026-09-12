package implementationconfig

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// SelectChecksIn resolves the working directory of every selected check from
// repositoryRoot. A missing cwd uses repositoryRoot. Command fields are
// selected and copied exactly as SelectChecks does.
func (configuration Configuration) SelectChecksIn(repositoryRoot string, platform Platform) (CheckSelection, error) {
	root, err := repositoryRootPath(repositoryRoot)
	if err != nil {
		return CheckSelection{}, err
	}

	selection, err := configuration.SelectChecks(platform)
	if err != nil {
		return CheckSelection{}, err
	}
	for name, check := range selection.Checks {
		check.CWD, err = resolvePath(root, check.CWD)
		if err != nil {
			return CheckSelection{}, configurationError("project", fmt.Sprintf("checks.%s.cwd: %v", name, err))
		}
		selection.Checks[name] = check
	}
	return selection, nil
}

// SelectHostChecksIn resolves checks for the platform running Stepan.
func (configuration Configuration) SelectHostChecksIn(repositoryRoot string) (CheckSelection, error) {
	return configuration.SelectChecksIn(repositoryRoot, HostPlatform())
}

// ResolveRulesFile resolves the optional project rules_file from
// repositoryRoot. Validation that the resulting target is a permitted
// Markdown rules file belongs to the rules validation boundary.
func (configuration Configuration) ResolveRulesFile(repositoryRoot string) (string, error) {
	root, err := repositoryRootPath(repositoryRoot)
	if err != nil {
		return "", err
	}
	if len(configuration.RulesFile) == 0 {
		return "", nil
	}
	if isNull(configuration.RulesFile) {
		return "", configurationError("project", "rules_file must be a string")
	}

	var rulesFile string
	if err := json.Unmarshal(configuration.RulesFile, &rulesFile); err != nil {
		return "", configurationError("project", "rules_file must be a string")
	}
	resolved, err := resolvePath(root, rulesFile)
	if err != nil {
		return "", configurationError("project", "rules_file: "+err.Error())
	}
	return resolved, nil
}

func repositoryRootPath(repositoryRoot string) (string, error) {
	if repositoryRoot == "" || !filepath.IsAbs(repositoryRoot) {
		return "", fmt.Errorf("repository root must be an absolute path")
	}
	return filepath.Clean(repositoryRoot), nil
}

// resolvePath joins an ordinary relative path to root without looking at the
// process working directory. It deliberately performs no shell, home, or
// environment expansion.
func resolvePath(root, value string) (string, error) {
	if value == "" {
		return root, nil
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	if filepath.VolumeName(value) != "" || strings.HasPrefix(value, string(filepath.Separator)) || (filepath.Separator == '\\' && strings.HasPrefix(value, "/")) {
		return "", fmt.Errorf("path %q must be relative to the repository root or absolute", value)
	}
	return filepath.Clean(filepath.Join(root, value)), nil
}
