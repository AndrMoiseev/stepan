package codexapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testSchema = json.RawMessage(`{"type":"object"}`)

func TestThreadAPIReusesConnectionForThreadsAndTurns(t *testing.T) {
	root := canonicalTempDir(t)
	connection, server, serverErr := threadTestConnection(t)
	go func() {
		for threadIndex, turns := range []int{2, 1} {
			request, err := server.Read()
			if err != nil || request.Method != "thread/start" {
				serverErr <- fmt.Errorf("thread/start: %+v, %v", request, err)
				return
			}
			var params struct {
				CWD string `json:"cwd"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.CWD != root {
				serverErr <- fmt.Errorf("thread/start params = %s", request.Params)
				return
			}
			threadID := fmt.Sprintf("thread-%d", threadIndex+1)
			if err := server.SendResult(request.ID, map[string]any{"thread": map[string]string{"id": threadID}}); err != nil {
				serverErr <- err
				return
			}
			for turnIndex := range turns {
				request, err = server.Read()
				if err != nil || request.Method != "turn/start" {
					serverErr <- fmt.Errorf("turn/start: %+v, %v", request, err)
					return
				}
				var params struct {
					ThreadID string              `json:"threadId"`
					Input    []map[string]string `json:"input"`
					CWD      string              `json:"cwd"`
					Approval string              `json:"approvalPolicy"`
					Sandbox  struct {
						Type    string `json:"type"`
						Network bool   `json:"networkAccess"`
					} `json:"sandboxPolicy"`
					Schema map[string]any `json:"outputSchema"`
				}
				if json.Unmarshal(request.Params, &params) != nil || params.ThreadID != threadID || len(params.Input) != 1 || params.Input[0]["text"] == "" || params.CWD != root || params.Approval != "on-request" || params.Sandbox.Type != "readOnly" || params.Sandbox.Network || params.Schema["type"] != "object" {
					serverErr <- fmt.Errorf("turn/start params = %s", request.Params)
					return
				}
				turnID := fmt.Sprintf("turn-%d-%d", threadIndex+1, turnIndex+1)
				if err := server.SendResult(request.ID, map[string]any{"turn": map[string]string{"id": turnID}}); err != nil {
					serverErr <- err
					return
				}
				output := fmt.Sprintf(`{"thread":%d,"turn":%d}`, threadIndex+1, turnIndex+1)
				if err := sendCompletedTurn(server, threadID, turnID, "item-1", output, output); err != nil {
					serverErr <- err
					return
				}
			}
		}
		serverErr <- nil
	}()

	for threadIndex, turns := range []int{2, 1} {
		thread, err := connection.StartThread(root)
		if err != nil || thread.ID != fmt.Sprintf("thread-%d", threadIndex+1) {
			t.Fatalf("thread = %+v, %v", thread, err)
		}
		for turnIndex := range turns {
			output, err := connection.RunTurn(thread, "prompt", TurnOptions{OutputSchema: testSchema})
			want := fmt.Sprintf(`{"thread":%d,"turn":%d}`, threadIndex+1, turnIndex+1)
			if err != nil || string(output) != want {
				t.Fatalf("turn output = %s, %v", output, err)
			}
		}
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("thread state was persisted: %v, %v", entries, err)
	}
}

func TestRunTurnRejectsConcurrentTurn(t *testing.T) {
	root := canonicalTempDir(t)
	connection, server, serverErr := threadTestConnection(t)
	received := make(chan struct{})
	release := make(chan struct{})
	go func() {
		threadRequest, err := server.Read()
		if err != nil {
			serverErr <- err
			return
		}
		if err := server.SendResult(threadRequest.ID, map[string]any{"thread": map[string]string{"id": "thread"}}); err != nil {
			serverErr <- err
			return
		}
		turnRequest, err := server.Read()
		if err != nil {
			serverErr <- err
			return
		}
		if err := server.SendResult(turnRequest.ID, map[string]any{"turn": map[string]string{"id": "turn"}}); err != nil {
			serverErr <- err
			return
		}
		close(received)
		<-release
		serverErr <- sendCompletedTurn(server, "thread", "turn", "item", `{"ok":true}`, `{"ok":true}`)
	}()
	thread, err := connection.StartThread(root)
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		_, err := connection.RunTurn(thread, "first", TurnOptions{OutputSchema: testSchema})
		first <- err
	}()
	<-received
	if _, err := connection.RunTurn(thread, "second", TurnOptions{OutputSchema: testSchema}); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("concurrent turn error = %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestRunTurnReturnsFailedTurnMessageWithoutClosingConnection(t *testing.T) {
	root := canonicalTempDir(t)
	connection, server, serverErr := threadTestConnection(t)
	go func() {
		request, err := server.Read()
		if err == nil {
			err = server.SendResult(request.ID, map[string]any{"thread": map[string]string{"id": "thread"}})
		}
		if err == nil {
			request, err = server.Read()
		}
		if err == nil {
			err = server.SendResult(request.ID, map[string]any{"turn": map[string]string{"id": "turn"}})
		}
		if err == nil {
			err = server.SendNotification("turn/completed", map[string]any{
				"threadId": "thread",
				"turn": map[string]any{
					"id": "turn", "status": "failed", "items": []any{},
					"error": map[string]string{"message": "quota exhausted"},
				},
			})
		}
		serverErr <- err
	}()

	thread, err := connection.StartThread(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.RunTurn(thread, "prompt", TurnOptions{OutputSchema: testSchema}); err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("failed turn error = %v", err)
	}
	if err := connection.Err(); err != nil {
		t.Fatalf("connection closed after failed turn: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestCompletedTurnValidation(t *testing.T) {
	final := terminalItem{ID: "final", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `{"ok":true}`}
	completed := map[string]terminalItem{"final": final}
	terminal := func(status string, items ...any) json.RawMessage {
		return mustJSON(t, map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": status, "items": items}})
	}
	for _, test := range []struct {
		name      string
		raw       json.RawMessage
		completed map[string]terminalItem
	}{
		{"invalid notification", json.RawMessage(`{`), completed},
		{"failed status", terminal("failed", final), completed},
		{"missing final", terminal("completed", terminalItem{ID: "tool", Type: "commandExecution"}), map[string]terminalItem{"tool": {ID: "tool", Type: "commandExecution"}}},
		{"duplicate finals", terminal("completed", final, terminalItem{ID: "other", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `{}`}), map[string]terminalItem{"final": final, "other": {ID: "other", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `{}`}}},
		{"contradictory final", terminal("completed", terminalItem{ID: "final", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `{"ok":false}`}), completed},
		{"non-object output", terminal("completed", terminalItem{ID: "final", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `[]`}), map[string]terminalItem{"final": {ID: "final", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `[]`}}},
		{"invalid output", terminal("completed", terminalItem{ID: "final", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `{`}), map[string]terminalItem{"final": {ID: "final", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `{`}}},
		{"trailing output", terminal("completed", terminalItem{ID: "final", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `{} {}`}), map[string]terminalItem{"final": {ID: "final", Type: "agentMessage", Phase: phasePtr("final_answer"), Text: `{} {}`}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeCompletedTurn(test.raw, test.completed); err == nil {
				t.Fatal("expected malformed terminal error")
			}
		})
	}
	t.Run("nullable phase compatibility", func(t *testing.T) {
		item := terminalItem{ID: "final", Type: "agentMessage", Text: `{"ok":true}`}
		output, err := decodeCompletedTurn(terminal("completed", item), map[string]terminalItem{"final": item})
		if err != nil || string(output) != item.Text {
			t.Fatalf("output = %s, %v", output, err)
		}
	})
	t.Run("terminal item without completed event", func(t *testing.T) {
		output, err := decodeCompletedTurn(terminal("completed", final), nil)
		if err != nil || string(output) != final.Text {
			t.Fatalf("output = %s, %v", output, err)
		}
	})
}

func TestTurnCorrelationRejectsWrongAndIncompleteIDs(t *testing.T) {
	run := &turnRun{threadID: "thread", turnID: "turn"}
	for _, test := range []struct {
		name, method string
		params       any
	}{
		{"wrong thread", "turn/completed", map[string]any{"threadId": "other", "turn": map[string]string{"id": "turn"}}},
		{"wrong turn", "item/completed", map[string]any{"threadId": "thread", "turnId": "other", "item": map[string]string{"id": "item"}}},
		{"missing item", "item/completed", map[string]any{"threadId": "thread", "turnId": "turn"}},
		{"approval mismatch", "item/fileChange/requestApproval", map[string]string{"threadId": "thread", "turnId": "other", "itemId": "item"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := run.correlate(Message{Method: test.method, Params: mustJSON(t, test.params)}); err == nil {
				t.Fatal("expected correlation error")
			}
		})
	}
}

func threadTestConnection(t *testing.T) (*Connection, *Transport, chan error) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	t.Cleanup(func() { _ = clientSide.Close(); _ = serverSide.Close() })
	server := NewTransport(serverSide, serverSide)
	serverErr := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		initialize, err := server.Read()
		if err == nil {
			err = server.SendResult(initialize.ID, map[string]string{"codexHome": "test", "platformFamily": "windows", "platformOs": "windows", "userAgent": "test/1"})
		}
		if err == nil {
			var initialized Message
			initialized, err = server.Read()
			if err == nil && (initialized.Kind != Notification || initialized.Method != "initialized") {
				err = errors.New("missing initialized notification")
			}
		}
		if err != nil {
			serverErr <- err
		}
		close(ready)
	}()
	connection, err := NewConnection(NewTransport(clientSide, clientSide), Handler{})
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	return connection, server, serverErr
}

func sendCompletedTurn(server *Transport, threadID, turnID, itemID, itemText, turnText string) error {
	item := map[string]any{"id": itemID, "type": "agentMessage", "phase": "final_answer", "text": itemText}
	if err := server.SendNotification("item/completed", map[string]any{"threadId": threadID, "turnId": turnID, "item": item}); err != nil {
		return err
	}
	item["text"] = turnText
	return server.SendNotification("turn/completed", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "completed", "items": []any{item}}})
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func phasePtr(value string) *string { return &value }

func TestStartThreadCanonicalizesCWD(t *testing.T) {
	root := canonicalTempDir(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	connection, server, serverErr := threadTestConnection(t)
	go func() {
		request, err := server.Read()
		if err == nil && !strings.Contains(string(request.Params), filepath.ToSlash(nested)) && !strings.Contains(string(request.Params), strings.ReplaceAll(nested, `\`, `\\`)) {
			err = fmt.Errorf("cwd was not sent: %s", request.Params)
		}
		if err == nil {
			err = server.SendResult(request.ID, map[string]any{"thread": map[string]string{"id": "thread"}})
		}
		serverErr <- err
	}()
	if _, err := connection.StartThread(filepath.Join(nested, ".")); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
