package qwenapp

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/conformance"
	"github.com/AndrMoiseev/stepan/internal/specflow"
)

func TestQwenProviderParity(t *testing.T) {
	conformance.ProviderParity(t, func(t *testing.T, _ []json.RawMessage) conformance.Fixture {
		t.Helper()
		workspace := makeGitRoot(t)
		t.Setenv("GO_WANT_QWENAPP_FAKE", "acp")
		t.Setenv("STEPAN_QWEN_ACP_CASE", "conformance")
		t.Setenv("STEPAN_QWEN_ACP_OBSERVATION", filepath.Join(t.TempDir(), "observation.json"))
		runtime, err := StartRuntime(Config{
			Executable:     absoluteTestExecutable(t),
			Workspace:      workspace,
			JSONContract:   JSONContract,
			EnvelopeSchema: specflow.FlowEnvelopeSchema(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return conformance.Fixture{
			Runtime: runtime, Workspace: workspace, OutputSchema: specflow.DialogueSchema(),
			Decode: func(raw json.RawMessage) (conformance.DomainEnvelope, error) {
				envelope, err := specflow.DecodeEnvelope(raw)
				return conformance.DomainEnvelope{Kind: string(envelope.Kind), Message: envelope.Message, DecisionCount: len(envelope.Decisions)}, err
			},
			WriteAllowed: func(config agentruntime.ThreadConfig, target string) bool {
				_, err := canonicalTargetWithin(config.Workspace, config.ArtifactRoot, target)
				return err == nil
			},
		}
	})
}
