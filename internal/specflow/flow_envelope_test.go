package specflow

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFlowEnvelopeSchemaIsDeterministicAndCoversAllTerminalResults(t *testing.T) {
	first := FlowEnvelopeSchema()
	if !bytes.Equal(first, FlowEnvelopeSchema()) {
		t.Fatal("flow envelope schema is not deterministic")
	}
	var object map[string]any
	if err := json.Unmarshal(first, &object); err != nil || object == nil {
		t.Fatalf("schema = %s, error = %v", first, err)
	}
	cases := []string{
		`{"status":"NEEDS_INPUT","message":"question"}`,
		`{"status":"READY_TO_WRITE","spec_id":"flow"}`,
		`{"status":"WRITTEN"}`,
		`{"status":"ANSWERED","message":"answer"}`,
		`{"status":"READY_TO_UPDATE"}`,
		`{"status":"UPDATED"}`,
	}
	for _, value := range cases {
		if _, err := DecodeFlowEnvelope([]byte(value)); err != nil {
			t.Fatalf("flow envelope %s: %v", value, err)
		}
	}
}

func TestFlowEnvelopeRejectsInvalidShapes(t *testing.T) {
	for _, value := range []string{
		`{"status":"UNKNOWN"}`,
		`{"status":"WRITTEN","message":"extra"}`,
		`{"status":"NEEDS_INPUT"}`,
		`[]`, `null`, `"text"`, `{"status":"UPDATED"} trailing`,
	} {
		if _, err := DecodeFlowEnvelope([]byte(value)); err == nil {
			t.Fatalf("invalid envelope accepted: %s", value)
		}
	}
}
