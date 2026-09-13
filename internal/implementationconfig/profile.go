package implementationconfig

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RuntimeProfile is the configured, provider-neutral identity used to create
// one role runtime. Model selection is never inferred from the profile name.
// Reasoning is empty when the selected provider does not need a reasoning
// setting.
type RuntimeProfile struct {
	Name      string
	Provider  string
	Model     string
	Reasoning string
}

// ResolveRoleProfile decodes the complete profile selected for role. It does
// not validate provider-specific model or reasoning values: that belongs to
// the provider factory which knows the runtime it will create.
func (configuration Configuration) ResolveRoleProfile(role string) (RuntimeProfile, error) {
	name := configuration.RoleProfile(role)
	raw, ok := configuration.Profiles[name]
	if !ok {
		return RuntimeProfile{}, fmt.Errorf("implementation configuration: role %q references missing profile %q", role, name)
	}
	return decodeRuntimeProfile(name, raw)
}

func decodeRuntimeProfile(name string, raw json.RawMessage) (RuntimeProfile, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return RuntimeProfile{}, fmt.Errorf("implementation configuration: profile %q must be an object", name)
	}
	profile := RuntimeProfile{Name: name}
	var err error
	if profile.Provider, err = requiredProfileString(name, "provider", fields); err != nil {
		return RuntimeProfile{}, err
	}
	if profile.Model, err = requiredProfileString(name, "model", fields); err != nil {
		return RuntimeProfile{}, err
	}
	if rawReasoning, present := fields["reasoning"]; present {
		if err := json.Unmarshal(rawReasoning, &profile.Reasoning); err != nil || strings.TrimSpace(profile.Reasoning) == "" {
			return RuntimeProfile{}, fmt.Errorf("implementation configuration: profile %q reasoning must be a nonempty string", name)
		}
	}
	return profile, nil
}

func requiredProfileString(name, field string, fields map[string]json.RawMessage) (string, error) {
	raw, present := fields[field]
	if !present {
		return "", fmt.Errorf("implementation configuration: profile %q %s is required", name, field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("implementation configuration: profile %q %s must be a nonempty string", name, field)
	}
	return value, nil
}
