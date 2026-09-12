package architecture

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	archgo "github.com/arch-go/arch-go/api"
	"github.com/arch-go/arch-go/api/configuration"
)

const modulePath = "github.com/AndrMoiseev/stepan"

func TestDependencies(t *testing.T) {
	config := loadConfig(t)
	assertPassingDependencyRules(t, archgo.CheckArchitecture(configuration.Load(modulePath), *config))
}

func TestDependencyEvaluatorAllowsSharedInfrastructureForImplementationFlow(t *testing.T) {
	result := evaluateFixture(t, loadConfig(t), map[string]string{
		"internal/agentruntime/doc.go":          "package agentruntime\n",
		"internal/flows/spec/doc.go":            "package specflow\n",
		"internal/flows/impl_loop/impl_loop.go": "package impl_loop\n\nimport _ \"github.com/AndrMoiseev/stepan/internal/agentruntime\"\n",
	})
	assertPackagePasses(t, result, modulePath+"/internal/flows/impl_loop")
}

func TestDependencyEvaluatorRejectsFlowDependencies(t *testing.T) {
	config := loadConfig(t)
	specToImplementation := evaluateFixture(t, config, map[string]string{
		"internal/agentruntime/doc.go":    "package agentruntime\n",
		"internal/flows/spec/spec.go":     "package specflow\n\nimport _ \"github.com/AndrMoiseev/stepan/internal/flows/impl_loop\"\n",
		"internal/flows/impl_loop/doc.go": "package impl_loop\n",
	})
	assertPackageFailsForForbiddenFlowRule(t, specToImplementation, modulePath+"/internal/flows/spec", "internal/flows/impl_loop")
	implementationToSpec := evaluateFixture(t, config, map[string]string{
		"internal/agentruntime/doc.go":          "package agentruntime\n",
		"internal/flows/spec/doc.go":            "package specflow\n",
		"internal/flows/impl_loop/impl_loop.go": "package impl_loop\n\nimport _ \"github.com/AndrMoiseev/stepan/internal/flows/spec\"\n",
	})
	assertPackageFailsForForbiddenFlowRule(t, implementationToSpec, modulePath+"/internal/flows/impl_loop", "internal/flows/spec")
}

func loadConfig(t *testing.T) *configuration.Config {
	t.Helper()
	configPath := filepath.Join("..", "..", "arch-go.yml")
	config, err := configuration.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load Arch-Go configuration: %v", err)
	}
	return config
}

func assertPassingDependencyRules(t *testing.T, result *archgo.Result) {
	t.Helper()
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

func evaluateFixture(t *testing.T, config *configuration.Config, files map[string]string) *archgo.Result {
	t.Helper()
	gopath := t.TempDir()
	root := filepath.Join(gopath, "src", filepath.FromSlash(modulePath))
	for path, content := range files {
		file := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.26.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	previousGOPATH := build.Default.GOPATH
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	build.Default.GOPATH = gopath
	t.Setenv("GOPATH", gopath)
	defer func() {
		build.Default.GOPATH = previousGOPATH
		if err := os.Chdir(previousDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	return archgo.CheckArchitecture(configuration.Load(modulePath), *config)
}

func assertPackagePasses(t *testing.T, result *archgo.Result, packagePath string) {
	t.Helper()
	evaluated := false
	for _, rule := range result.DependenciesRuleResult.Results {
		for _, verification := range rule.Verifications {
			if verification.Package == packagePath {
				evaluated = true
				if !verification.Passes {
					t.Fatalf("Arch-Go rejected allowed dependency for %s: %s", packagePath, strings.Join(verification.Details, "; "))
				}
			}
		}
	}
	if !evaluated {
		t.Fatalf("Arch-Go did not evaluate shared infrastructure import for %s", packagePath)
	}
}

func assertPackageFailsForForbiddenFlowRule(t *testing.T, result *archgo.Result, packagePath, forbiddenPath string) {
	t.Helper()
	for _, rule := range result.DependenciesRuleResult.Results {
		if rule.Rule.ShouldNotDependsOn == nil || !contains(rule.Rule.ShouldNotDependsOn.Internal, forbiddenPath) {
			continue
		}
		for _, verification := range rule.Verifications {
			if verification.Package == packagePath && !verification.Passes {
				return
			}
		}
	}
	t.Fatalf("Arch-Go did not reject %s importing %s through its flow-isolation rule", packagePath, forbiddenPath)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
