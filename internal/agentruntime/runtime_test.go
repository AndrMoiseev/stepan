package agentruntime

import (
	"path/filepath"
	"testing"
)

func TestTurnOptionsCloneAndPolicies(t *testing.T) {
	schema := []byte(`{"type":"object"}`)
	options := TurnOptions{OutputSchema: schema, Policy: ReadOnlyTurnPolicy()}.Clone()
	schema[0] = '['
	if string(options.OutputSchema) != `{"type":"object"}` {
		t.Fatalf("schema was not copied: %q", options.OutputSchema)
	}
	if _, writable := options.Policy.WritableRoot(); writable {
		t.Fatal("read-only policy has a writable root")
	}
	if _, err := SingleWriteRootTurnPolicy("relative"); err == nil {
		t.Fatal("relative write root was accepted")
	}
	root := filepath.Join(t.TempDir(), "write")
	policy, err := SingleWriteRootTurnPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, writable := policy.WritableRoot(); !writable || got != filepath.Clean(root) {
		t.Fatalf("write policy = %q, %t", got, writable)
	}
}
