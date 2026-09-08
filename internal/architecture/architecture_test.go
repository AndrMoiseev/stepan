package architecture

import (
	"path/filepath"
	"strings"
	"testing"

	archgo "github.com/arch-go/arch-go/api"
	"github.com/arch-go/arch-go/api/configuration"
)

const modulePath = "github.com/AndrMoiseev/stepan"

func TestDependencies(t *testing.T) {
	configPath := filepath.Join("..", "..", "arch-go.yml")
	config, err := configuration.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load Arch-Go configuration: %v", err)
	}

	result := archgo.CheckArchitecture(configuration.Load(modulePath), *config)
	if result.DependenciesRuleResult == nil {
		t.Fatal("Arch-Go did not evaluate dependency rules")
	}
	for _, rule := range result.DependenciesRuleResult.Results {
		if len(rule.Verifications) == 0 {
			t.Errorf("Arch-Go rule %q did not match a package", rule.Rule.Package)
		}
		for _, verification := range rule.Verifications {
			if !verification.Passes {
				t.Errorf("%s violates %s: %s", verification.Package, rule.Description, strings.Join(verification.Details, "; "))
			}
		}
	}
}
