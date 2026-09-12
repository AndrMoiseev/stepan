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
	var rawDefinitions map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawDefinitions); err != nil || rawDefinitions == nil {
		return nil, configurationError("project", "checks must be an object")
	}
	definitions := make(map[string]CheckDefinition, len(rawDefinitions))
	for name, rawDefinition := range rawDefinitions {
		if name == "" {
			return nil, configurationError("project", "checks must use non-empty names")
		}
		definition, err := parseCheckDefinition(name, rawDefinition)
		if err != nil {
			return nil, err
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

func parseCheckDefinition(name string, raw json.RawMessage) (CheckDefinition, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return CheckDefinition{}, configurationError("project", fmt.Sprintf("checks.%s must be an object", name))
	}

	var definition struct {
		Kind           CheckKind `json:"kind"`
		CWD            string    `json:"cwd"`
		TimeoutSeconds int       `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &definition); err != nil {
		return CheckDefinition{}, configurationError("project", fmt.Sprintf("checks.%s has invalid fields", name))
	}
	result := CheckDefinition{
		Kind:           definition.Kind,
		CWD:            definition.CWD,
		TimeoutSeconds: definition.TimeoutSeconds,
	}
	if commandRaw, ok := fields["command"]; ok {
		command, err := parseDeclaredCheckCommand("checks."+name+".command", commandRaw)
		if err != nil {
			return CheckDefinition{}, err
		}
		result.Command = &command
	}
	if platformsRaw, ok := fields["platforms"]; ok {
		var rawPlatforms map[string]json.RawMessage
		if err := json.Unmarshal(platformsRaw, &rawPlatforms); err != nil || rawPlatforms == nil {
			return CheckDefinition{}, configurationError("project", fmt.Sprintf("checks.%s.platforms must be an object", name))
		}
		result.Platforms = make(map[string]*CheckCommand, len(rawPlatforms))
		for platform, commandRaw := range rawPlatforms {
			command, err := parseDeclaredCheckCommand("checks."+name+".platforms."+platform, commandRaw)
			if err != nil {
				return CheckDefinition{}, err
			}
			result.Platforms[platform] = &command
		}
	}
	return result, nil
}

func parseDeclaredCheckCommand(field string, raw json.RawMessage) (CheckCommand, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return CheckCommand{}, configurationError("project", field+" must be an object")
	}

	programRaw, ok := fields["program"]
	var program string
	if !ok || isNull(programRaw) || json.Unmarshal(programRaw, &program) != nil || program == "" {
		return CheckCommand{}, configurationError("project", field+".program must be a non-empty string")
	}
	argsRaw, ok := fields["args"]
	if !ok || isNull(argsRaw) {
		return CheckCommand{}, configurationError("project", field+".args must be an array of strings")
	}
	var rawArgs []json.RawMessage
	if err := json.Unmarshal(argsRaw, &rawArgs); err != nil || rawArgs == nil {
		return CheckCommand{}, configurationError("project", field+".args must be an array of strings")
	}
	args := make([]string, len(rawArgs))
	for index, rawArg := range rawArgs {
		if isNull(rawArg) || json.Unmarshal(rawArg, &args[index]) != nil {
			return CheckCommand{}, configurationError("project", field+".args must be an array of strings")
		}
	}

	command := CheckCommand{Program: program, Args: args}
	if envRaw, ok := fields["env"]; ok {
		if isNull(envRaw) {
			return CheckCommand{}, configurationError("project", field+".env must be an object of strings")
		}
		var rawEnv map[string]json.RawMessage
		if err := json.Unmarshal(envRaw, &rawEnv); err != nil || rawEnv == nil {
			return CheckCommand{}, configurationError("project", field+".env must be an object of strings")
		}
		command.Env = make(map[string]string, len(rawEnv))
		for name, rawValue := range rawEnv {
			var value string
			if isNull(rawValue) || json.Unmarshal(rawValue, &value) != nil {
				return CheckCommand{}, configurationError("project", field+".env must be an object of strings")
			}
			command.Env[name] = value
		}
	}
	return command, nil
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
	if command.Args != nil {
		command.Args = append([]string{}, command.Args...)
	}
	if command.Env != nil {
		env := make(map[string]string, len(command.Env))
		for key, value := range command.Env {
			env[key] = value
		}
		command.Env = env
	}
	return command
}
