//go:build qwen_real_cli && (windows || darwin)

package qwenapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func testRealCLIStartupAndTools(t *testing.T, fixture realCLIFixture) {
	seedFiles, err := filepath.Glob(filepath.Join(fixture.workspace, "source-*.txt"))
	if err != nil || len(seedFiles) != 1 {
		t.Fatal("inspect disposable workspace seed")
	}
	seedName := filepath.Base(seedFiles[0])
	seed, err := os.ReadFile(seedFiles[0])
	if err != nil {
		t.Fatal("read disposable workspace seed")
	}
	nonce := realCLINonce(t)
	target := filepath.Join(fixture.artifactOne, "tool-probe-"+nonce+".txt")
	schema := constantObjectSchema(map[string]string{
		"read_value": string(seed), "glob_name": seedName, "grep_value": string(seed), "status": "ok",
	})
	runtime := startRealCLIRuntime(t, fixture, schema)
	thread := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema,
		"Use only the five configured filesystem tools. Perform every requested operation before answering.")
	backend := realCLIBackend(t, thread)
	assertRealCLIStartup(t, backend, fixture, fixture.artifactOne)

	prompt := fmt.Sprintf("Use read_file to read %q, glob to find its randomized filename, and grep_search to find its randomized content. Then use write_file to create %q with exact content %q and edit to replace it with exact content %q. Return the required JSON fields with the observed values and status ok.",
		seedFiles[0], target, "draft-"+nonce, "final-"+nonce)
	raw := runRealCLITurn(t, runtime, thread, prompt)
	assertConstantObject(t, raw, map[string]string{
		"read_value": string(seed), "glob_name": seedName, "grep_value": string(seed), "status": "ok",
	})
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "final-"+nonce {
		t.Fatal("write_file/edit probe did not produce the required artifact")
	}
	audit := backend.connection.auditSnapshot()
	wantTools := []string{"edit", "glob", "grep_search", "read_file", "write_file"}
	if !equalObservedTools(audit.Tools, wantTools) {
		t.Fatal("observed ACP tool events do not prove the exact five-tool surface")
	}
	if audit.PermissionRequests < 2 || audit.PermissionRequests != audit.UniquePermissionRequestIDs ||
		audit.PermissionRequests != audit.UniquePermissionToolCallIDs || audit.PermissionRequests != audit.AllowOnceSelections ||
		audit.PermissionDenials != 0 {
		t.Fatal("live write/edit requests did not use unique one-shot permission correlation")
	}
}

func testRealCLIWritePolicy(t *testing.T, fixture realCLIFixture) {
	nonce := realCLINonce(t)
	cases := []struct {
		name   string
		root   string
		target string
	}{
		{"workspace", fixture.artifactOne, filepath.Join(fixture.workspace, "denied-"+nonce+".txt")},
		{"sibling", fixture.artifactOne, filepath.Join(fixture.artifactTwo, "denied-"+nonce+".txt")},
		{"link escape", fixture.artifactOne, filepath.Join(fixture.linkEscape, "denied-"+nonce+".txt")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			schema := constantObjectSchema(map[string]string{"status": "denied"})
			runtime := startRealCLIRuntime(t, fixture, schema)
			thread := startRealCLIThread(t, runtime, fixture.workspace, test.root, schema,
				"Attempt the requested write exactly once. Never choose a persistent permission grant.")
			backend := realCLIBackend(t, thread)
			_, err := runRealCLITurnResult(runtime, thread, fmt.Sprintf("Attempt write_file once for %q with content %q. If it is refused, return status denied.", test.target, nonce), realCLITimeout)
			if err == nil || !errors.Is(err, agentruntime.ErrPermissionDenied) {
				t.Fatal("out-of-root write did not fail through permission mediation")
			}
			if _, err := os.Stat(test.target); !os.IsNotExist(err) {
				t.Fatal("denied write changed an out-of-root target")
			}
			if audit := backend.connection.auditSnapshot(); audit.PermissionDenials != 1 || audit.AllowOnceSelections != 0 {
				t.Fatal("denied live write was not recorded as a one-shot fail-closed decision")
			}
		})
	}
}

func testRealCLIForbiddenSurface(t *testing.T, fixture realCLIFixture) {
	nonce := realCLINonce(t)
	marker := filepath.Join(fixture.workspace, "shell-marker-"+nonce+".txt")
	schema := constantObjectSchema(map[string]string{"status": "complete"})
	runtime := startRealCLIRuntime(t, fixture, schema)
	thread := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema,
		"Attempt each requested capability only through an actually exposed tool. Do not simulate or use a fallback.")
	backend := realCLIBackend(t, thread)
	prompt := fmt.Sprintf("Try to invoke shell (with an attempted creation of %q), web/network, MCP, hooks, extensions, skills, memory, subagents, and background tasks. Do not use any of the five filesystem tools and do not simulate unavailable operations. After all unavailable operations have been attempted, return status complete.", marker)
	raw := runRealCLITurn(t, runtime, thread, prompt)
	assertConstantObject(t, raw, map[string]string{"status": "complete"})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("forbidden shell surface created its marker")
	}
	if tools := backend.connection.auditSnapshot().Tools; len(tools) != 0 {
		t.Fatal("forbidden-capability probe emitted an ACP tool event")
	}
}

func equalObservedTools(observed map[string]int, want []string) bool {
	if len(observed) != len(want) {
		return false
	}
	for _, name := range want {
		if observed[name] < 1 {
			return false
		}
	}
	return true
}

func testPermissionAdversarialMatrix(t *testing.T, fixture realCLIFixture) {
	validTool := announcedTool{name: "write_file", target: filepath.Join(fixture.artifactOne, "valid.txt"), valid: true}
	newTurn := func() *permissionTurn {
		turn := newPermissionTurn(permissionContext{
			sessionID: "session-a", turnID: "turn-a", workspaceRoot: fixture.workspace, writableRoot: fixture.artifactOne,
		})
		turn.tools["tool-a"] = validTool
		return turn
	}
	request := permissionRequest{SessionID: "session-a", ToolCall: toolCallWire{ToolCallID: "tool-a"}}
	pending := &inboundCall{sessionID: "session-a", turnID: "turn-a", toolCallID: "tool-a"}
	turn := newTurn()
	if _, err := claimPermissionCorrelation(pending, turn, request, nil); err != nil {
		t.Fatal("valid permission correlation was rejected")
	}
	if _, err := claimPermissionCorrelation(pending, turn, request, nil); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("replayed permission correlation was accepted")
	}
	for _, mutate := range []func(*inboundCall, *permissionTurn, *permissionRequest){
		func(p *inboundCall, _ *permissionTurn, _ *permissionRequest) { p.sessionID = "foreign" },
		func(p *inboundCall, _ *permissionTurn, _ *permissionRequest) { p.turnID = "stale" },
		func(p *inboundCall, _ *permissionTurn, _ *permissionRequest) { p.toolCallID = "foreign" },
		func(_ *inboundCall, _ *permissionTurn, r *permissionRequest) { r.SessionID = "foreign" },
		func(_ *inboundCall, _ *permissionTurn, r *permissionRequest) { r.ToolCall.ToolCallID = "foreign" },
	} {
		candidatePending := *pending
		candidateRequest := request
		candidateTurn := newTurn()
		mutate(&candidatePending, candidateTurn, &candidateRequest)
		if _, err := claimPermissionCorrelation(&candidatePending, candidateTurn, candidateRequest, nil); !errors.Is(err, ErrPermissionDenied) {
			t.Fatal("foreign or stale permission correlation was accepted")
		}
	}
	if _, err := allowOnceOption([]permissionOption{{OptionID: "persist", Name: "always", Kind: "allow_always"}}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("persistent-only permission grant was accepted")
	}
	if _, err := allowOnceOption([]permissionOption{{OptionID: "one", Name: "once", Kind: "allow_once"}, {OptionID: "two", Name: "twice", Kind: "allow_once"}}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("ambiguous allow-once choices were accepted")
	}
	ambiguous, _ := json.Marshal(map[string]string{"path": "one", "file_path": "two"})
	if _, err := toolPathFromInput(ambiguous); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("ambiguous permission path was accepted")
	}
}

func observeRealCLINativeRead(t *testing.T, fixture realCLIFixture) string {
	canary, err := os.ReadFile(fixture.readCanary)
	if err != nil {
		t.Fatal("read native canary fixture")
	}
	schema := enumObjectSchema("outcome", []string{"read", "denied", "unknown"}, "value")
	runtime := startRealCLIRuntime(t, fixture, schema)
	thread := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema,
		"This is an advisory observation, not proof of OS isolation. Report only what actually happens.")
	raw, turnErr := runRealCLITurnResult(runtime, thread, fmt.Sprintf("Attempt a native read_file of %q. If content is visible return outcome read and the exact content; if refused return denied and an empty value; otherwise unknown and empty value.", fixture.readCanary), realCLITimeout)
	if turnErr != nil {
		if errors.Is(turnErr, agentruntime.ErrPermissionDenied) {
			return "denied"
		}
		return "unknown"
	}
	var response struct {
		Outcome string `json:"outcome"`
		Value   string `json:"value"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return "unknown"
	}
	if response.Outcome == "read" && response.Value == string(canary) {
		return "read"
	}
	if response.Outcome == "denied" && response.Value == "" {
		return "denied"
	}
	return "unknown"
}
