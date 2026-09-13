package impl_loop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

func TestTaskRoleStartContextsShareSelfContainedBriefAndProgressiveRules(t *testing.T) {
	repository := t.TempDir()
	writeRoleContextFile(t, filepath.Join(repository, "rules", "index.md"), "# entry\n")
	writeRoleContextFile(t, filepath.Join(repository, "rules", "nested", "testing.md"), "# testing\n")
	rules, err := BuildRulesIndex(repository, implementationconfig.RulesFileValidation{
		File: filepath.Join(repository, "rules", "index.md"),
		Root: filepath.Join(repository, "rules"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rules, (RulesIndex{EntryFile: "rules/index.md", Documents: []string{"rules/index.md", "rules/nested/testing.md"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("rules index = %#v, want %#v", got, want)
	}

	input := TaskRoleStartInput{
		AssignmentID: "assignment-7", BriefID: "brief-7", Brief: "Implement the observable behavior and preserve compatibility.", Rules: rules,
		Checks: []CheckCatalogEntry{{Name: "unit", Kind: implementationconfig.CheckKindTests, Required: true}},
	}
	executor, err := BuildImplementerStartContext(input)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := BuildTaskReviewerStartContext(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, context := range []RoleStartContext{executor, reviewer} {
		for _, required := range []string{
			"Assignment: assignment-7", "Brief version: brief-7", input.Brief,
			"Read `rules/index.md` first", "rules/nested/testing.md", "unit - tests (required)",
		} {
			if !strings.Contains(context.StartMessage, required) {
				t.Fatalf("%s start context does not contain %q\n%s", context.Role, required, context.StartMessage)
			}
		}
		if strings.Contains(context.StartMessage, "COMPLETE-SPECIFICATION-MARKER") {
			t.Fatalf("%s unexpectedly received complete specification\n%s", context.Role, context.StartMessage)
		}
	}
	if !strings.Contains(executor.Instructions, "Do not execute commands") || !strings.Contains(executor.Instructions, "request configured checks") {
		t.Fatalf("executor instructions do not prohibit direct commands\n%s", executor.Instructions)
	}
}

func TestRunRoleContextsKeepOrchestratorAndFinalReviewBoundaries(t *testing.T) {
	rules := RulesIndex{EntryFile: "rules/index.md", Documents: []string{"rules/index.md"}}
	orchestrator, err := BuildOrchestratorStartContext(OrchestratorStartInput{
		OpenSpecPackage: "complete change package", MachineTaskList: "machine tasks", RunState: "active", StageResults: []string{"baseline passed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"complete change package", "machine tasks", "active", "baseline passed", "Do not research code"} {
		if !strings.Contains(orchestrator.StartMessage, required) {
			t.Fatalf("orchestrator context does not contain %q\n%s", required, orchestrator.StartMessage)
		}
	}

	final, err := BuildFinalReviewerStartContext(FinalReviewerStartInput{
		Specification: "COMPLETE-SPECIFICATION-MARKER", Rules: rules, Diff: "FINAL-DIFF-MARKER",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"COMPLETE-SPECIFICATION-MARKER", "FINAL-DIFF-MARKER", "rules/index.md", "unavailable prior rounds or check history"} {
		if !strings.Contains(final.StartMessage, required) {
			t.Fatalf("final reviewer context does not contain %q\n%s", required, final.StartMessage)
		}
	}
	for _, forbidden := range []string{"PREVIOUS-ROUND-MARKER", "CHECK-HISTORY-MARKER", "IMPLEMENTATION-HISTORY-MARKER"} {
		if strings.Contains(final.StartMessage, forbidden) {
			t.Fatalf("final reviewer received forbidden history marker %q\n%s", forbidden, final.StartMessage)
		}
	}
}

func TestRoleInstructionsAndThreadConfigCoverEveryRole(t *testing.T) {
	roles := []ResponseRole{
		ResponseRoleOrchestrator, ResponseRoleBriefer, ResponseRoleImplementer, ResponseRoleTaskReviewer,
		ResponseRoleExplorer, ResponseRoleFinalReviewer, ResponseRoleBootstrapper,
	}
	for _, role := range roles {
		t.Run(string(role), func(t *testing.T) {
			instructions, err := RoleInstructions(role)
			if err != nil || strings.TrimSpace(instructions) == "" {
				t.Fatalf("instructions = %q, %v", instructions, err)
			}
		})
	}
	if _, err := RoleInstructions("unknown"); !errors.Is(err, ErrInvalidRoleContext) {
		t.Fatalf("unknown role error = %v", err)
	}

	start, err := BuildImplementerStartContext(TaskRoleStartInput{AssignmentID: "assignment", BriefID: "brief", Brief: "brief"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := ThreadConfigForRoleContext(start, agentruntime.ThreadConfig{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if config.BootstrapInstructions != start.StartMessage {
		t.Fatalf("bootstrap context was not preserved")
	}
	if len(config.OutputSchema) == 0 {
		t.Fatal("response schema was not set")
	}
	var schema map[string]any
	if err := json.Unmarshal(config.OutputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %#v", schema["type"])
	}
}

func TestRoleContextRequiresMandatoryInputs(t *testing.T) {
	if _, err := BuildImplementerStartContext(TaskRoleStartInput{AssignmentID: "a", BriefID: "b"}); !errors.Is(err, ErrInvalidRoleContext) {
		t.Fatalf("missing brief error = %v", err)
	}
	if _, err := BuildOrchestratorStartContext(OrchestratorStartInput{OpenSpecPackage: "spec", RunState: "active"}); !errors.Is(err, ErrInvalidRoleContext) {
		t.Fatalf("missing task list error = %v", err)
	}
	if _, err := BuildFinalReviewerStartContext(FinalReviewerStartInput{Specification: "spec"}); !errors.Is(err, ErrInvalidRoleContext) {
		t.Fatalf("missing diff error = %v", err)
	}
	if _, err := ThreadConfigForRoleContext(RoleStartContext{Role: ResponseRoleImplementer}, agentruntime.ThreadConfig{Workspace: t.TempDir()}); !errors.Is(err, ErrInvalidRoleContext) {
		t.Fatalf("incomplete start context error = %v", err)
	}
}

func writeRoleContextFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
