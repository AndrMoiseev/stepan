package impl_loop

import (
	"context"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
)

func TestInvokeControlledAgentCallRejectsRestoredViolationBeforeRetry(t *testing.T) {
	t.Parallel()
	repository := newFilesystemWorkspace(t)
	writeAgentFile(t, repository, ".stepan/settings.json", "protected before\n")
	runtime := &controlledCallRuntime{turns: []controlledTurn{
		{raw: controlledResponse(t, "MUST NOT BE ACCEPTED"), before: func() { writeAgentFile(t, repository, ".stepan/settings.json", "forbidden\n") }},
		{raw: controlledResponse(t, "accepted after restoration")},
	}}
	call := controlledCallFixture(t, runtime)
	call.Repository = repository
	call.Workspace = testfs.New()
	call.Policy = AgentCallPolicy{Role: AgentRoleExecutor, CallID: call.Expectation.Binding.CallID, AllowUnprotected: true, ProtectedPaths: []string{".stepan/settings.json"}}
	result, err := InvokeControlledAgentCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 || result.Response.Message == nil || *result.Response.Message != "accepted after restoration" {
		t.Fatalf("violation result = %#v", result)
	}
	assertAgentFile(t, repository, ".stepan/settings.json", "protected before\n")
	if len(runtime.messages) != 2 || !strings.Contains(runtime.messages[1], "changed prohibited paths") {
		t.Fatalf("violation did not produce a retry diagnostic: %#v", runtime.messages)
	}
	if records := readViolationRecords(t, call.Journal); len(records) != 1 || records[0].RestorationResult != "restored" {
		t.Fatalf("violation records = %#v", records)
	}
}
