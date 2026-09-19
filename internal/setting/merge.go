package setting

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	RoleOrchestrator  = "orchestrator"
	RoleBriefer       = "briefer"
	RoleImplementer   = "implementer"
	RoleTaskReviewer  = "task_reviewer"
	RoleExplorer      = "explorer"
	RoleFinalReviewer = "final_reviewer"
	RoleBootstrapper  = "bootstrapper"
)

var defaultRoleProfiles = map[string]string{
	RoleOrchestrator:  "medium",
	RoleBriefer:       "high",
	RoleImplementer:   "medium",
	RoleTaskReviewer:  "high",
	RoleExplorer:      "low",
	RoleFinalReviewer: "ultra",
	RoleBootstrapper:  "high",
}

var loopRoles = []string{
	RoleOrchestrator,
	RoleBriefer,
	RoleImplementer,
	RoleTaskReviewer,
	RoleExplorer,
	RoleFinalReviewer,
}

// Configuration is the effective implementation configuration after the user
// and project sections have been merged. Check-related values deliberately
// remain raw project JSON: their command schema is handled separately.
type Configuration struct {
	Profiles       map[string]json.RawMessage
	Roles          map[string]string
	Limits         map[string]json.RawMessage
	Checks         json.RawMessage
	RequiredChecks json.RawMessage
	RulesFile      json.RawMessage
	MainBranch     json.RawMessage
}

// Merge combines user settings with project settings. Project role assignments
// and individual limits replace only their corresponding entries. A project
// profile replaces the user profile as a whole; a null project profile removes
// its inherited profile.
func Merge(sources Sources) (Configuration, error) {
	user, err := parseSection("user", sources.User)
	if err != nil {
		return Configuration{}, err
	}
	project, err := parseSection("project", sources.Project)
	if err != nil {
		return Configuration{}, err
	}

	if err := rejectUserProjectFields(user); err != nil {
		return Configuration{}, err
	}
	userProfiles, err := rawObject("user", "agentruntime.profiles", sources.UserProfiles, len(sources.UserProfiles) != 0, false)
	if err != nil {
		return Configuration{}, err
	}
	projectProfiles, err := rawObject("project", "agentruntime.profiles", sources.ProjectProfiles, len(sources.ProjectProfiles) != 0, true)
	if err != nil {
		return Configuration{}, err
	}

	configuration := Configuration{
		Profiles:       copyRawValues(userProfiles),
		Roles:          copyStrings(user.roles),
		Limits:         copyRawValues(user.limits),
		Checks:         copyRaw(project.checks),
		RequiredChecks: copyRaw(project.requiredChecks),
		RulesFile:      copyRaw(project.rulesFile),
		MainBranch:     copyRaw(project.mainBranch),
	}
	for name, profile := range projectProfiles {
		if isNull(profile) {
			delete(configuration.Profiles, name)
			continue
		}
		configuration.Profiles[name] = copyRaw(profile)
	}
	for role, profile := range project.roles {
		configuration.Roles[role] = profile
	}
	for name, limit := range project.limits {
		configuration.Limits[name] = copyRaw(limit)
	}
	if err := validateRoleProfiles(configuration); err != nil {
		return Configuration{}, err
	}
	return configuration, nil
}

type section struct {
	roles          map[string]string
	limits         map[string]json.RawMessage
	checks         json.RawMessage
	requiredChecks json.RawMessage
	rulesFile      json.RawMessage
	mainBranch     json.RawMessage
	present        map[string]bool
}

func parseSection(level string, raw json.RawMessage) (section, error) {
	section := section{present: make(map[string]bool)}
	if raw == nil {
		return section, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return section, configurationError(level, "expected an implementation object")
	}
	for name := range fields {
		section.present[name] = true
	}
	for _, misplaced := range []string{"profiles", "auth_token", "nessy", "nessyapp"} {
		if section.present[misplaced] {
			return section, configurationError(level, misplaced+" does not belong in flows.impl_loop")
		}
	}

	var err error
	if section.roles, err = stringObject(level, "roles", fields["roles"], section.present["roles"]); err != nil {
		return section, err
	}
	if section.limits, err = rawObject(level, "limits", fields["limits"], section.present["limits"], false); err != nil {
		return section, err
	}
	section.checks = fields["checks"]
	section.requiredChecks = fields["required_checks"]
	section.rulesFile = fields["rules_file"]
	section.mainBranch = fields["main_branch"]
	return section, nil
}

func rawObject(level, field string, raw json.RawMessage, present, allowNullValues bool) (map[string]json.RawMessage, error) {
	if !present {
		return map[string]json.RawMessage{}, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, configurationError(level, field+" must be an object")
	}
	for name, value := range values {
		if !allowNullValues && isNull(value) {
			return nil, configurationError(level, field+"."+name+" must not be null")
		}
		if field == "agentruntime.profiles" && !isNull(value) && !isObject(value) {
			return nil, configurationError(level, field+"."+name+" must be an object")
		}
	}
	return values, nil
}

func stringObject(level, field string, raw json.RawMessage, present bool) (map[string]string, error) {
	if !present {
		return map[string]string{}, nil
	}
	var rawValues map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawValues); err != nil || rawValues == nil {
		return nil, configurationError(level, field+" must be an object of profile names")
	}
	values := make(map[string]string, len(rawValues))
	for name, value := range rawValues {
		if isNull(value) {
			return nil, configurationError(level, field+" must be an object of profile names")
		}
		var profile string
		if err := json.Unmarshal(value, &profile); err != nil {
			return nil, configurationError(level, field+" must be an object of profile names")
		}
		values[name] = profile
	}
	return values, nil
}

func rejectUserProjectFields(user section) error {
	for _, field := range []string{"checks", "required_checks", "rules_file", "main_branch"} {
		if user.present[field] {
			return configurationError("user", field+" is project-only")
		}
	}
	return nil
}

func validateRoleProfiles(configuration Configuration) error {
	for role, profile := range configuration.Roles {
		if _, ok := configuration.Profiles[profile]; !ok {
			return fmt.Errorf("implementation configuration: role %q references missing profile %q", role, profile)
		}
	}
	return nil
}

// RoleProfile returns the configured profile for a role, or its agreed
// default. It deliberately does not fabricate a profile: provider, model, and
// reasoning remain configuration supplied values.
func (configuration Configuration) RoleProfile(role string) string {
	if profile, ok := configuration.Roles[role]; ok {
		return profile
	}
	return defaultRoleProfiles[role]
}

// ValidateLoopRoles verifies the role assignments required to start an
// implementation loop. Bootstrap is intentionally excluded because it has a
// separate interactive setup path when no bootstrapper profile exists.
func (configuration Configuration) ValidateLoopRoles() error {
	for _, role := range loopRoles {
		profile := configuration.RoleProfile(role)
		if _, ok := configuration.Profiles[profile]; !ok {
			return fmt.Errorf("implementation configuration: role %q references missing profile %q", role, profile)
		}
	}
	return nil
}

// BootstrapProfile returns the configured bootstrapper profile when it is
// available. A missing default profile is not an error here: bootstrap asks
// the user for provider, model, and reasoning before its first agent call.
func (configuration Configuration) BootstrapProfile() (string, bool, error) {
	profile := configuration.RoleProfile(RoleBootstrapper)
	if _, ok := configuration.Profiles[profile]; !ok {
		return "", false, nil
	}
	return profile, true, nil
}

func configurationError(level, reason string) error {
	return fmt.Errorf("%s implementation configuration: %s", level, reason)
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func isObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func copyRawValues(values map[string]json.RawMessage) map[string]json.RawMessage {
	copy := make(map[string]json.RawMessage, len(values))
	for name, value := range values {
		copy[name] = copyRaw(value)
	}
	return copy
}

func copyStrings(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}

func copyRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
