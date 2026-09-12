package implementationconfig

import (
	"encoding/json"
	"fmt"
	"runtime"
)

const defaultCheckTimeoutSeconds = 600

// CheckKind classifies a project check for reporting and orchestration.
type CheckKind string

const (
	CheckKindBuild          CheckKind = "build"
	CheckKindLint           CheckKind = "lint"
	CheckKindStaticAnalysis CheckKind = "static_analysis"
	CheckKindTests          CheckKind = "tests"
)

// Platform identifies the operating system and architecture of the machine
// running Stepan. It deliberately does not describe a cross-compilation target.
type Platform struct {
	OS           string
	Architecture string
}

// HostPlatform returns the platform of the machine running this process.
func HostPlatform() Platform {
	return Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH}
}

func (platform Platform) key() string {
	return platform.OS + "/" + platform.Architecture
}

// CheckCommand is a directly executed program and its process-local overrides.
// Command execution itself is intentionally outside this package.
type CheckCommand struct {
	Program string            `json:"program"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// CheckDefinition is the project declaration of a named check. A platform
// command replaces Command as a whole; its fields are never merged with it.
type CheckDefinition struct {
	Kind           CheckKind                `json:"kind"`
	Command        *CheckCommand            `json:"command"`
	Platforms      map[string]*CheckCommand `json:"platforms"`
	CWD            string                   `json:"cwd"`
	TimeoutSeconds int                      `json:"timeout_seconds"`
}

// SelectedCheck is a check prepared for one machine. An unavailable check has
// no applicable command and must not be executed.
type SelectedCheck struct {
	Name           string
	Kind           CheckKind
	Command        CheckCommand
	CWD            string
	TimeoutSeconds int
	Available      bool
}

// CheckSelection is the prepared set of checks for a machine. Required keeps
// the project order; Checks also contains additional project checks.
type CheckSelection struct {
	Required []string
	Checks   map[string]SelectedCheck
}

// SelectChecks parses the project-only check configuration and selects commands
// for platform. Every required name must resolve to an available command before
// a caller can start any check. Additional checks without a suitable command are
// returned as unavailable so callers cannot accidentally run a fallback command.
func (configuration Configuration) SelectChecks(platform Platform) (CheckSelection, error) {
	definitions, err := parseChecks(configuration.Checks)
	if err != nil {
		return CheckSelection{}, err
	}
	required, err := parseRequiredChecks(configuration.RequiredChecks)
	if err != nil {
		return CheckSelection{}, err
	}

	selection := CheckSelection{
		Required: append([]string(nil), required...),
		Checks:   make(map[string]SelectedCheck, len(definitions)),
	}
	for name, definition := range definitions {
		selection.Checks[name] = selectCheck(name, definition, platform)
	}
	for _, name := range required {
		check, ok := selection.Checks[name]
		if !ok {
			return CheckSelection{}, configurationError("project", fmt.Sprintf("required_checks references unknown check %q", name))
		}
		if !check.Available {
			return CheckSelection{}, configurationError("project", fmt.Sprintf("required check %q has no command for platform %q", name, platform.key()))
		}
	}
	return selection, nil
}

// SelectHostChecks prepares checks for the OS and architecture that are running
// Stepan. Cross-build GOOS and GOARCH values belong in CheckCommand.Env and do
// not influence this selection.
func (configuration Configuration) SelectHostChecks() (CheckSelection, error) {
	return configuration.SelectChecks(HostPlatform())
}

func parseChecks(raw json.RawMessage) (map[string]CheckDefinition, error) {
	var definitions map[string]CheckDefinition
	if err := json.Unmarshal(raw, &definitions); err != nil || definitions == nil {
		return nil, configurationError("project", "checks must be an object")
	}
	for name, definition := range definitions {
		if name == "" {
			return nil, configurationError("project", "checks must use non-empty names")
		}
		if !validCheckKind(definition.Kind) {
			return nil, configurationError("project", fmt.Sprintf("checks.%s has unsupported kind %q", name, definition.Kind))
		}
		if definition.TimeoutSeconds < 0 {
			return nil, configurationError("project", fmt.Sprintf("checks.%s timeout_seconds must not be negative", name))
		}
		if definition.TimeoutSeconds == 0 {
			definition.TimeoutSeconds = defaultCheckTimeoutSeconds
		}
		definitions[name] = definition
	}
	return definitions, nil
}

func parseRequiredChecks(raw json.RawMessage) ([]string, error) {
	var required []string
	if err := json.Unmarshal(raw, &required); err != nil || len(required) == 0 {
		return nil, configurationError("project", "required_checks must be a non-empty array of check names")
	}
	for _, name := range required {
		if name == "" {
			return nil, configurationError("project", "required_checks must contain non-empty check names")
		}
	}
	return required, nil
}

func validCheckKind(kind CheckKind) bool {
	switch kind {
	case CheckKindBuild, CheckKindLint, CheckKindStaticAnalysis, CheckKindTests:
		return true
	default:
		return false
	}
}

func selectCheck(name string, definition CheckDefinition, platform Platform) SelectedCheck {
	command := definition.Command
	if platformCommand, ok := definition.Platforms[platform.key()]; ok {
		command = platformCommand
	}
	selected := SelectedCheck{
		Name:           name,
		Kind:           definition.Kind,
		CWD:            definition.CWD,
		TimeoutSeconds: definition.TimeoutSeconds,
	}
	if command == nil || command.Program == "" {
		return selected
	}
	selected.Available = true
	selected.Command = copyCommand(*command)
	return selected
}

func copyCommand(command CheckCommand) CheckCommand {
	command.Args = append([]string(nil), command.Args...)
	if command.Env != nil {
		env := make(map[string]string, len(command.Env))
		for key, value := range command.Env {
			env[key] = value
		}
		command.Env = env
	}
	return command
}
