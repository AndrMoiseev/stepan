package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/checkexec"
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

func TestResumeRunsEntireRequiredSetWithoutConsumingAttempts(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.load = func(string) (implementationconfig.Configuration, error) {
		return resumeChecksConfiguration(t, `{
"lint":{"kind":"lint","command":{"program":"lint","args":[]}},
"test_all":{"kind":"tests","command":{"program":"test_all","args":[]}},
"build":{"kind":"build","command":{"program":"build","args":[]}}}`, `["lint","test_all","build"]`), nil
	}
	var calls []string
	input := fixture.input()
	input.Runner = CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		calls = append(calls, command.Program)
		return checkexec.Result{}, nil
	})

	result, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"lint", "test_all", "build"}; !slices.Equal(got, want) {
		t.Fatalf("resume required-check order = %#v, want %#v", got, want)
	}
	if fixture.run.Status != implementationstate.RunActive || !result.ResumeChecks.Succeeded() || result.ResumeCheckEvidence.ID == "" {
		t.Fatalf("resume did not return active only after fresh required evidence: result=%#v run=%#v", result, fixture.run)
	}
	operation := fixture.run.RunOperations[len(fixture.run.RunOperations)-1]
	if !operation.UncountedResumeCheck || operation.Counter != implementationstate.CycleCounterNone || len(operation.Attempts) != 0 {
		t.Fatalf("resume checks consumed attempts: %#v", operation)
	}
	if len(fixture.run.RunResults) != 1 || fixture.run.RunResults[0].Status != implementationstate.ResultSucceeded {
		t.Fatalf("resume checks did not retain successful result: %#v", fixture.run.RunResults)
	}
}

func TestResumeInterruptedRequiredCommandRestartsCompleteSetFromFirstCheck(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.load = func(string) (implementationconfig.Configuration, error) {
		return resumeChecksConfiguration(t, `{
"lint":{"kind":"lint","command":{"program":"lint","args":[]}},
"test_all":{"kind":"tests","command":{"program":"test_all","args":[]}}}`, `["lint","test_all"]`), nil
	}
	var calls []string
	interrupted := true
	firstContext, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	input := fixture.input()
	input.Runner = CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		calls = append(calls, command.Program)
		if interrupted {
			interrupted = false
			cancelFirst()
			return checkexec.Result{ExitCode: -1, Failure: checkexec.FailureCanceled, Stderr: []byte("process ended during lint")}, context.Canceled
		}
		return checkexec.Result{}, nil
	})

	first, err := Resume(firstContext, input)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.run.Status != implementationstate.RunPaused || first.ResumeChecks.Results[0].Status != CheckFailed || first.ResumeChecks.Results[1].Status != CheckNotRun || fixture.run.RunResults[0].Status != implementationstate.ResultInterrupted {
		t.Fatalf("interrupted resume check did not remain a diagnostic pause: result=%#v run=%#v", first, fixture.run)
	}
	second, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"lint", "lint", "test_all"}; !slices.Equal(got, want) {
		t.Fatalf("resume continued an interrupted set instead of restarting it: %#v, want %#v", got, want)
	}
	if fixture.run.Status != implementationstate.RunActive || !second.ResumeChecks.Succeeded() {
		t.Fatalf("second resume did not establish fresh full evidence: result=%#v run=%#v", second, fixture.run)
	}
	for _, operation := range fixture.run.RunOperations {
		if operation.UncountedResumeCheck && len(operation.Attempts) != 0 {
			t.Fatalf("resume check retained an attempt: %#v", operation)
		}
	}
}

func TestResumeRequiredCheckFailurePersistsPauseAndDoesNotCreateSessions(t *testing.T) {
	fixture := newResumeFixture(t, "")
	changed := resumeTestConfiguration(t, "changed-model", "")
	fixture.load = func(string) (implementationconfig.Configuration, error) { return changed, nil }
	owner, err := NewSessionOwner(PreparedRuntimes{}, threadConfigForTest(fixture.repository))
	if err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.SessionOwner = &owner
	input.SessionBase = threadConfigForTest(fixture.repository)
	var calls []string
	input.Runner = CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		calls = append(calls, command.Program)
		return checkexec.Result{ExitCode: 1, Stderr: []byte("unit failed")}, nil
	})

	result, err := Resume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []string{"unit"}; !slices.Equal(got, want) {
		t.Fatalf("failed resume ran unexpected commands: %#v, want %#v", got, want)
	}
	if fixture.run.Status != implementationstate.RunPaused || fixture.run.ExecutionBlock == nil || !strings.Contains(fixture.run.ExecutionBlock.Diagnostic, "unit failed") || result.ResumeChecks.Results[0].Status != CheckFailed {
		t.Fatalf("failed resume check did not persist diagnostic pause: result=%#v run=%#v", result, fixture.run)
	}
	if owner == nil || result.SessionsRecreated {
		t.Fatalf("resume check failure created or replaced sessions before the gate passed: result=%#v", result)
	}
	operation := fixture.run.RunOperations[len(fixture.run.RunOperations)-1]
	if !operation.UncountedResumeCheck || len(operation.Attempts) != 0 {
		t.Fatalf("failed resume check consumed attempts: %#v", operation)
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
	fixture.classify = func([]byte, []byte) (SpecificationChange, error) {
		return SpecificationChange{RequiresNewScope: true}, nil
	}
	writeResumeFile(t, filepath.Join(fixture.repository, "openspec", "changes", "change", "proposal.md"), "changed scope\n")

	_, err := Resume(context.Background(), fixture.input())
	if !errors.Is(err, ErrResumeScopeChanged) {
		t.Fatalf("resume error = %v", err)
	}
	if fixture.run.Status != implementationstate.RunClosed || fixture.run.CloseReason == "" {
		t.Fatalf("scope change did not close run: %#v", fixture.run)
	}
}

func TestResumeRefreshesCompatibleSpecificationChange(t *testing.T) {
	fixture := newResumeFixture(t, "")
	fixture.classify = func(previous, current []byte) (SpecificationChange, error) {
		if string(previous) == string(current) {
			t.Fatal("classifier did not receive distinct specification versions")
		}
		return SpecificationChange{}, nil
	}
	writeResumeFile(t, filepath.Join(fixture.repository, "openspec", "changes", "change", "proposal.md"), "compatible clarification\n")

	if _, err := Resume(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	if fixture.run.Status != implementationstate.RunActive || fixture.run.Identity.Specification == fixture.specification {
		t.Fatalf("compatible specification was not refreshed: %#v", fixture.run)
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

func TestResumeRulesClassificationUsesExactValidatedMarkdownDocuments(t *testing.T) {
	for _, test := range []struct {
		name          string
		path          string
		staged        bool
		wantWorkspace bool
	}{
		{name: "root markdown rule", path: "rules/AGENTS.md"},
		{name: "staged markdown rule", path: "rules/AGENTS.md", staged: true},
		{name: "sibling code", path: "rules/helper.go", wantWorkspace: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResumeFixture(t, "rules/AGENTS.md")
			fixture.workspace.actual.TreeOID = "changed-" + test.name
			fixture.workspace.actual.StatusHash = "changed-status-" + test.name
			if test.staged {
				fixture.workspace.actual.IndexHash = "staged-rules-index"
			}
			fixture.workspace.paths = []string{test.path}
			writeResumeFile(t, filepath.Join(fixture.repository, filepath.FromSlash(test.path)), "changed\n")
			result, err := Resume(context.Background(), fixture.input())
			if err != nil {
				t.Fatal(err)
			}
			if result.WorkspaceChanged != test.wantWorkspace {
				t.Fatalf("WorkspaceChanged = %v, want %v", result.WorkspaceChanged, test.wantWorkspace)
			}
		})
	}
}

func TestResumePausesForChangedGitControlButPreservesUnstagedEdits(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*gitsnapshot.Snapshot)
		wantError bool
	}{
		{name: "changed branch", change: func(s *gitsnapshot.Snapshot) { s.HeadRef = "refs/heads/other" }, wantError: true},
		{name: "detached head", change: func(s *gitsnapshot.Snapshot) { s.HeadRef = "" }, wantError: true},
		{name: "unrelated commit", change: func(s *gitsnapshot.Snapshot) { s.HeadOID = "other-head" }, wantError: true},
		{name: "staged index", change: func(s *gitsnapshot.Snapshot) { s.IndexHash = "other-index" }, wantError: true},
		{name: "unstaged edit", change: func(s *gitsnapshot.Snapshot) { s.TreeOID, s.StatusHash = "manual-tree", "manual-status" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResumeFixture(t, "")
			test.change(&fixture.workspace.actual)
			fixture.workspace.paths = []string{"code.go"}
			result, err := Resume(context.Background(), fixture.input())
			if test.wantError {
				if !errors.Is(err, ErrResumeReconciliation) || fixture.run.Status != implementationstate.RunPaused {
					t.Fatalf("unsafe Git control change was accepted: err=%v run=%#v", err, fixture.run)
				}
				return
			}
			if err != nil || !result.WorkspaceChanged || fixture.run.Status != implementationstate.RunActive {
				t.Fatalf("unstaged edit was not retained: err=%v result=%#v run=%#v", err, result, fixture.run)
			}
		})
	}
}

func TestResumePreservesAcceptedPendingCommitAfterInformationalReflection(t *testing.T) {
	fixture := newResumeFixture(t, "")
	preparePendingCommitReflection(t, fixture)
	fixture.workspace.actual.TreeOID = "informational-reflection-tree"
	fixture.workspace.actual.StatusHash = "informational-reflection-status"
	fixture.workspace.actual.IndexHash = "staged-by-refused-hook"
	fixture.workspace.paths = []string{"openspec/changes/change/tasks.md"}

	if _, err := Resume(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	assignment := fixture.run.Assignments[0]
	if fixture.run.Status != implementationstate.RunActive || assignment.Status != implementationstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil || assignment.Acceptance.PendingCommit.Tree != "informational-reflection-tree" {
		t.Fatalf("resume invalidated accepted pending commit after informational reflection: %#v", fixture.run)
	}
}

func TestResumePreservesAcceptedReflectionBeforePendingCommitIntent(t *testing.T) {
	fixture := newResumeFixture(t, "rules/rules.md")
	prepareAcceptedReflectionEvidence(t, fixture)
	fixture.workspace.actual.TreeOID = "reflected-tasks-and-rules-tree"
	fixture.workspace.actual.StatusHash = "reflected-tasks-and-rules-status"
	fixture.workspace.compare = func(before, after gitsnapshot.Snapshot) []string {
		switch {
		case before.TreeOID == "expected-tree" && after.TreeOID == "reflected-tasks-tree":
			return []string{"openspec/changes/change/tasks.md"}
		case before.TreeOID == "reflected-tasks-tree" && after.TreeOID == "reflected-tasks-and-rules-tree":
			return []string{"rules/rules.md"}
		default:
			return nil
		}
	}

	if _, err := Resume(context.Background(), fixture.input()); err != nil {
		t.Fatal(err)
	}
	assignment := fixture.run.Assignments[0]
	if fixture.run.Status != implementationstate.RunActive || assignment.Status != implementationstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil || assignment.Acceptance.PendingCommit.OperationID != "" {
		t.Fatalf("durable reflection evidence did not preserve pending acceptance: %#v", fixture.run)
	}
}

func TestResumeCheckGeneratedChangeInvalidatesAcceptedState(t *testing.T) {
	fixture := newResumeFixture(t, "")
	preparePendingCommitReflection(t, fixture)
	fixture.workspace.diff = func(before, after gitsnapshot.Snapshot) gitsnapshot.Difference {
		if before.TreeOID != after.TreeOID || before.StatusHash != after.StatusHash {
			return gitsnapshot.Difference{Paths: []string{"generated.go"}}
		}
		return gitsnapshot.Difference{}
	}
	input := fixture.input()
	input.Runner = CheckRunnerFunc(func(_ context.Context, _ checkexec.Command) (checkexec.Result, error) {
		fixture.workspace.actual.TreeOID = "generated-by-resume-check"
		fixture.workspace.actual.StatusHash = "generated-by-resume-check-status"
		return checkexec.Result{}, nil
	})

	if _, err := Resume(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	assignment := fixture.run.Assignments[0]
	if fixture.run.Status != implementationstate.RunActive || assignment.Status != implementationstate.AssignmentActive || assignment.Acceptance != nil || fixture.run.CurrentState == fixture.baseline {
		t.Fatalf("resume check-generated mutation did not invalidate acceptance: %#v", fixture.run)
	}
}

type resumeFixture struct {
	repository    string
	run           *implementationstate.Run
	journal       *runstore.Run
	state         *runstore.StateStore
	baseline      implementationstate.EvidenceRef
	specification implementationstate.EvidenceRef
	workspace     *resumeWorkspace
	load          func(string) (implementationconfig.Configuration, error)
	classify      func([]byte, []byte) (SpecificationChange, error)
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
	return &resumeFixture{repository: repository, run: run, journal: journal, state: state, baseline: baseline, specification: specRef, workspace: &resumeWorkspace{actual: expected}, load: func(string) (implementationconfig.Configuration, error) { return configuration, nil }}
}

func (fixture *resumeFixture) input() ResumeInput {
	return ResumeInput{
		Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, Workspace: fixture.workspace,
		Runner:              CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) { return checkexec.Result{}, nil }),
		ConfigurationLoader: fixture.load, ClassifySpecificationChange: fixture.classify,
	}
}

func preparePendingCommitReflection(t *testing.T, fixture *resumeFixture) {
	t.Helper()
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if err := fixture.run.AddRunOperation(implementationstate.Operation{ID: "baseline-check", Kind: implementationstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartRunAttempt("baseline-check"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddRunResult(implementationstate.OperationResult{ID: "baseline-check-result", OperationID: "baseline-check", Status: implementationstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordInitialBaselinePass("baseline-check", "baseline-check-result"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.StartAssignment("assignment", []implementationstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief, err := fixture.journal.Publish("pending-brief", []byte("brief"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddBriefVersion("assignment", implementationstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []implementationstate.Operation{{ID: "check", Kind: implementationstate.OperationCheck, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterMandatoryChecks}, {ID: "review", Kind: implementationstate.OperationReview, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterAssignmentReview}} {
		if err := fixture.run.AddOperation("assignment", operation); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.run.StartAssignmentAttempt("assignment", operation.ID); err != nil {
			t.Fatal(err)
		}
		if err := fixture.run.AddResult("assignment", implementationstate.OperationResult{ID: implementationstate.ResultID(string(operation.ID) + "-result"), OperationID: operation.ID, Status: implementationstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	acceptance := implementationstate.AcceptanceEvidence{BriefID: "brief", State: fixture.run.CurrentState, Basis: basis, CheckResultIDs: []implementationstate.ResultID{"check-result"}, ReviewResultID: "review-result", PendingCommit: implementationstate.CommitIntent{OperationID: "commit", ParentCommit: "head", Tree: "informational-reflection-tree", Message: "commit accepted work"}}
	if err := fixture.run.AcceptAssignment("assignment", acceptance); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.Pause("commit hook failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
}

func prepareAcceptedReflectionEvidence(t *testing.T, fixture *resumeFixture) {
	t.Helper()
	preparePendingCommitReflection(t, fixture)
	// Model the narrower crash window after a successful reflection was made
	// durable but before CommitAcceptedAssignment saved its intent.
	if err := fixture.run.Resume(); err != nil {
		t.Fatal(err)
	}
	fixture.run.Assignments[0].Acceptance.PendingCommit = implementationstate.CommitIntent{}
	if err := fixture.run.AddRunOperation(implementationstate.Operation{ID: "reflect-progress", Kind: implementationstate.OperationAgent, Basis: implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}, Description: "reflect accepted task progress in tasks.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run.StartRunAttempt("reflect-progress"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordRunAttemptOutcome("reflect-progress", implementationstate.AttemptSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	state := resumeSnapshot("reflected-tasks-tree", "reflected-tasks-status")
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := fixture.journal.Publish("reflect-progress-result-workspace", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.AddRunResult(implementationstate.OperationResult{ID: "reflect-progress-result", OperationID: "reflect-progress", Status: implementationstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}, Evidence: []implementationstate.EvidenceRef{evidence}}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.Pause("before pending commit intent"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
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

func resumeChecksConfiguration(t *testing.T, checks, required string) implementationconfig.Configuration {
	t.Helper()
	profiles := `{"low":{"provider":"test","model":"initial-model"},"medium":{"provider":"test","model":"initial-model"},"high":{"provider":"test","model":"initial-model"},"ultra":{"provider":"test","model":"initial-model"}}`
	configuration, err := implementationconfig.Merge(implementationconfig.Sources{Project: json.RawMessage(`{"profiles":` + profiles + `,"checks":` + checks + `,"required_checks":` + required + `}`)})
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
	compare  func(before, after gitsnapshot.Snapshot) []string
	diff     func(before, after gitsnapshot.Snapshot) gitsnapshot.Difference
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

func (workspace *resumeWorkspace) Compare(_ context.Context, _ string, before, after gitsnapshot.Snapshot) ([]string, error) {
	if workspace.compare != nil {
		return append([]string(nil), workspace.compare(before, after)...), nil
	}
	return append([]string(nil), workspace.paths...), nil
}
func (workspace *resumeWorkspace) Diff(_ context.Context, _ string, before, after gitsnapshot.Snapshot) (gitsnapshot.Difference, error) {
	if workspace.diff != nil {
		return workspace.diff(before, after), nil
	}
	return gitsnapshot.Difference{}, nil
}
func (workspace *resumeWorkspace) RestorePaths(context.Context, string, gitsnapshot.Snapshot, gitsnapshot.Snapshot, []string) (gitsnapshot.Snapshot, error) {
	workspace.restores++
	return workspace.actual, nil
}
func (workspace *resumeWorkspace) AssignmentDiff(context.Context, string, string) (string, error) {
	return "", nil
}
