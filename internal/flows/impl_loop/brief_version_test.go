package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func TestExecuteBriefSelectionPublishesControllerGeneratedMarkdownBeforeJournalReference(t *testing.T) {
	run, stateStore, journal, repository, expectation := newBriefSelectionFixture(t)
	defer stateStore.Close()
	if err := PrepareBriefSelection(context.Background(), stateStore, run, "select"); err != nil {
		t.Fatal(err)
	}
	content := "## Expected result\n\nImplement only task A."
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefReadyResponseWithContent(t, []implstate.TaskID{"A"}, content)}}}
	if _, err := ExecuteBriefSelection(context.Background(), briefSelectionCall("assignment-1", run, stateStore, journal, repository, expectation, runtime)); err != nil {
		t.Fatal(err)
	}
	brief := run.Assignments[0].Briefs[0]
	if brief.ID != "brief-assignment-1-v1" || brief.Number != 1 || brief.Document.ID != "brief-assignment-1-v1.md" || brief.Document.Digest == "" {
		t.Fatalf("controller brief metadata = %#v", brief)
	}
	path, err := journal.ArtifactPath(brief.Document)
	if err != nil {
		t.Fatalf("published brief path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nassignment_id: assignment-1\nversion: 1\ntask_ids:\n  - A\n---\n\n" + content + "\n"
	if got := string(data); got != want {
		t.Fatalf("brief Markdown = %q, want %q", got, want)
	}
	current, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil {
		t.Fatal(err)
	}
	if got := current.Assignments[0].Briefs[0].Document; got != brief.Document {
		t.Fatalf("journal brief reference = %#v, want %#v", got, brief.Document)
	}
}

func TestPersistRefinedBriefKeepsAssignmentTasksAndBuildsSharedCurrentRoleContext(t *testing.T) {
	run, stateStore, journal, repository, expectation := newBriefSelectionFixture(t)
	defer stateStore.Close()
	if err := PrepareBriefSelection(context.Background(), stateStore, run, "select"); err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefReadyResponseWithContent(t, []implstate.TaskID{"A", "B"}, "initial brief")}}}
	if _, err := ExecuteBriefSelection(context.Background(), briefSelectionCall("assignment-1", run, stateStore, journal, repository, expectation, runtime)); err != nil {
		t.Fatal(err)
	}
	initial := run.Assignments[0].Briefs[0]
	refinement := ResponseExpectation{Role: ResponseRoleBriefer, State: ResponseStateBriefRefinement, Scope: ResponseScopeAssignment, Binding: expectation.Binding}
	refinement.Binding.AssignmentID, refinement.Binding.BriefID = "assignment-1", initial.ID
	refinedBody := "refined brief evaluates the code already written"
	response := AgentResponse{Kind: ResponseBriefReady, TaskIDs: []implstate.TaskID{"A", "B"}, Brief: &refinedBody, Binding: refinement.Binding}
	refined, err := PersistRefinedBriefVersion(context.Background(), journal, stateStore, run, refinement, response)
	if err != nil {
		t.Fatal(err)
	}
	if refined.ID != "brief-assignment-1-v2" || refined.Number != 2 || !reflect.DeepEqual(run.Assignments[0].TaskIDs, []implstate.TaskID{"A", "B"}) {
		t.Fatalf("refined assignment = %#v, brief = %#v", run.Assignments[0], refined)
	}
	implementer, reviewer, err := BuildCurrentTaskRoleContexts(journal, run, "assignment-1", RulesIndex{EntryFile: "rules.md", EntryContent: "current rules", Documents: []string{"rules.md"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	currentDocument := string(mustReadBrief(t, journal, refined.Document))
	for _, start := range []RoleStartContext{implementer, reviewer} {
		if !strings.Contains(start.StartMessage, "Brief version: brief-assignment-1-v2") || !strings.Contains(start.StartMessage, currentDocument) || strings.Contains(start.StartMessage, "initial brief") {
			t.Fatalf("role did not receive the same current version: %s", start.StartMessage)
		}
	}
	for _, required := range []string{"assignment_id: assignment-1", "version: 2", "task_ids:\n  - A\n  - B", refinedBody} {
		if !strings.Contains(currentDocument, required) {
			t.Fatalf("current brief misses %q:\n%s", required, currentDocument)
		}
	}
	if strings.Contains(currentDocument, "current rules") {
		t.Fatal("brief artifact unexpectedly snapshots rules")
	}
}

func TestPersistRefinedBriefRejectsChangedAssignmentSelection(t *testing.T) {
	run, stateStore, journal, repository, expectation := newBriefSelectionFixture(t)
	defer stateStore.Close()
	if err := PrepareBriefSelection(context.Background(), stateStore, run, "select"); err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefReadyResponse(t, []implstate.TaskID{"A", "B"})}}}
	if _, err := ExecuteBriefSelection(context.Background(), briefSelectionCall("assignment-1", run, stateStore, journal, repository, expectation, runtime)); err != nil {
		t.Fatal(err)
	}
	current := run.Assignments[0].Briefs[0]
	refinement := ResponseExpectation{Role: ResponseRoleBriefer, State: ResponseStateBriefRefinement, Scope: ResponseScopeAssignment, Binding: expectation.Binding}
	refinement.Binding.AssignmentID, refinement.Binding.BriefID = "assignment-1", current.ID
	body := "attempt to change scope"
	response := AgentResponse{Kind: ResponseBriefReady, TaskIDs: []implstate.TaskID{"A"}, Brief: &body, Binding: refinement.Binding}
	if _, err := PersistRefinedBriefVersion(context.Background(), journal, stateStore, run, refinement, response); !errors.Is(err, ErrBriefVersion) {
		t.Fatalf("changed task selection = %v, want ErrBriefVersion", err)
	}
	if len(run.Assignments[0].Briefs) != 1 {
		t.Fatalf("invalid refinement persisted a version: %#v", run.Assignments[0].Briefs)
	}
}

func briefReadyResponseWithContent(t *testing.T, taskIDs []implstate.TaskID, content string) json.RawMessage {
	t.Helper()
	payload := responsePayloadMap(ResponseBriefReady)
	values := make([]string, len(taskIDs))
	for index, taskID := range taskIDs {
		values[index] = string(taskID)
	}
	payload["task_ids"] = values
	payload["brief"] = content
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustReadBrief(t *testing.T, journal *runstore.Run, reference implstate.EvidenceRef) []byte {
	t.Helper()
	data, err := journal.Read(reference)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
