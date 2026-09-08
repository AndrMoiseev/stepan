package nessyapp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestResponseAssemblerCollectsOnlyOrderedTextFromOneMessage(t *testing.T) {
	assembler := newResponseAssembler()
	identity := turnIdentity{sessionID: "session", promptID: "i:7"}
	if err := assembler.bind(identity); err != nil {
		t.Fatal(err)
	}
	updates := []map[string]any{
		{"sessionUpdate": "agent_message_chunk", "messageId": "answer", "content": map[string]any{"type": "text", "text": `{"answer":"`}},
		{"sessionUpdate": "agent_thought_chunk", "messageId": "thought", "content": map[string]any{"type": "text", "text": "private"}},
		{"sessionUpdate": "tool_call", "toolCallId": "read", "kind": "read", "status": "completed"},
		{"sessionUpdate": "usage_update", "used": 1, "size": 100},
		{"sessionUpdate": "agent_message_chunk", "messageId": "answer", "content": map[string]any{"type": "text", "text": `ok"}`}},
	}
	for _, update := range updates {
		raw, err := json.Marshal(update)
		if err != nil {
			t.Fatal(err)
		}
		if err := assembler.observe(identity, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := assembler.terminal(identity); err != nil {
		t.Fatal(err)
	}
	if got := string(assembler.output()); got != `{"answer":"ok"}` {
		t.Fatalf("assembled output = %q", got)
	}
}

func TestResponseAssemblerRejectsAmbiguousUnknownAndLateContent(t *testing.T) {
	tests := []struct {
		name string
		run  func(*responseAssembler, turnIdentity) error
	}{
		{name: "foreign session", run: func(a *responseAssembler, id turnIdentity) error {
			id.sessionID = "foreign"
			return a.observe(id, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}`))
		}},
		{name: "foreign prompt", run: func(a *responseAssembler, id turnIdentity) error {
			id.promptID = "i:8"
			return a.observe(id, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}`))
		}},
		{name: "unknown content", run: func(a *responseAssembler, id turnIdentity) error {
			return a.observe(id, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"future","text":"x"}}`))
		}},
		{name: "missing text", run: func(a *responseAssembler, id turnIdentity) error {
			return a.observe(id, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text"}}`))
		}},
		{name: "second identified message", run: func(a *responseAssembler, id turnIdentity) error {
			if err := a.observe(id, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","messageId":"one","content":{"type":"text","text":"x"}}`)); err != nil {
				return err
			}
			return a.observe(id, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","messageId":"two","content":{"type":"text","text":"y"}}`))
		}},
		{name: "identifier appears late", run: func(a *responseAssembler, id turnIdentity) error {
			if err := a.observe(id, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}`)); err != nil {
				return err
			}
			return a.observe(id, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","messageId":"one","content":{"type":"text","text":"y"}}`))
		}},
		{name: "duplicate terminal", run: func(a *responseAssembler, id turnIdentity) error {
			if err := a.terminal(id); err != nil {
				return err
			}
			return a.terminal(id)
		}},
		{name: "late content", run: func(a *responseAssembler, id turnIdentity) error {
			if err := a.terminal(id); err != nil {
				return err
			}
			return a.observe(id, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}`))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assembler := newResponseAssembler()
			identity := turnIdentity{sessionID: "session", promptID: "i:7"}
			if err := assembler.bind(identity); err != nil {
				t.Fatal(err)
			}
			err := test.run(assembler, identity)
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "\"text\"") || strings.Contains(err.Error(), "future") {
				t.Fatalf("diagnostic leaked content: %v", err)
			}
		})
	}
}

func TestValidateCandidateRequiresExactJSONObjectAndSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)
	tests := []struct {
		name      string
		candidate string
		valid     bool
		schemaErr bool
	}{
		{name: "valid", candidate: " \n" + `{"answer":"ok"}` + "\t", valid: true},
		{name: "markdown", candidate: "```json\n{\"answer\":\"ok\"}\n```"},
		{name: "prefix", candidate: `result: {"answer":"ok"}`},
		{name: "suffix", candidate: `{"answer":"ok"} done`},
		{name: "second object", candidate: `{"answer":"one"}{"answer":"two"}`},
		{name: "array", candidate: `[{"answer":"ok"}]`},
		{name: "duplicate property", candidate: `{"answer":"one","answer":"two"}`},
		{name: "malformed", candidate: `{"answer":`},
		{name: "schema violation", candidate: `{"answer":7}`, schemaErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := validateCandidate(schema, json.RawMessage(test.candidate))
			if test.valid {
				if err != nil || string(output) != `{"answer":"ok"}` {
					t.Fatalf("validateCandidate = %q, %v", output, err)
				}
				return
			}
			var candidate candidateError
			if !errors.As(err, &candidate) || candidate.schema != test.schemaErr || output != nil {
				t.Fatalf("validateCandidate = %q, %#v", output, err)
			}
		})
	}
}
