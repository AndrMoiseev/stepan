package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/openspec"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestResumeReconcilesPausedRunAndPreservesManualWorkingCopyChanges(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.workspace.actual.TreeOID = "manual-code-tree"
	fixture.workspace.actual.StatusHash = "manual-code-status"
	fixture.workspace.paths = []string{"internal/example.go"}

	result, err := Resume(context.Background(), fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	if fixture.run.Status != implementationstate.RunActive || !result.WorkspaceChanged {
		t.Fatalf("resume did not retain classified manual change: run=%#v result=%#v", fixture.run, result)
	}
	if fixture.run.CurrentState == fixture.baseline {
		t.Fatal("manual working-copy fingerprint was not retained as current state")
	}
	if fixture.workspace.restores != 0 {
		t.Fatalf("resume restored manual worktree changes %d times", fixture.workspace.restores)
	}
}

func TestResumeReloadsChangedConfigurationAndRecreatesSessionOwner(t *testing.T) {
	fixture := newResumeFixture(t, "")
	changed := resumeTestConfiguration(t, "changed-model", "")
	fixture.load = func(string) (implementationconfig.Configuration, error) { return changed, nil }
	base := fixture.repository
	old, err := NewSessionOwner(PreparedRuntimes{}, threadConfigForTest(base))
	if err != nil {
		t.Fatal(err)
	}
	owner := old
	input := fixture.input()
	input.SessionOwner = &owner
	input.SessionBase = threadConfigForTest(base)

	result, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConfigurationChanged || !result.SessionsRecreated || owner == old {
		t.Fatalf("configuration reload did not install a fresh session owner: %#v", result)
	}
	if _, err := old.Orchestrator(context.Background(), RoleStartContext{Role: ResponseRoleOrchestrator}); !errors.Is(err, ErrSessionOwnerClosed) {
		t.Fatalf("old owner remains usable after profile change: %v", err)
	}
}

func TestResumeInvalidConfigurationLeavesDurableDiagnosticPause(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.load = func(string) (implementationconfig.Configuration, error) {
		return implementationconfig.Configuration{}, errors.New("invalid implementation JSON")
	}

	_, err := Resume(context.Background(), fixture.input())
	if !errors.Is(err, ErrResumeReconciliation) {
		t.Fatalf("resume error = %v", err)
	}
	if fixture.run.Status != implementationstate.RunPaused || fixture.run.ExecutionBlock == nil || fixture.run.ExecutionBlock.Diagnostic == "" {
		t.Fatalf("invalid configuration did not remain a diagnostic pause: %#v", fixture.run)
	}
	persisted, _, readErr := fixture.state.Current(context.Background())
	if readErr != nil || persisted.Status != implementationstate.RunPaused || persisted.ExecutionBlock == nil {
		t.Fatalf("diagnostic pause was not durable: run=%#v err=%v", persisted, readErr)
	}
}

func TestResumeClosesWhenCompleteSpecificationChanges(t *testing.T) {
	fixture := newResumeFixture(t, "")
	writeResumeFile(t, filepath.Join(fixture.repository, "openspec", "changes", "change", "proposal.md"), "changed scope\n")

	_, err := Resume(context.Background(), fixture.input())
	if !errors.Is(err, ErrResumeScopeChanged) {
		t.Fatalf("resume error = %v", err)
	}
	if fixture.run.Status != implementationstate.RunClosed || fixture.run.CloseReason == "" {
		t.Fatalf("scope change did not close run: %#v", fixture.run)
	}
}

func TestResumeRulesContentOnlyDoesNotInvalidateCurrentAcceptanceState(t *testing.T) {
	fixture := newResumeFixture(t, "rules/rules.md")
	fixture.workspace.actual.TreeOID = "rules-only-tree"
	fixture.workspace.actual.StatusHash = "rules-only-status"
	fixture.workspace.paths = []string{"rules/rules.md"}
	writeResumeFile(t, filepath.Join(fixture.repository, "rules", "rules.md"), "# Updated rules\n")

	result, err := Resume(context.Background(), fixture.input())
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceChanged || fixture.run.CurrentState != fixture.baseline || fixture.run.Status != implementationstate.RunActive {
		t.Fatalf("rules-only edit altered acceptance state: result=%#v run=%#v", result, fixture.run)
	}
}

type resumeFixture struct {
	repository string
	run        *implementationstate.Run
	journal    *runstore.Run
	state      *runstore.StateStore
	baseline   implementationstate.EvidenceRef
	workspace  *resumeWorkspace
	load       func(string) (implementationconfig.Configuration, error)
}

func newResumeFixture(t *testing.T, rulesFile string) *resumeFixture {
	t.Helper()
	repository := t.TempDir()
	writeResumeFile(t, filepath.Join(repository, "openspec", "changes", "change", "proposal.md"), "proposal\n")
	writeResumeFile(t, filepath.Join(repository, "openspec", "changes", "change", "design.md"), "design\n")
	writeResumeFile(t, filepath.Join(repository, "openspec", "changes", "change", "tasks.md"), "- [ ] task\n")
	writeResumeFile(t, filepath.Join(repository, "openspec", "changes", "change", "specs", "feature", "spec.md"), "change requirement\n")
	writeResumeFile(t, filepath.Join(repository, "openspec", "specs", "base", "spec.md"), "base requirement\n")
	if rulesFile != "" {
		writeResumeFile(t, filepath.Join(repository, filepath.FromSlash(rulesFile)), "# Rules\n")
	}
	configuration := resumeTestConfiguration(t, "initial-model", rulesFile)
	configurationBytes, err := canonicalResumeConfiguration(configuration)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := openspec.Load(repository, "change")
	if err != nil {
		t.Fatal(err)
	}
	specification, err := resumeSpecification(pkg)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(filepath.Join(t.TempDir(), "stepan"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("resume-run")
	if err != nil {
		t.Fatal(err)
	}
	expected := resumeSnapshot("expected-tree", "expected-status")
	snapshotBytes, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := journal.Publish("baseline", snapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	specRef, err := journal.Publish("specification", specification)
	if err != nil {
		t.Fatal(err)
	}
	configRef, err := journal.Publish("configuration", configurationBytes)
	if err != nil {
		t.Fatal(err)
	}
	tasksRef, err := journal.Publish("tasks", []byte("tasks"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := implementationstate.NewRun(implementationstate.RunIdentity{ID: "resume-run", Change: "change", Repository: repository, WorkCopy: repository, Branch: "feature", BaselineCommit: "base", BaselineState: baseline, Specification: specRef, TaskList: tasksRef, Configuration: configRef}, []implementationstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Pause("user paused"); err != nil {
		t.Fatal(err)
	}
	state, err := runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	return &resumeFixture{repository: repository, run: run, journal: journal, state: state, baseline: baseline, workspace: &resumeWorkspace{actual: expected}, load: func(string) (implementationconfig.Configuration, error) { return configuration, nil }}
}

func (fixture *resumeFixture) input() ResumeInput {
	return ResumeInput{Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Workspace: fixture.workspace, ConfigurationLoader: fixture.load}
}

func resumeTestConfiguration(t *testing.T, model, rulesFile string) implementationconfig.Configuration {
	t.Helper()
	profiles := `{"low":{"provider":"test","model":"` + model + `"},"medium":{"provider":"test","model":"` + model + `"},"high":{"provider":"test","model":"` + model + `"},"ultra":{"provider":"test","model":"` + model + `"}}`
	raw := `{"profiles":` + profiles + `,"checks":{"unit":{"kind":"tests","command":{"program":"unit","args":[]}}},"required_checks":["unit"]`
	if rulesFile != "" {
		raw += `,"rules_file":"` + rulesFile + `"`
	}
	raw += `}`
	configuration, err := implementationconfig.Merge(implementationconfig.Sources{Project: json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

func resumeSnapshot(tree, status string) gitsnapshot.Snapshot {
	return gitsnapshot.Snapshot{HeadOID: "head", HeadRef: "refs/heads/feature", TreeOID: tree, IndexHash: "index", StatusHash: status, SubmodulesHash: "submodules"}
}

func threadConfigForTest(workspace string) agentruntime.ThreadConfig {
	return agentruntime.ThreadConfig{Workspace: workspace}
}

func writeResumeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

type resumeWorkspace struct {
	actual   gitsnapshot.Snapshot
	paths    []string
	restores int
}

func (workspace *resumeWorkspace) Capture(context.Context, string) (gitsnapshot.Snapshot, error) {
	return workspace.actual, nil
}
func (workspace *resumeWorkspace) EnsureUnchanged(_ context.Context, _ string, expected gitsnapshot.Snapshot) error {
	if sameResumeSnapshot(workspace.actual, expected) {
		return nil
	}
	return gitsnapshot.ErrRepositoryDiverged
}

func sameResumeSnapshot(left, right gitsnapshot.Snapshot) bool {
	return left.HeadOID == right.HeadOID && left.HeadRef == right.HeadRef && left.TreeOID == right.TreeOID && left.IndexHash == right.IndexHash && left.StatusHash == right.StatusHash && left.SubmodulesHash == right.SubmodulesHash
}
func (workspace *resumeWorkspace) Compare(context.Context, string, gitsnapshot.Snapshot, gitsnapshot.Snapshot) ([]string, error) {
	return append([]string(nil), workspace.paths...), nil
}
func (workspace *resumeWorkspace) Diff(context.Context, string, gitsnapshot.Snapshot, gitsnapshot.Snapshot) (gitsnapshot.Difference, error) {
	return gitsnapshot.Difference{}, nil
}
func (workspace *resumeWorkspace) RestorePaths(context.Context, string, gitsnapshot.Snapshot, gitsnapshot.Snapshot, []string) (gitsnapshot.Snapshot, error) {
	workspace.restores++
	return workspace.actual, nil
}
func (workspace *resumeWorkspace) AssignmentDiff(context.Context, string, string) (string, error) {
	return "", nil
}
