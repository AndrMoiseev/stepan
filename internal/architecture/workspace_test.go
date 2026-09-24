package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestImplementationGitAccessGoesThroughWorkspace(t *testing.T) {
	root := filepath.Join("..", "flows", "impl_loop")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, violation := range workspaceAccessViolations(filepath.ToSlash(relative), string(contents)) {
			t.Errorf("%s: %s", relative, violation)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func workspaceAccessViolations(path, source string) []string {
	if strings.HasPrefix(path, "workspace/git/") {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ParseComments)
	if err != nil {
		return []string{err.Error()}
	}
	var violations []string
	aliases := map[string]string{}
	gitIntegration := false
	processIntegration := false
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//go:build ") && strings.Contains(comment.Text, "git_integration") {
				gitIntegration = true
			}
			if strings.HasPrefix(comment.Text, "//go:build ") && strings.Contains(comment.Text, "process_integration") {
				processIntegration = true
			}
		}
	}
	for _, imported := range file.Imports {
		name, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			violations = append(violations, err.Error())
			continue
		}
		alias := filepath.Base(name)
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		aliases[alias] = name
		if name == "os/exec" && !strings.HasPrefix(path, "checkexec/") && !(strings.HasSuffix(path, "_test.go") && processIntegration) {
			violations = append(violations, "process execution belongs in checkexec or workspace/git; process tests require process_integration")
		}
		if name == modulePath+"/internal/git" && alias == "." {
			violations = append(violations, "dot import of internal/git bypasses the Workspace contract")
		}
		if strings.HasPrefix(name, modulePath+"/internal/flows/impl_loop/workspace/git") && strings.HasSuffix(path, "_test.go") && !gitIntegration {
			violations = append(violations, "real Git adapters and fixtures require git_integration; use testfs for orchestration")
		}
		if strings.HasSuffix(name, "/workspace/git/testfixture") && !strings.HasSuffix(path, "_test.go") {
			violations = append(violations, "Git fixtures are test-only")
		}
	}
	// Shared serializable types remain in internal/git. All executable Git
	// capabilities, including a function captured into a variable, stay in Workspace.
	allowed := map[string]bool{
		"Snapshot": true, "Difference": true, "BoundaryError": true, "SubmoduleCycleError": true,
		"ErrRepositoryDiverged": true, "ErrOutsideBoundary": true, "ErrSubmoduleCycle": true, "ErrRestoreUnsafe": true,
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if identifier, ok := selector.X.(*ast.Ident); ok && aliases[identifier.Name] == modulePath+"/internal/git" && !allowed[selector.Sel.Name] {
				violations = append(violations, "internal/git."+selector.Sel.Name+" must be accessed through Workspace")
			}
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok || aliases[identifier.Name] != "os/exec" {
			return true
		}
		for _, argument := range call.Args {
			literal, ok := argument.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				continue
			}
			for _, word := range strings.Fields(value) {
				program := strings.ToLower(filepath.Base(strings.ReplaceAll(word, "\\", "/")))
				if program == "git" || program == "git.exe" {
					violations = append(violations, "Git subprocesses belong in workspace/git")
				}
			}
		}
		return true
	})
	return violations
}

func TestWorkspaceAccessRuleRejectsBypasses(t *testing.T) {
	for _, test := range []struct {
		name, path, source string
		allowed            bool
	}{
		{"snapshot type", "flow.go", `package impl_loop; import repo "github.com/AndrMoiseev/stepan/internal/git"; var snapshot repo.Snapshot`, true},
		{"direct capture", "flow.go", `package impl_loop; import repo "github.com/AndrMoiseev/stepan/internal/git"; var capture = repo.Capture`, false},
		{"dot import", "flow.go", `package impl_loop; import . "github.com/AndrMoiseev/stepan/internal/git"`, false},
		{"Git subprocess", "flow.go", `package impl_loop; import child "os/exec"; var command = child.Command("git", "status")`, false},
		{"Git shell", "flow.go", `package impl_loop; import "os/exec"; var command = exec.Command("sh", "-c", "git status")`, false},
		{"ordinary adapter test", "flow_test.go", `package impl_loop; import _ "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/git"`, false},
		{"integration wiring", "flow_test.go", "//go:build git_integration\n\npackage impl_loop\nimport _ \"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/git\"", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			violations := workspaceAccessViolations(test.path, test.source)
			if (len(violations) == 0) != test.allowed {
				t.Fatalf("violations = %v, allowed = %v", violations, test.allowed)
			}
		})
	}
}
