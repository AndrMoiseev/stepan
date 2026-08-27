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
		`{"status":"WRITTEN","feature_id":"flow"}`,
		`{"status":"ANSWERED","message":"answer"}`,
		`{"status":"NEEDS_INPUT","message":"question"}`,
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
		`{"status":"WRITTEN","feature_id":"flow","message":"extra"}`,
		`{"status":"WRITTEN"}`,
		`{"status":"NEEDS_INPUT"}`,
		`[]`, `null`, `"text"`, `{"status":"UPDATED"} trailing`,
	} {
		if _, err := DecodeFlowEnvelope([]byte(value)); err == nil {
			t.Fatalf("invalid envelope accepted: %s", value)
		}
	}
}
