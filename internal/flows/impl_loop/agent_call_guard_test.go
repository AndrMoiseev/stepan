package impl_loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	workcopy "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
	"github.com/AndrMoiseev/stepan/internal/git"
)

const executionBlockedPauseReason = "execution_blocked: cannot safely attribute or restore agent file changes"

func TestObserveAgentCallRestoresOnlyOrchestratorViolationAndRequestsRetry(t *testing.T) {
	t.Parallel()
	repository := newFilesystemWorkspace(t)
	writeAgentFile(t, repository, "prior-executor.go", "pre-existing executor work\n")
	run, journal := newAgentCallRun(t)

	outcome, err := observeAgentCall(context.Background(), testfs.New(), repository, AgentCallPolicy{
		Role: AgentRoleOrchestrator, CallID: "orchestrator-1",
		AllowedPaths: []string{"openspec/changes/example/tasks.md"},
	}, run, journal, func() error {
		writeAgentFile(t, repository, "openspec/changes/example/tasks.md", "- [x] task\n")
		writeAgentFile(t, repository, "internal/engine.go", "forbidden agent edit\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Disposition != CallRetry || !strings.Contains(outcome.Diagnostic, "response was rejected") {
		t.Fatalf("outcome = %#v", outcome)
	}
	if run.Status != implstate.RunActive {
		t.Fatalf("successful targeted restoration paused run: %#v", run)
	}
	assertAgentFile(t, repository, "prior-executor.go", "pre-existing executor work\n")
	assertAgentFile(t, repository, "openspec/changes/example/tasks.md", "- [x] task\n")
	if _, err := os.Stat(filepath.Join(repository, "internal", "engine.go")); !os.IsNotExist(err) {
		t.Fatalf("forbidden change remains after restore: %v", err)
	}
	records := readViolationRecords(t, journal)
	if len(records) != 1 || records[0].Role != "orchestrator" || records[0].CallID != "orchestrator-1" || strings.Join(records[0].Paths, ",") != "internal/engine.go" || records[0].RestorationResult != "restored" || !strings.Contains(records[0].ViolatedConstraint, "allowed") {
		t.Fatalf("violation journal = %#v", records)
	}
}

func TestObserveAgentCallPreservesExecutorAllowedWorkAndRestoresProtectedFile(t *testing.T) {
	t.Parallel()
	repository := newFilesystemWorkspace(t)
	writeAgentFile(t, repository, ".stepan/settings.json", "{\"protected\":true}\n")
	run, journal := newAgentCallRun(t)

	outcome, err := observeAgentCall(context.Background(), testfs.New(), repository, AgentCallPolicy{
		Role: AgentRoleExecutor, CallID: "executor-1", AllowUnprotected: true,
		ProtectedPaths: []string{".stepan/settings.json"},
	}, run, journal, func() error {
		writeAgentFile(t, repository, "internal/allowed.go", "allowed implementation\n")
		writeAgentFile(t, repository, ".stepan/settings.json", "{\"protected\":false}\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Disposition != CallRetry {
		t.Fatalf("outcome = %#v", outcome)
	}
	assertAgentFile(t, repository, "internal/allowed.go", "allowed implementation\n")
	assertAgentFile(t, repository, ".stepan/settings.json", "{\"protected\":true}\n")
	records := readViolationRecords(t, journal)
	if len(records) != 1 || records[0].Paths[0] != ".stepan/settings.json" || !strings.Contains(records[0].ViolatedConstraint, "protected") {
		t.Fatalf("violation journal = %#v", records)
	}
}

func TestObserveAgentCallBlocksOnAmbiguousControlChangeWithoutRestoring(t *testing.T) {
	t.Parallel()
	repository := newFilesystemWorkspace(t)
	run, journal := newAgentCallRun(t)

	outcome, err := observeAgentCall(context.Background(), indexChangedWorkspace{Control: testfs.New()}, repository, AgentCallPolicy{
		Role: AgentRoleOrchestrator, CallID: "orchestrator-ambiguous",
		AllowedPaths: []string{"openspec/changes/example/tasks.md"},
	}, run, journal, func() error {
		writeAgentFile(t, repository, "internal/engine.go", "unknown origin\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Disposition != CallExecutionBlocked || run.Status != implstate.RunPaused || run.PauseReason != executionBlockedPauseReason {
		t.Fatalf("ambiguous control-state change did not block: outcome=%#v run=%#v", outcome, run)
	}
	assertAgentFile(t, repository, "internal/engine.go", "unknown origin\n")
	if records := readViolationRecords(t, journal); len(records) != 0 {
		t.Fatalf("unattributed control-state change was journaled as a role violation: %#v", records)
	}
}

func TestObserveAgentCallBlocksWhenTargetedRollbackIsImpossible(t *testing.T) {
	t.Parallel()
	repository := newFilesystemWorkspace(t)
	writeAgentFile(t, repository, ".stepan/settings.json", "{\"protected\":true}\n")
	run, journal := newAgentCallRun(t)

	outcome, err := observeAgentCall(context.Background(), testfs.New(), repository, AgentCallPolicy{
		Role: AgentRoleExecutor, CallID: "executor-unsafe-restore", AllowUnprotected: true,
		ProtectedPaths: []string{".stepan/settings.json"},
	}, run, journal, func() error {
		if err := os.Remove(filepath.Join(repository, ".stepan", "settings.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repository, ".stepan", "settings.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Disposition != CallExecutionBlocked || run.Status != implstate.RunPaused {
		t.Fatalf("unsafe restoration did not block: outcome=%#v run=%#v", outcome, run)
	}
	if info, err := os.Stat(filepath.Join(repository, ".stepan", "settings.json")); err != nil || !info.IsDir() {
		t.Fatalf("unsafe target was changed by fallback restoration: info=%v err=%v", info, err)
	}
	records := readViolationRecords(t, journal)
	if len(records) != 1 || !strings.HasPrefix(records[0].RestorationResult, "failed:") {
		t.Fatalf("failed restoration was not recorded: %#v", records)
	}
}

func newAgentCallRun(t *testing.T) (*implstate.Run, *runstore.Run) {
	t.Helper()
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("agent-call")
	if err != nil {
		t.Fatal(err)
	}
	return &implstate.Run{Status: implstate.RunActive}, journal
}

func writeAgentFile(t *testing.T, repository, relative, contents string) {
	t.Helper()
	path := filepath.Join(repository, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertAgentFile(t *testing.T, repository, relative, want string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(relative)))
	if err != nil || string(contents) != want {
		t.Fatalf("file %s = %q, %v; want %q", relative, contents, err, want)
	}
}

// Models the ambiguous metadata observation without executing a Git mutation.
type indexChangedWorkspace struct{ workcopy.Control }

func (w indexChangedWorkspace) Diff(ctx context.Context, repository string, before, after git.Snapshot) (git.Difference, error) {
	diff, err := w.Control.Diff(ctx, repository, before, after)
	diff.IndexChanged = true
	return diff, err
}
