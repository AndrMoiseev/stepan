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
	"github.com/AndrMoiseev/stepan/internal/setting"
)

func TestTaskRoleStartContextsShareSelfContainedBriefAndProgressiveRules(t *testing.T) {
	repository := t.TempDir()
	writeRoleContextFile(t, filepath.Join(repository, "rules", "index.md"), "# entry\nENTRY-RULE-MARKER\n")
	writeRoleContextFile(t, filepath.Join(repository, "rules", "nested", "testing.md"), "# testing\nNESTED-RULE-MARKER\n")
	rules, err := BuildRulesIndex(repository, setting.RulesFileValidation{
		File: filepath.Join(repository, "rules", "index.md"),
		Root: filepath.Join(repository, "rules"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rules, (RulesIndex{EntryFile: "rules/index.md", EntryContent: "# entry\nENTRY-RULE-MARKER\n", Documents: []string{"rules/index.md", "rules/nested/testing.md"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("rules index = %#v, want %#v", got, want)
	}

	input := TaskRoleStartInput{
		AssignmentID: "assignment-7", BriefID: "brief-7", Brief: "Implement the observable behavior and preserve compatibility.", Rules: rules,
		Checks: []CheckCatalogEntry{{Name: "unit", Kind: setting.CheckKindTests, Required: true}},
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
			"rules/index.md", "ENTRY-RULE-MARKER", "rules/nested/testing.md", "unit - tests (required)",
		} {
			if !strings.Contains(context.StartMessage, required) {
				t.Fatalf("%s start context does not contain %q\n%s", context.Role, required, context.StartMessage)
			}
		}
		for _, forbidden := range []string{"COMPLETE-SPECIFICATION-MARKER", "NESTED-RULE-MARKER"} {
			if strings.Contains(context.StartMessage, forbidden) {
				t.Fatalf("%s unexpectedly received complete specification\n%s", context.Role, context.StartMessage)
			}
		}
	}
	if !strings.Contains(executor.Instructions, "Do not execute commands") || !strings.Contains(executor.Instructions, "request configured checks") {
		t.Fatalf("executor instructions do not prohibit direct commands\n%s", executor.Instructions)
	}
}

func TestRunRoleContextsKeepOrchestratorAndFinalReviewBoundaries(t *testing.T) {
	rules := RulesIndex{EntryFile: "rules/index.md", EntryContent: "FINAL-ENTRY-RULE-MARKER", Documents: []string{"rules/index.md"}}
	orchestrator, err := BuildOrchestratorStartContext(OrchestratorStartInput{
		OpenSpecPackage: "complete change package", MachineTaskList: "machine tasks", RunState: "active", StageResults: []string{"baseline passed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"complete change package", "machine tasks", "active", "baseline passed", "Do not research code", "directly edit only tasks.md of the selected change", "Machine state, Git metadata and operations, and every other file are controller-owned"} {
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
	for _, required := range []string{"COMPLETE-SPECIFICATION-MARKER", "FINAL-DIFF-MARKER", "rules/index.md", "FINAL-ENTRY-RULE-MARKER", "unavailable prior rounds or check history"} {
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

func TestRoleInstructionsDescribeFlatTransportAndRoleSpecificSemantics(t *testing.T) {
	tests := []struct {
		role     ResponseRole
		required []string
	}{
		{ResponseRoleImplementer, []string{"`implementation_ready`: populate `message`", "`checks_requested`: populate `check_names`", "`clarification_required`: populate `question`, `context`, `boundaries`, `references`"}},
		{ResponseRoleBriefer, []string{"Assignment brief format", "without YAML frontmatter", "`assignment_id`, `version`, and `task_ids`"}},
		{ResponseRoleFinalReviewer, []string{"Implementation review result format", "have equal lengths", "expected_results"}},
		{ResponseRoleTaskReviewer, []string{"`review_passed`: populate `message`, `references`", "`changes_requested`: populate `finding_ids`, `findings`, `finding_decisions`, `finding_reasons`, `locations`, `bases`, `expected_results`", "have equal lengths"}},
		{ResponseRoleOrchestrator, []string{"`tasks_extracted`: populate `task_ids`, `task_payloads`", "`tasks_added`: populate `task_ids`, `task_payloads`", "`progress_reflected`: populate `task_ids`", "`clarification_required`: populate `question`, `context`, `boundaries`, `references`"}},
	}
	for _, test := range tests {
		t.Run(string(test.role), func(t *testing.T) {
			instructions, err := RoleInstructions(test.role)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range responseTransportFields {
				if !strings.Contains(instructions, "`"+field+"`") {
					t.Fatalf("instructions omit required transport field %q\n%s", field, instructions)
				}
			}
			for _, required := range append(test.required, "flat JSON object", "`\"\"` for strings and `[]` for arrays", "Populate no field from another action") {
				if !strings.Contains(instructions, required) {
					t.Fatalf("instructions omit %q\n%s", required, instructions)
				}
			}
		})
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
