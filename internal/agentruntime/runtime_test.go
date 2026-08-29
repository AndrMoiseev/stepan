package agentruntime

import "testing"

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
