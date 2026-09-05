package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestThreadConfigCloneAndValidation(t *testing.T) {
	schema := []byte(`{"type":"object"}`)
	config := ThreadConfig{Workspace: t.TempDir(), OutputSchema: schema}.Clone()
	schema[0] = '['
	if string(config.OutputSchema) != `{"type":"object"}` {
		t.Fatalf("schema was not copied: %q", config.OutputSchema)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOutputUsesImmutableThreadSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"kind":{"enum":["message","artifact"]},"message":{"type":"string"},"decisions":{"type":"array","items":{"type":"object","properties":{"author":{"enum":["user","agent"]},"decision":{"type":"string","minLength":1},"rationale":{"type":"string","minLength":1},"alternatives":{"type":"array","items":{"type":"string"}},"supersedes":{"type":"array","items":{"type":"integer","minimum":1}}},"required":["author","decision","rationale","alternatives","supersedes"],"additionalProperties":false}}},"required":["kind","message","decisions"],"additionalProperties":false}`)
	for _, output := range []json.RawMessage{
		json.RawMessage(`{"kind":"message","message":"question","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"message","message":"decision","decisions":[{"author":"user","decision":"keep","rationale":"required","alternatives":[],"supersedes":[1]}]}`),
	} {
		if err := ValidateOutput(schema, output); err != nil {
			t.Errorf("valid output %s: %v", output, err)
		}
	}
	for _, output := range []json.RawMessage{
		json.RawMessage(`{"kind":"artifact","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[],"path":"intent.md"}`),
		json.RawMessage(`{"kind":"other","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"message","message":"x","decisions":[{"author":"user","decision":"keep","rationale":"required","alternatives":[],"supersedes":[0]}]}`),
	} {
		if err := ValidateOutput(schema, output); err == nil {
			t.Errorf("invalid output accepted: %s", output)
		}
	}
}

func TestMarkThreadFailedPreservesScopeAndCause(t *testing.T) {
	cause := fmt.Errorf("decode: %w", ErrRuntimeProtocol)
	err := MarkThreadFailed(cause)
	if !errors.Is(err, ErrThreadFailed) || !errors.Is(err, ErrRuntimeProtocol) {
		t.Fatalf("thread failure = %v", err)
	}
	if again := MarkThreadFailed(err); again != err {
		t.Fatal("thread failure wrapping is not idempotent")
	}
	if MarkThreadFailed(nil) != nil {
		t.Fatal("nil cause became a failure")
	}
}
