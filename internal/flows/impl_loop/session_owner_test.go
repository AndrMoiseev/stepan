package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestSessionOwnerScopesSessionsAndContinuesAfterExplorer(t *testing.T) {
	factory := &sessionRuntimeFactory{}
	owner := newSessionOwnerForTest(t, factory)
	t.Cleanup(func() { _ = owner.Close() })

	orchestrator, err := owner.Orchestrator(context.Background(), sessionStartContext(t, ResponseRoleOrchestrator))
	if err != nil {
		t.Fatal(err)
	}
	if same, err := owner.Orchestrator(context.Background(), sessionStartContext(t, ResponseRoleOrchestrator)); err != nil || same != orchestrator {
		t.Fatalf("orchestrator session = %p, %v; want continued %p", same, err, orchestrator)
	}

	assignmentA := implementationstate.AssignmentID("assignment-a")
	brieferA, err := owner.Briefer(context.Background(), assignmentA, sessionBrieferStartContext(t, assignmentA))
	if err != nil {
		t.Fatal(err)
	}
	implementerA, err := owner.Assignment(context.Background(), assignmentA, ResponseRoleImplementer, sessionStartContext(t, ResponseRoleImplementer))
	if err != nil {
		t.Fatal(err)
	}
	reviewerA, err := owner.Assignment(context.Background(), assignmentA, ResponseRoleTaskReviewer, sessionStartContext(t, ResponseRoleTaskReviewer))
	if err != nil {
		t.Fatal(err)
	}
	if brieferA == implementerA || implementerA == reviewerA || brieferA == reviewerA {
		t.Fatal("assignment roles unexpectedly share one session")
	}
	if _, err := implementerA.RunTurn("implement"); err != nil {
		t.Fatal(err)
	}
	explorerOne, err := owner.Explorer(context.Background(), sessionStartContext(t, ResponseRoleExplorer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := explorerOne.RunTurn("research"); err != nil {
		t.Fatal(err)
	}
	if same, err := owner.Assignment(context.Background(), assignmentA, ResponseRoleImplementer, sessionStartContext(t, ResponseRoleImplementer)); err != nil || same != implementerA {
		t.Fatalf("implementer after explorer = %p, %v; want continued %p", same, err, implementerA)
	}
	if _, err := implementerA.RunTurn("apply research result"); err != nil {
		t.Fatal(err)
	}
	explorerTwo, err := owner.Explorer(context.Background(), sessionStartContext(t, ResponseRoleExplorer))
	if err != nil {
		t.Fatal(err)
	}
	if explorerTwo == explorerOne {
		t.Fatal("two Explorer requests reused a session")
	}

	finalOne, err := owner.FinalReviewer(context.Background(), "round-1", sessionStartContext(t, ResponseRoleFinalReviewer))
	if err != nil {
		t.Fatal(err)
	}
	if same, err := owner.FinalReviewer(context.Background(), "round-1", sessionStartContext(t, ResponseRoleFinalReviewer)); err != nil || same != finalOne {
		t.Fatalf("final-review round session = %p, %v; want continued %p", same, err, finalOne)
	}
	if next, err := owner.FinalReviewer(context.Background(), "round-2", sessionStartContext(t, ResponseRoleFinalReviewer)); err != nil || next == finalOne {
		t.Fatalf("next final-review round = %p, %v; want fresh session", next, err)
	}

	assignmentB := implementationstate.AssignmentID("assignment-b")
	for _, role := range []ResponseRole{ResponseRoleBriefer, ResponseRoleImplementer, ResponseRoleTaskReviewer} {
		var next *AgentSession
		if role == ResponseRoleBriefer {
			next, err = owner.Briefer(context.Background(), assignmentB, sessionBrieferStartContext(t, assignmentB))
		} else {
			next, err = owner.Assignment(context.Background(), assignmentB, role, sessionStartContext(t, role))
		}
		if err != nil {
			t.Fatal(err)
		}
		previous := map[ResponseRole]*AgentSession{ResponseRoleBriefer: brieferA, ResponseRoleImplementer: implementerA, ResponseRoleTaskReviewer: reviewerA}[role]
		if next == previous {
			t.Fatalf("next assignment reused %s session", role)
		}
	}

	if got := factory.turns(implementerA); got != 2 {
		t.Fatalf("implementer turns = %d, want continuation with 2 turns", got)
	}
	for _, config := range factory.configurations() {
		role := roleFromBootstrap(config.BootstrapInstructions)
		wantWrite := role == ResponseRoleOrchestrator || role == ResponseRoleImplementer
		if config.WorkspaceWriteAllowed != wantWrite {
			t.Fatalf("%s workspace write = %t, want %t", role, config.WorkspaceWriteAllowed, wantWrite)
		}
	}
}

func TestSessionOwnerRejectsInvalidScopesAndClosedOwner(t *testing.T) {
	factory := &sessionRuntimeFactory{}
	owner := newSessionOwnerForTest(t, factory)
	if _, err := owner.Assignment(context.Background(), "", ResponseRoleImplementer, sessionStartContext(t, ResponseRoleImplementer)); err == nil {
		t.Fatal("empty assignment ID was accepted")
	}
	if _, err := owner.Assignment(context.Background(), "assignment", ResponseRoleBriefer, sessionStartContext(t, ResponseRoleBriefer)); err == nil {
		t.Fatal("briefer was accepted through generic assignment session")
	}
	if _, err := owner.Assignment(context.Background(), "assignment", ResponseRoleExplorer, sessionStartContext(t, ResponseRoleExplorer)); err == nil {
		t.Fatal("Explorer was accepted as an assignment session")
	}
	if _, err := owner.FinalReviewer(context.Background(), "", sessionStartContext(t, ResponseRoleFinalReviewer)); err == nil {
		t.Fatal("empty final-review round was accepted")
	}
	if _, err := owner.Orchestrator(context.Background(), sessionStartContext(t, ResponseRoleBriefer)); err == nil {
		t.Fatal("mismatched role context was accepted")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Orchestrator(context.Background(), sessionStartContext(t, ResponseRoleOrchestrator)); !errors.Is(err, ErrSessionOwnerClosed) {
		t.Fatalf("closed owner error = %v", err)
	}
}

func TestSessionOwnerRecreateIgnoresTerminalCloseButSurfacesCleanupFailure(t *testing.T) {
	t.Run("terminal close", func(t *testing.T) {
		factory := &sessionRuntimeFactory{closeThreadErr: agentruntime.ErrTurnInterrupted}
		owner := newSessionOwnerForTest(t, factory)
		t.Cleanup(func() { _ = owner.Close() })
		session, err := owner.Assignment(context.Background(), "assignment", ResponseRoleImplementer, sessionStartContext(t, ResponseRoleImplementer))
		if err != nil {
			t.Fatal(err)
		}
		next, err := session.Recreate(context.Background())
		if err != nil || next == session {
			t.Fatalf("terminal close recreation = %p, %v", next, err)
		}
	})

	t.Run("cleanup failure", func(t *testing.T) {
		factory := &sessionRuntimeFactory{closeThreadErr: errors.Join(agentruntime.ErrThreadFailed, agentruntime.ErrRuntimeCleanup)}
		owner := newSessionOwnerForTest(t, factory)
		t.Cleanup(func() { _ = owner.Close() })
		session, err := owner.Assignment(context.Background(), "assignment", ResponseRoleImplementer, sessionStartContext(t, ResponseRoleImplementer))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.Recreate(context.Background()); !errors.Is(err, agentruntime.ErrRuntimeCleanup) {
			t.Fatalf("cleanup failure was swallowed: %v", err)
		}
	})
}

func newSessionOwnerForTest(t *testing.T, factory RuntimeFactory) *SessionOwner {
	t.Helper()
	roles := make(map[string]PreparedRole, len(loopRuntimeRoles))
	for _, role := range loopRuntimeRoles {
		roles[role] = PreparedRole{Role: role, Profile: implementationconfig.RuntimeProfile{Name: role}, Factory: factory}
	}
	owner, err := NewSessionOwner(PreparedRuntimes{roles: roles}, agentruntime.ThreadConfig{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func sessionStartContext(t *testing.T, role ResponseRole) RoleStartContext {
	t.Helper()
	start, err := newRoleStartContext(role, "session owner test context")
	if err != nil {
		t.Fatal(err)
	}
	return start
}

func sessionBrieferStartContext(t *testing.T, assignmentID implementationstate.AssignmentID) BrieferStartContext {
	t.Helper()
	return BrieferStartContext{assignmentID: assignmentID, start: sessionStartContext(t, ResponseRoleBriefer)}
}

type sessionRuntimeFactory struct {
	mu             sync.Mutex
	runtimes       []*sessionRuntime
	closeThreadErr error
	responses      map[ResponseRole]map[string]any
}

func (factory *sessionRuntimeFactory) Preflight(implementationconfig.RuntimeProfile) error {
	return nil
}

func (factory *sessionRuntimeFactory) Create(context.Context, implementationconfig.RuntimeProfile) (agentruntime.Runtime, error) {
	runtime := &sessionRuntime{closeThreadErr: factory.closeThreadErr, responses: factory.responses}
	factory.mu.Lock()
	factory.runtimes = append(factory.runtimes, runtime)
	factory.mu.Unlock()
	return runtime, nil
}

func (factory *sessionRuntimeFactory) configurations() []agentruntime.ThreadConfig {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	var configurations []agentruntime.ThreadConfig
	for _, runtime := range factory.runtimes {
		configurations = append(configurations, runtime.configurations...)
	}
	return configurations
}

func (factory *sessionRuntimeFactory) turns(session *AgentSession) int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	for _, runtime := range factory.runtimes {
		if runtime == session.runtime {
			return runtime.turnCount
		}
	}
	return 0
}

type sessionRuntime struct {
	configurations []agentruntime.ThreadConfig
	turnCount      int
	closeThreadErr error
	responses      map[ResponseRole]map[string]any
}

func (runtime *sessionRuntime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	runtime.configurations = append(runtime.configurations, config.Clone())
	return len(runtime.configurations), nil
}

func (runtime *sessionRuntime) RunTurn(thread agentruntime.Thread, _ string) (json.RawMessage, error) {
	runtime.turnCount++
	kind := ResponseImplementationReady
	role := ResponseRoleImplementer
	var bootstrap string
	if number, ok := thread.(int); ok && number > 0 && number <= len(runtime.configurations) {
		bootstrap = runtime.configurations[number-1].BootstrapInstructions
		role = roleFromBootstrap(bootstrap)
		switch role {
		case ResponseRoleOrchestrator:
			kind = ResponseProgressReflected
		case ResponseRoleBriefer:
			kind = ResponseBriefReady
		case ResponseRoleTaskReviewer, ResponseRoleFinalReviewer:
			kind = ResponseReviewPassed
		}
	}
	response := responsePayloadMap(kind)
	if configured := runtime.responses[role]; configured != nil {
		response = configured
		kind = ResponseKind(configured["kind"].(string))
	}
	if kind == ResponseBriefReady {
		for _, line := range strings.Split(bootstrap, "\n") {
			for _, field := range strings.Fields(line) {
				if strings.Contains(line, " status=pending ") && strings.HasPrefix(field, "id=") {
					response["task_ids"] = []string{strings.TrimPrefix(field, "id=")}
					break
				}
				if strings.HasPrefix(line, "- id=") && strings.HasPrefix(field, "tasks=") {
					response["task_ids"] = strings.Split(strings.TrimPrefix(field, "tasks="), ",")
					break
				}
			}
			if ids, ok := response["task_ids"].([]string); ok && len(ids) != 0 && ids[0] != "task-1" {
				break
			}
		}
	}
	if kind == ResponseReviewPassed {
		response["message"] = "review passed after restart"
		response["references"] = []string{"durable assignment diff", "required check evidence"}
	}
	payload, _ := json.Marshal(response)
	return payload, nil
}

func (runtime *sessionRuntime) CloseThread(agentruntime.Thread) error { return runtime.closeThreadErr }
func (runtime *sessionRuntime) Interrupt() error                      { return nil }
func (runtime *sessionRuntime) Close() error                          { return nil }

func roleFromBootstrap(instructions string) ResponseRole {
	for _, role := range []ResponseRole{
		ResponseRoleOrchestrator, ResponseRoleBriefer, ResponseRoleImplementer, ResponseRoleTaskReviewer,
		ResponseRoleExplorer, ResponseRoleFinalReviewer,
	} {
		roleInstructions, _ := RoleInstructions(role)
		if len(roleInstructions) > 0 && len(instructions) >= len(roleInstructions) && instructions[:len(roleInstructions)] == roleInstructions {
			return role
		}
	}
	return "unknown"
}
