package architecture

import (
	"bytes"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
		"internal/setting/doc.go":               "package setting\n",
		"internal/flows/spec/doc.go":            "package specflow\n",
		"internal/flows/impl_loop/impl_loop.go": "package impl_loop\n\nimport _ \"github.com/AndrMoiseev/stepan/internal/agentruntime\"\nimport _ \"github.com/AndrMoiseev/stepan/internal/setting\"\n",
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

func TestImplementationStoreMayUseOnlyNestedState(t *testing.T) {
	config := loadConfig(t)
	result := evaluateFixture(t, config, map[string]string{
		"internal/flows/impl_loop/doc.go":       "package impl_loop\n",
		"internal/flows/impl_loop/state/doc.go": "package state\n",
		"internal/flows/impl_loop/store/doc.go": "package store\n\nimport _ \"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state\"\n",
	})
	assertPackagePasses(t, result, modulePath+"/internal/flows/impl_loop/store")

	result = evaluateFixture(t, config, map[string]string{
		"internal/flows/impl_loop/doc.go":       "package impl_loop\n",
		"internal/flows/impl_loop/store/doc.go": "package store\n\nimport _ \"github.com/AndrMoiseev/stepan/internal/flows/impl_loop\"\n",
	})
	assertPackageFailsForForbiddenFlowRule(t, result, modulePath+"/internal/flows/impl_loop/store", "internal/flows/impl_loop")
}

func TestExternalProcessTestsDeclareIntegrationSuite(t *testing.T) {
	root := filepath.Join("..", "..")
	var unclassified []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, contents, parser.ImportsOnly)
		if err != nil {
			return err
		}
		importsExec := false
		for _, imported := range parsed.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if name == "os/exec" {
				importsExec = true
				break
			}
		}
		if !importsExec {
			return nil
		}
		header := contents
		if len(header) > 512 {
			header = header[:512]
		}
		if bytes.Contains(header, []byte("git_integration")) || bytes.Contains(header, []byte("process_integration")) || bytes.Contains(header, []byte("nessy_real_cli")) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		unclassified = append(unclassified, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(unclassified)
	if len(unclassified) != 0 {
		t.Fatalf("tests importing os/exec must declare an integration build tag:\n%s", strings.Join(unclassified, "\n"))
	}
}

func TestTransientRunStoreIsUsedOnlyByTests(t *testing.T) {
	root := filepath.Join("..", "..")
	storeImport := modulePath + "/internal/flows/impl_loop/store"
	var productionCallers []string
	for _, sourceRoot := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, sourceRoot), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			aliases := make(map[string]struct{})
			for _, imported := range parsed.Imports {
				name, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				if name != storeImport {
					continue
				}
				alias := "store"
				if imported.Name != nil {
					alias = imported.Name.Name
				}
				aliases[alias] = struct{}{}
			}
			called := false
			ast.Inspect(parsed, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "NewTransient" {
					return true
				}
				identifier, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				if _, ok := aliases[identifier.Name]; ok {
					called = true
					return false
				}
				return true
			})
			if called {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				productionCallers = append(productionCallers, filepath.ToSlash(relative))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(productionCallers)
	if len(productionCallers) != 0 {
		t.Fatalf("runstore.NewTransient is test-only; production callers:\n%s", strings.Join(productionCallers, "\n"))
	}
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
