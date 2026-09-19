//go:build git_integration

package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// TestDeterministicRecoveryEndToEnd drives the public recovery seams through
// representative crash boundaries.  The agent and check runners are local
// deterministic fakes; the Git boundary deliberately uses a disposable real
// repository.  This keeps provider behaviour out of the conformance test
// while ensuring that a restart joins the durable journal, SQLite projection,
// and local commit history without duplicating allowed work.
func TestDeterministicRecoveryEndToEnd(t *testing.T) {
	t.Run("agent receipt survives database loss without repeating the turn", func(t *testing.T) {
		runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: controlledResponse(t, "preserve this accepted work")}}}
		call := controlledCallFixture(t, runtime)
		basis := implementationstate.AcceptanceBasis{Specification: call.Run.Identity.Specification, Configuration: call.Run.Identity.Configuration}
		if err := call.Run.AddRunOperation(implementationstate.Operation{
			ID: "unrelated-explorer", Kind: implementationstate.OperationAgent, Basis: basis,
			Counter: implementationstate.CycleCounterExplorer, Episode: "unrelated",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := call.Run.StartRunAttemptWithLimits("unrelated-explorer", controlledCallLimits()); err != nil {
			t.Fatal(err)
		}
		if err := call.Run.RecordRunAttemptOutcome("unrelated-explorer", implementationstate.AttemptSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := call.StateStore.Record(context.Background(), call.Run); err != nil {
			t.Fatal(err)
		}

		crash := errors.New("simulated process exit after durable agent receipt")
		call.AfterSuccessReceipt = func() error { return crash }
		if _, err := InvokeControlledAgentCall(context.Background(), call); !errors.Is(err, crash) {
			t.Fatalf("agent crash boundary error = %v", err)
		}
		if err := call.StateStore.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(call.StateStore.DatabasePath()); err != nil {
			t.Fatal(err)
		}

		reopened, err := runstore.OpenState(call.Journal)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		recovered, _, err := reopened.Current(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		call.Run = recovered
		call.StateStore = reopened
		call.AfterSuccessReceipt = nil
		result, err := InvokeControlledAgentCall(context.Background(), call)
		if err != nil || result.Response.Message == nil || *result.Response.Message != "preserve this accepted work" {
			t.Fatalf("recover agent receipt result=%#v err=%v", result, err)
		}
		agent := finalRunOperation(recovered, "agent-operation")
		if len(runtime.messages) != 1 || agent == nil || len(agent.Attempts) != 1 || agent.Attempts[0].Outcome != implementationstate.AttemptSucceeded {
			t.Fatalf("recovery repeated or lost agent work: messages=%q operation=%#v", runtime.messages, agent)
		}
		if recovered.RunExplorerCounters["unrelated"] != 1 {
			t.Fatalf("recovery reset unrelated Explorer accounting: %#v", recovered.RunExplorerCounters)
		}
	})

	t.Run("resume retries the complete check set without consuming attempts", func(t *testing.T) {
		fixture := newResumeFixture(t, "")
		fixture.load = func(string) (configuration implementationconfig.Configuration, err error) {
			return resumeChecksConfiguration(t, `{
"lint":{"kind":"lint","command":{"program":"lint","args":[]}},
"test_all":{"kind":"tests","command":{"program":"test_all","args":[]}}}`, `["lint","test_all"]`), nil
		}
		var calls []string
		interrupted := true
		firstContext, cancel := context.WithCancel(context.Background())
		defer cancel()
		input := fixture.input()
		input.Runner = CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
			calls = append(calls, command.Program)
			if interrupted {
				interrupted = false
				cancel()
				return checkexec.Result{ExitCode: -1, Failure: checkexec.FailureCanceled}, context.Canceled
			}
			return checkexec.Result{}, nil
		})
		if _, err := Resume(firstContext, input); err != nil {
			t.Fatalf("interrupted resume = %v", err)
		}
		if fixture.run.Status != implementationstate.RunPaused {
			t.Fatalf("interrupted preflight did not pause: %#v", fixture.run)
		}
		if _, err := Resume(context.Background(), input); err != nil {
			t.Fatalf("recovered resume = %v", err)
		}
		if got, want := strings.Join(calls, ","), "lint,lint,test_all"; got != want {
			t.Fatalf("resume check order=%q, want %q", got, want)
		}
		for _, operation := range fixture.run.RunOperations {
			if operation.UncountedResumeCheck && len(operation.Attempts) != 0 {
				t.Fatalf("resume preflight consumed an attempt: %#v", operation)
			}
		}
	})

	t.Run("stale acceptance and a committed Git boundary recover without duplicate commits", func(t *testing.T) {
		// First establish that input refresh invalidates old acceptance evidence;
		// a later continuation must obtain fresh checks rather than treating it
		// as current work.
		fixture := newResumeFixture(t, "")
		prepareAcceptedRestartAssignment(t, fixture)
		if err := fixture.run.Pause("simulate restart after acceptance"); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
			t.Fatal(err)
		}
		configureAcceptanceRefresh(t, fixture, "compatible specification")
		if _, err := Resume(context.Background(), fixture.input()); err != nil {
			t.Fatal(err)
		}
		if err := CanStartTaskReview(fixture.run, "assignment"); !errors.Is(err, ErrTaskReviewNotReady) {
			t.Fatalf("stale acceptance remained reviewable: %v", err)
		}

		repository := newGitWorkspace(t)
		switchToBranch(t, repository, "implementation")
		run, state, journal := acceptanceReflectionFixture(t, repository)
		acceptCommitFixture(t, state, run)
		writeGitWorkspaceFile(t, filepath.Join(repository, "implementation.txt"), "accepted work survives recovery\n")
		preparation := captureCommitPreparation(t, repository)
		message, err := messageForImplementationCommit(run, "assignment", "commit-1", commitResponse(run, "commit-1", "commit accepted work"))
		if err != nil {
			t.Fatal(err)
		}
		intent := implementationstate.CommitIntent{OperationID: "commit-1", ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: message}
		if err := run.SetPendingCommitIntent("assignment", intent); err != nil {
			t.Fatal(err)
		}
		if _, err := state.Record(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		// Model a crash after local Git accepts the commit but before its result
		// reaches SQLite.  Drop the projection as well: recovery must replay
		// JSONL and adopt this exact commit, not create a second one.
		gitFixture(t, repository, "add", "--all")
		gitFixture(t, repository, "commit", "--quiet", "-m", message)
		created := strings.TrimSpace(gitFixture(t, repository, "rev-parse", "HEAD"))
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(state.DatabasePath()); err != nil {
			t.Fatal(err)
		}
		recoveredState, err := runstore.OpenState(journal)
		if err != nil {
			t.Fatal(err)
		}
		defer recoveredState.Close()
		recovered, _, err := recoveredState.Current(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		result, err := ReconcilePendingCommit(context.Background(), ReconcilePendingCommitInput{
			Run: recovered, StateStore: recoveredState, Repository: repository, AssignmentID: "assignment",
		})
		if err != nil || !result.Adopted || result.Commit.CommitID != created {
			t.Fatalf("recover committed boundary result=%#v err=%v", result, err)
		}
		if count := strings.TrimSpace(gitFixture(t, repository, "rev-list", "--count", "HEAD")); count != "2" {
			t.Fatalf("recovery duplicated commit: count=%s", count)
		}
		content, err := os.ReadFile(filepath.Join(repository, "implementation.txt"))
		if err != nil || string(content) != "accepted work survives recovery\n" || recovered.Assignments[0].Status != implementationstate.AssignmentCommitted {
			t.Fatalf("recovery lost allowed Git work: content=%q run=%#v err=%v", content, recovered, err)
		}
	})
}
