package impl_loop

import (
	"context"
	"fmt"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

// RuntimeFactory validates the concrete provider configuration before the
// controller starts a role. Implementations own executable/auth availability,
// model selection, and the provider's supported reasoning values.
//
// The flow deliberately does not import provider adapters. This keeps a
// provider's construction and credentials outside the flow while preserving a
// single preflight boundary for every role.
type RuntimeFactory interface {
	Preflight(setting.RuntimeProfile) error
	Create(context.Context, setting.RuntimeProfile) (agentruntime.Runtime, error)
}

// Start constructs a fresh provider runtime for this role using the profile
// already checked during PrepareRuntimes.
func (role PreparedRole) Start(ctx context.Context) (agentruntime.Runtime, error) {
	if role.Factory == nil {
		return nil, fmt.Errorf("implementation runtime factory is missing for role %q", role.Role)
	}
	runtime, err := role.Factory.Create(ctx, role.Profile)
	if err != nil {
		return nil, fmt.Errorf("start implementation role %q profile %q: %w", role.Role, role.Profile.Name, err)
	}
	if runtime == nil {
		return nil, fmt.Errorf("start implementation role %q profile %q: runtime factory returned nil", role.Role, role.Profile.Name)
	}
	return runtime, nil
}

// PreparedRole binds a role to the exact decoded profile and provider factory
// that will create its runtime later. The values are immutable copies from the
// effective implementation configuration.
type PreparedRole struct {
	Role    string
	Profile setting.RuntimeProfile
	Factory RuntimeFactory
}

// PreparedRuntimes is the complete, preflight-validated runtime plan for an
// implementation loop. Bootstrapper is intentionally absent: it has its own
// interactive configuration mode and preflight path.
type PreparedRuntimes struct {
	roles map[string]PreparedRole
}

// Role returns the prepared binding for a loop role.
func (prepared PreparedRuntimes) Role(role string) (PreparedRole, bool) {
	value, ok := prepared.roles[role]
	return value, ok
}

// PrepareRuntimes resolves and validates every role used by the implementation
// loop before any runtime is started. It does not touch factories for providers
// absent from the resolved role plan. Provider-specific validation runs once
// for each role, so differing model or reasoning settings cannot be hidden by
// another role sharing the same provider.
func PrepareRuntimes(configuration setting.Configuration, factories map[string]RuntimeFactory) (PreparedRuntimes, error) {
	resolved := make([]PreparedRole, 0, len(loopRuntimeRoles))
	for _, role := range loopRuntimeRoles {
		profile, err := configuration.ResolveRoleProfile(role)
		if err != nil {
			return PreparedRuntimes{}, err
		}
		factory, ok := factories[profile.Provider]
		if !ok || factory == nil {
			return PreparedRuntimes{}, fmt.Errorf("implementation configuration: role %q profile %q uses unsupported provider %q", role, profile.Name, profile.Provider)
		}
		resolved = append(resolved, PreparedRole{Role: role, Profile: profile, Factory: factory})
	}

	prepared := PreparedRuntimes{roles: make(map[string]PreparedRole, len(resolved))}
	for _, role := range resolved {
		if err := role.Factory.Preflight(role.Profile); err != nil {
			return PreparedRuntimes{}, fmt.Errorf("implementation configuration: role %q profile %q: %w", role.Role, role.Profile.Name, err)
		}
		prepared.roles[role.Role] = role
	}
	return prepared, nil
}

var loopRuntimeRoles = []string{
	setting.RoleOrchestrator,
	setting.RoleBriefer,
	setting.RoleImplementer,
	setting.RoleTaskReviewer,
	setting.RoleExplorer,
	setting.RoleFinalReviewer,
}
