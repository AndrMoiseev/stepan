package qwenapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

const turnTestSchema = `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`

func TestTurnRunnerRepairsTwiceAndAcceptsThirdResponse(t *testing.T) {
	runner, connection, server, raw := establishTurnRunner(t, json.RawMessage(turnTestSchema), "role instructions")
	defer raw.Close()
	defer connection.Close()

	result := make(chan struct {
		output json.RawMessage
		err    error
	}, 1)
	go func() {
		output, err := runner.run("produce an answer")
		result <- struct {
			output json.RawMessage
			err    error
		}{output, err}
	}()

	for attempt, candidate := range []string{"not json", `{"answer":7}`, `{"answer":"third"}`} {
		request, id := readSessionPromptMessage(t, server)
		if attempt > 0 {
			text := request.Prompt[0].Text
			if !strings.Contains(text, "previous response could not be accepted") || !strings.Contains(text, turnTestSchema) {
				t.Fatalf("repair prompt %d = %q", attempt, text)
			}
			if strings.Contains(text, "not json") {
				t.Fatalf("repair prompt leaked raw response: %q", text)
			}
		}
		sendAssistantChunk(t, server, "s", "answer", candidate[:len(candidate)/2])
		if err := server.sendNotification("session/update", map[string]any{
			"sessionId": "s", "update": map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "ignore thought"}},
		}); err != nil {
			t.Fatal(err)
		}
		sendAssistantChunk(t, server, "s", "answer", candidate[len(candidate)/2:])
		if err := server.sendResult(id, map[string]string{"stopReason": "end_turn"}); err != nil {
			t.Fatal(err)
		}
	}

	got := <-result
	if got.err != nil || string(got.output) != `{"answer":"third"}` {
		t.Fatalf("run = %q, %v", got.output, got.err)
	}
}

func TestTurnRunnerExhaustsAtThreeResponsesWithoutFourthPrompt(t *testing.T) {
	runner, connection, server, raw := establishTurnRunner(t, json.RawMessage(turnTestSchema), "role")
	defer raw.Close()
	defer connection.Close()
	errOut := make(chan error, 1)
	go func() {
		_, err := runner.run("produce an answer")
		errOut <- err
	}()
	for range agentruntime.DefaultRetryLimit {
		request, id := readSessionPromptMessage(t, server)
		if request.SessionID != "s" {
			t.Fatalf("prompt session = %q", request.SessionID)
		}
		sendAssistantChunk(t, server, "s", "answer", "invalid")
		if err := server.sendResult(id, map[string]string{"stopReason": "end_turn"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-errOut; !errors.Is(err, ErrProtocol) || !errors.Is(err, ErrRepairExhausted) {
		t.Fatalf("exhaustion error = %v", err)
	}
	assertNoPrompt(t, raw, server)
	if connection.Err() != nil {
		t.Fatalf("format exhaustion corrupted connection: %v", connection.Err())
	}
}

func TestTurnRunnerClonesBootstrapSchemaAndSessionContext(t *testing.T) {
	schema := json.RawMessage("{\n  \"type\": \"object\", \"properties\": {\"answer\": {\"type\": \"string\"}}, \"required\": [\"answer\"], \"additionalProperties\": false\n}")
	originalSchema := append(json.RawMessage(nil), schema...)
	role := "immutable role instructions"
	runner, connection, server, raw := establishTurnRunner(t, schema, role)
	defer raw.Close()
	defer connection.Close()
	for index := range schema {
		schema[index] = 'x'
	}

	firstResult := make(chan error, 1)
	go func() { _, err := runner.run("first user request"); firstResult <- err }()
	first, firstID := readSessionPromptMessage(t, server)
	firstText := first.Prompt[0].Text
	encodedWorkspace, _ := json.Marshal(runner.context.Workspace)
	for _, expected := range []string{role, string(originalSchema), string(encodedWorkspace), "first user request", "readableRoots", "writableRoots"} {
		if !strings.Contains(firstText, expected) {
			t.Fatalf("first prompt lacks %q:\n%s", expected, firstText)
		}
	}
	sendAssistantChunk(t, server, "s", "answer", "invalid credential-body-never-copy")
	if err := server.sendResult(firstID, map[string]string{"stopReason": "end_turn"}); err != nil {
		t.Fatal(err)
	}
	repair, repairID := readSessionPromptMessage(t, server)
	if !strings.Contains(repair.Prompt[0].Text, string(originalSchema)) || strings.Contains(repair.Prompt[0].Text, "credential-body-never-copy") {
		t.Fatalf("unsafe or mutated repair prompt: %s", repair.Prompt[0].Text)
	}
	sendAssistantChunk(t, server, "s", "answer", `{"answer":"fixed"}`)
	if err := server.sendResult(repairID, map[string]string{"stopReason": "end_turn"}); err != nil {
		t.Fatal(err)
	}
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}

	secondResult := make(chan error, 1)
	go func() { _, err := runner.run("second user request"); secondResult <- err }()
	second, secondID := readSessionPromptMessage(t, server)
	secondText := second.Prompt[0].Text
	if secondText != "second user request" || strings.Contains(secondText, role) || strings.Contains(secondText, string(originalSchema)) {
		t.Fatalf("subsequent ordinary prompt repeated bootstrap: %q", secondText)
	}
	sendAssistantChunk(t, server, "s", "answer", `{"answer":"again"}`)
	if err := server.sendResult(secondID, map[string]string{"stopReason": "end_turn"}); err != nil {
		t.Fatal(err)
	}
	if err := <-secondResult; err != nil {
		t.Fatal(err)
	}

	for _, required := range []string{"exactly one UTF-8 JSON object", "Markdown fences", "prefixes", "suffixes", "Stepan schema"} {
		if !strings.Contains(JSONContract, required) {
			t.Fatalf("process JSON contract lacks %q: %s", required, JSONContract)
		}
	}
}

func TestTurnRunnerDoesNotRepairNonFormatFailures(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *transport, net.Conn, requestID)
		want error
	}{
		{name: "cancelled", want: agentruntime.ErrTurnInterrupted, run: func(t *testing.T, server *transport, _ net.Conn, id requestID) {
			if err := server.sendResult(id, map[string]string{"stopReason": "cancelled"}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "transport corruption", want: ErrProtocol, run: func(t *testing.T, _ *transport, raw net.Conn, _ requestID) {
			if _, err := raw.Write([]byte{0xff, '\n'}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "permission denial", want: agentruntime.ErrPermissionDenied, run: func(t *testing.T, server *transport, _ net.Conn, id requestID) {
			outside := filepath.Join(t.TempDir(), "outside.md")
			announceTool(t, server, "denied", "write_file", map[string]any{"file_path": outside}, outside)
			if outcome := requestPermission(t, server, "denied-request", "denied", "write_file", map[string]any{"file_path": outside}, outside, standardPermissionOptions()); outcome != "cancelled" {
				t.Fatalf("permission outcome = %q", outcome)
			}
			sendAssistantChunk(t, server, "s", "answer", "invalid")
			if err := server.sendResult(id, map[string]string{"stopReason": "end_turn"}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, connection, server, raw := establishTurnRunner(t, json.RawMessage(turnTestSchema), "role")
			defer raw.Close()
			defer connection.Close()
			errOut := make(chan error, 1)
			go func() { _, err := runner.run("prompt"); errOut <- err }()
			_, id := readSessionPromptMessage(t, server)
			test.run(t, server, raw, id)
			if err := <-errOut; !errors.Is(err, test.want) {
				t.Fatalf("turn error = %v, want %v", err, test.want)
			}
			if test.name != "transport corruption" {
				assertNoPrompt(t, raw, server)
			}
		})
	}
}

func TestTurnRunnerProtocolContentFailureDoesNotRepair(t *testing.T) {
	runner, _, server, raw := establishTurnRunner(t, json.RawMessage(turnTestSchema), "role")
	defer raw.Close()
	errOut := make(chan error, 1)
	go func() { _, err := runner.run("prompt"); errOut <- err }()
	readSessionPrompt(t, server)
	if err := server.sendNotification("session/update", map[string]any{
		"sessionId": "s", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "unknown", "text": "credential-body"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-errOut; !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), "credential-body") {
		t.Fatalf("protocol content error = %v", err)
	}
}

func TestLateBufferedChunkCannotBecomeRepairResponse(t *testing.T) {
	runner, connection, server, raw := establishTurnRunner(t, json.RawMessage(turnTestSchema), "role")
	defer raw.Close()
	errOut := make(chan struct {
		output json.RawMessage
		err    error
	}, 1)
	go func() {
		output, err := runner.run("prompt")
		errOut <- struct {
			output json.RawMessage
			err    error
		}{output, err}
	}()
	_, id := readSessionPromptMessage(t, server)
	sendAssistantChunk(t, server, "s", "initial", "invalid")
	terminalAndLate := fmt.Sprintf(
		"{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"stopReason\":\"end_turn\"}}\n"+
			"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"s\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"messageId\":\"late\",\"content\":{\"type\":\"text\",\"text\":\"{\\\"answer\\\":\\\"must-not-be-repair\\\"}\"}}}}\n",
		id.raw,
	)
	if _, err := raw.Write([]byte(terminalAndLate)); err != nil {
		t.Fatal(err)
	}
	got := <-errOut
	if got.output != nil || !errors.Is(got.err, ErrProtocol) || !errors.Is(got.err, ErrConnectionClosed) {
		t.Fatalf("late buffered result = %s, %v", got.output, got.err)
	}
	if connection.Err() == nil || !strings.Contains(connection.Err().Error(), "late assistant content") {
		t.Fatalf("connection error = %v", connection.Err())
	}
	if unexpected, err := server.read(); err == nil {
		t.Fatalf("late content triggered an unsafe repair prompt: %+v", unexpected)
	}
}

func TestProcessCarriesExactContractAndImmutableSessionPrompts(t *testing.T) {
	workspace := makeGitRoot(t)
	artifact := t.TempDir()
	observationPath := filepath.Join(t.TempDir(), "structured-observation.json")
	t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
	t.Setenv("STEPAN_QWEN_ACP_CASE", "structured-turn")
	t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", observationPath)
	process := NewProcess(Config{
		Executable:   testExecutableName(t),
		Workspace:    workspace,
		JSONContract: JSONContract,
	}, artifact)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	connection, err := OpenConnection(process)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	schema := json.RawMessage(turnTestSchema)
	runner, err := newTurnRunner(connection, agentruntime.ThreadConfig{
		BootstrapInstructions: "process integration role",
		OutputSchema:          schema,
		Workspace:             process.WorkspaceRoot(),
		ArtifactRoot:          process.WritableRoot(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range schema {
		schema[index] = 'x'
	}
	first, err := runner.run("first process request")
	if err != nil || string(first) != `{"answer":"repaired"}` {
		t.Fatalf("first process turn = %s, %v", first, err)
	}
	second, err := runner.run("second process request")
	if err != nil || string(second) != `{"answer":"next"}` {
		t.Fatalf("second process turn = %s, %v", second, err)
	}

	var observation acpObservation
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(observationPath)
		if readErr == nil && json.Unmarshal(data, &observation) == nil && len(observation.Prompts) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(observation.Prompts) != 3 {
		t.Fatalf("captured prompts = %#v", observation.Prompts)
	}
	contractCount := 0
	for index, argument := range observation.Args {
		if argument == "--json-schema" {
			t.Fatalf("native schema flag reached process argv: %#v", observation.Args)
		}
		if argument == "--append-system-prompt" {
			contractCount++
			if index+1 >= len(observation.Args) || observation.Args[index+1] != JSONContract {
				t.Fatalf("process JSON contract = %#v", observation.Args)
			}
		}
	}
	if contractCount != 1 {
		t.Fatalf("process JSON contract occurrences = %d in %#v", contractCount, observation.Args)
	}
	encodedWorkspace, _ := json.Marshal(process.WorkspaceRoot())
	encodedArtifact, _ := json.Marshal(process.WritableRoot())
	if !strings.Contains(observation.Prompts[0], "process integration role") || !strings.Contains(observation.Prompts[0], turnTestSchema) ||
		!strings.Contains(observation.Prompts[0], string(encodedWorkspace)) || !strings.Contains(observation.Prompts[0], string(encodedArtifact)) {
		t.Fatalf("first process prompt lacks immutable bootstrap context: %q", observation.Prompts[0])
	}
	if !strings.Contains(observation.Prompts[1], turnTestSchema) || strings.Contains(observation.Prompts[1], "invalid process response") {
		t.Fatalf("process repair prompt is unsafe or missing schema: %q", observation.Prompts[1])
	}
	if observation.Prompts[2] != "second process request" {
		t.Fatalf("later process prompt repeated bootstrap: %q", observation.Prompts[2])
	}
}

func establishTurnRunner(t *testing.T, schema json.RawMessage, role string) (*turnRunner, *Connection, *transport, net.Conn) {
	t.Helper()
	connection, _, server, raw, err := establishTestConnection(t, validInitialize(), map[string]any{"sessionId": "s"}, connectionHandler{})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	artifact := t.TempDir()
	connection.configureFilePolicy(filepath.Clean(workspace), filepath.Clean(artifact))
	runner, err := newTurnRunner(connection, agentruntime.ThreadConfig{
		BootstrapInstructions: role,
		OutputSchema:          schema,
		Workspace:             filepath.Clean(workspace),
		ArtifactRoot:          filepath.Clean(artifact),
	})
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	return runner, connection, server, raw
}

func readSessionPrompt(t *testing.T, server *transport) sessionPromptParams {
	t.Helper()
	params, _ := readSessionPromptMessage(t, server)
	return params
}

func readSessionPromptMessage(t *testing.T, server *transport) (sessionPromptParams, requestID) {
	t.Helper()
	received, err := server.read()
	if err != nil || received.kind != requestMessage || received.method != "session/prompt" {
		t.Fatalf("session prompt = %+v, %v", received, err)
	}
	var params sessionPromptParams
	if decodeResult(received.params, &params) != nil || params.SessionID == "" || len(params.Prompt) != 1 || params.Prompt[0].Type != "text" {
		t.Fatalf("session prompt params = %s", received.params)
	}
	return params, received.id
}

func sendAssistantChunk(t *testing.T, server *transport, sessionID, messageID, text string) {
	t.Helper()
	update := map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}
	if messageID != "" {
		update["messageId"] = messageID
	}
	if err := server.sendNotification("session/update", map[string]any{"sessionId": sessionID, "update": update}); err != nil {
		t.Fatal(err)
	}
}

func assertNoPrompt(t *testing.T, raw net.Conn, server *transport) {
	t.Helper()
	if err := raw.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := server.read(); err == nil {
		t.Fatalf("unexpected fourth/repair prompt: %+v", unexpected)
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("checking absent prompt: %v", err)
	}
	_ = raw.SetReadDeadline(time.Time{})
}
