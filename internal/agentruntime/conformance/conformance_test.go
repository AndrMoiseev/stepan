package conformance

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestWorkspaceWritePathCases(t *testing.T) {
	roots := PathRoots{
		WorkspaceRoot: t.TempDir(),
		ArtifactRoot:  t.TempDir(),
		SiblingRoot:   t.TempDir(),
		ExternalRoot:  t.TempDir(),
		LinkRoot:      t.TempDir(),
	}
	cases := WorkspaceWritePathCases(roots)
	if len(cases) == 0 || cases[0].Name != "workspace" || !cases[0].WantAllowed {
		t.Fatalf("workspace-write cases = %#v", cases)
	}
	for _, candidate := range cases[1:] {
		if candidate.Name == "artifact existing" || candidate.Name == "artifact new target" {
			if !candidate.WantAllowed {
				t.Fatalf("workspace-write case %q lost artifact access", candidate.Name)
			}
		}
	}
}

func TestSharedWritePoliciesKeepDocumentAndWorkspaceSessionsDistinct(t *testing.T) {
	workspace := t.TempDir()
	artifact := t.TempDir()
	fixture := Fixture{
		WriteAllowed: func(config agentruntime.ThreadConfig, target string) bool {
			if target == filepath.Join(config.Workspace, "intent.md") || target == filepath.Join(config.Workspace, "source.go") {
				return config.WorkspaceWriteAllowed
			}
			return target == filepath.Join(config.ArtifactRoot, "intent.md") || target == filepath.Join(config.ArtifactRoot, "artifact.md")
		},
	}
	base := agentruntime.ThreadConfig{Workspace: workspace, ArtifactRoot: artifact, OutputSchema: json.RawMessage(`{"type":"object"}`)}
	DocumentSessionWritePolicy(t, fixture, base)
	writeConfig := base.Clone()
	writeConfig.WorkspaceWriteAllowed = true
	WorkspaceWriteSessionPolicy(t, fixture, writeConfig)
}
