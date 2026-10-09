package setting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapProposalDoesNotRetainToken(t *testing.T) {
	directory := t.TempDir()
	paths := BootstrapConfigurationPaths{User: filepath.Join(directory, "user.json"), Project: filepath.Join(directory, "project.json")}
	writeSettings(t, paths.User, `{"agentruntime":{"nessyapp":{"auth_token":"home-secret"}}}`)
	proposal, err := PrepareBootstrapConfigurationProposal(paths, `{"agentruntime":{"profiles":{"high":{"provider":"codex","model":"m"}}}}`, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range proposal.patches {
		if strings.Contains(string(patch), "home-secret") {
			t.Fatal("token entered bootstrap proposal")
		}
	}
	for _, diff := range proposal.Diffs {
		if strings.Contains(diff.Diff, "home-secret") || strings.Contains(diff.Diff, "auth_token") {
			t.Fatalf("token entered bootstrap diff: %s", diff.Diff)
		}
	}
	if err := SaveBootstrapConfigurationProposal(proposal); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.User)
	if err != nil || !strings.Contains(string(data), "home-secret") {
		t.Fatalf("saved settings lost token: %v", err)
	}
}

func TestBootstrapProposalRejectsNestedCredentialFields(t *testing.T) {
	paths := BootstrapConfigurationPaths{User: filepath.Join(t.TempDir(), "user.json"), Project: filepath.Join(t.TempDir(), "project.json")}
	_, err := PrepareBootstrapConfigurationProposal(paths, `{"agentruntime":{"profiles":{"high":{"provider":"codex","model":"m","api_key":"secret"}}}}`, `{}`)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("credential proposal error = %v", err)
	}
}
