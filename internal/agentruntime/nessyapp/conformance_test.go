package nessyapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/conformance"
	"github.com/AndrMoiseev/stepan/internal/specflow"
)

func TestNessyProviderParity(t *testing.T) {
	conformance.ProviderParity(t, func(t *testing.T, script conformance.Script) conformance.Fixture {
		t.Helper()
		workspace := makeGitRoot(t)
		runtime := nessyConformanceRuntime(t, workspace, script)
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

func TestNessyApplicationParity(t *testing.T) {
	conformance.ApplicationParity(t, specflow.RuntimeIdentity{Provider: "nessy", Model: "default"}, func(t *testing.T, workspace string, script conformance.Script) conformance.ApplicationFixture {
		t.Helper()
		return conformance.ApplicationFixture{Runtime: nessyConformanceRuntime(t, workspace, script)}
	})
}

func nessyConformanceRuntime(t *testing.T, workspace string, script conformance.Script) *Runtime {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "script.json")
	data, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO_WANT_NESSYAPP_FAKE", "acp")
	t.Setenv("STEPAN_NESSY_ACP_CASE", "conformance")
	t.Setenv("STEPAN_NESSY_CONFORMANCE_SCRIPT", scriptPath)
	t.Setenv("STEPAN_NESSY_ACP_OBSERVATION", filepath.Join(t.TempDir(), "observation.json"))
	runtime, err := StartRuntime(Config{
		AuthToken:      testAuthToken(t),
		Workspace:      workspace,
		JSONContract:   JSONContract,
		EnvelopeSchema: specflow.FlowEnvelopeSchema(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
