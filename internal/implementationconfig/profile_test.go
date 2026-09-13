package implementationconfig

import (
	"strings"
	"testing"
)

func TestResolveRoleProfileRequiresProviderAndModel(t *testing.T) {
	for _, test := range []struct {
		name, profile, want string
	}{
		{"missing provider", `{"model":"m"}`, "provider is required"},
		{"empty provider", `{"provider":" ","model":"m"}`, "provider must be a nonempty string"},
		{"missing model", `{"provider":"codex"}`, "model is required"},
		{"empty reasoning", `{"provider":"codex","model":"m","reasoning":" "}`, "reasoning must be a nonempty string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration, err := Merge(sources(t, `{"profiles":{"medium":`+test.profile+`}}`, ``))
			if err != nil {
				t.Fatal(err)
			}
			_, err = configuration.ResolveRoleProfile(RoleOrchestrator)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResolveRoleProfile error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestResolveRoleProfilePreservesProviderModelAndReasoning(t *testing.T) {
	configuration, err := Merge(sources(t, `{"profiles":{"medium":{"provider":"claude","model":"sonnet","reasoning":"high"}}}`, ``))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := configuration.ResolveRoleProfile(RoleOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	if want := (RuntimeProfile{Name: "medium", Provider: "claude", Model: "sonnet", Reasoning: "high"}); profile != want {
		t.Fatalf("profile = %#v, want %#v", profile, want)
	}
}
