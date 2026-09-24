package state

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestAttemptCountersSeparateSemanticRoundsFromTechnicalRetries(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", testBrief()); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []Operation{
		{ID: "review-1", Kind: OperationReview, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterAssignmentReview},
		{ID: "review-2", Kind: OperationReview, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterAssignmentReview},
		{ID: "mandatory", Kind: OperationCheck, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterMandatoryChecks},
		{ID: "requested", Kind: OperationCheck, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterChecksRequested},
		{ID: "brief-refinement", Kind: OperationAgent, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterBriefRefinement},
		{ID: "explorer-1", Kind: OperationAgent, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterExplorer, Episode: "implementation"},
		{ID: "explorer-2", Kind: OperationAgent, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterExplorer, Episode: "implementation"},
		{ID: "explorer-next", Kind: OperationAgent, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterExplorer, Episode: "review"},
	} {
		if err := run.AddOperation("assignment", operation); err != nil {
			t.Fatalf("AddOperation(%s): %v", operation.ID, err)
		}
	}

	if got, err := run.StartAssignmentAttempt("assignment", "review-1"); err != nil || got != (OperationAttempt{Number: 1, SemanticRound: 1}) {
		t.Fatalf("first review attempt = %#v, %v", got, err)
	}
	if got, err := run.StartAssignmentAttempt("assignment", "review-1"); err != nil || got != (OperationAttempt{Number: 2, SemanticRound: 1}) {
		t.Fatalf("technical retry = %#v, %v; want same semantic round", got, err)
	}
	for _, id := range []OperationID{"review-2", "mandatory", "requested", "brief-refinement", "explorer-1", "explorer-2", "explorer-next"} {
		if _, err := run.StartAssignmentAttempt("assignment", id); err != nil {
			t.Fatalf("StartAssignmentAttempt(%s): %v", id, err)
		}
	}

	counters := run.Assignments[0].Counters
	if counters.AssignmentReview != 2 || counters.MandatoryChecks != 1 || counters.ChecksRequested != 1 || counters.BriefRefinement != 1 || counters.Explorer["implementation"] != 2 || counters.Explorer["review"] != 1 {
		t.Fatalf("independent counters = %#v", counters)
	}
	if err := run.AddBriefVersion("assignment", BriefVersion{ID: "brief-2", Number: 2, Document: testBrief().Document}); err != nil {
		t.Fatalf("brief revision rejected: %v", err)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("Validate() after a brief revision = %v", err)
	}

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var restarted Run
	if err := json.Unmarshal(data, &restarted); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Validate(); err != nil {
		t.Fatalf("restarted state is invalid: %v", err)
	}
	if got, err := restarted.StartAssignmentAttempt("assignment", "review-2"); err != nil || got != (OperationAttempt{Number: 2, SemanticRound: 2}) {
		t.Fatalf("review after restart = %#v, %v; want retained counter", got, err)
	}
}

func TestSupersedeRunOperationPreservesAuditAndRejectsAtomically(t *testing.T) {
	run := newSingleTaskRun(t)
	oldBasis := run.currentBasis()
	if err := run.AddRunOperation(Operation{ID: "select-old", Kind: OperationAgent, Basis: oldBasis, Description: "select next assignment", Counter: CycleCounterNone}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("select-old"); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordRunAttemptOutcome("select-old", AttemptInterrupted, "process stopped"); err != nil {
		t.Fatal(err)
	}
	run.Identity.Configuration = EvidenceRef{ID: "config-new", Digest: "config-new-digest"}
	want, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	invalid := Operation{ID: "select-new", Kind: OperationAgent, Basis: run.currentBasis(), Description: "different action", Counter: CycleCounterNone, Supersedes: "select-old"}
	if err := run.SupersedeRunOperation("select-old", invalid); err == nil {
		t.Fatal("invalid supersession was accepted")
	}
	got, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected supersession mutated state\n got: %s\nwant: %s", got, want)
	}
	replacement := Operation{ID: "select-new", Kind: OperationAgent, Basis: run.currentBasis(), Description: "select next assignment", Counter: CycleCounterNone, Supersedes: "select-old"}
	if err := run.SupersedeRunOperation("select-old", replacement); err != nil {
		t.Fatal(err)
	}
	if len(run.RunOperations) < 2 || run.RunOperations[len(run.RunOperations)-1].Supersedes != "select-old" || len(run.RunOperations[len(run.RunOperations)-2].Attempts) != 1 {
		t.Fatalf("supersession did not retain linked audit history: %#v", run.RunOperations)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("superseded run is invalid: %v", err)
	}
}

func TestFinalReviewTechnicalRetryDoesNotConsumeAnotherRound(t *testing.T) {
	run := newSingleTaskRun(t)
	for _, operation := range []Operation{
		{ID: "final-1", Kind: OperationReview, Basis: testBasis(), Counter: CycleCounterFinalReview},
		{ID: "final-2", Kind: OperationReview, Basis: testBasis(), Counter: CycleCounterFinalReview},
	} {
		if err := run.AddRunOperation(operation); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []struct {
		id      OperationID
		attempt OperationAttempt
	}{
		{"final-1", OperationAttempt{Number: 1, SemanticRound: 1}},
		{"final-1", OperationAttempt{Number: 2, SemanticRound: 1}},
		{"final-2", OperationAttempt{Number: 1, SemanticRound: 2}},
	} {
		if got, err := run.StartRunAttempt(want.id); err != nil || got != want.attempt {
			t.Fatalf("StartRunAttempt(%s) = %#v, %v; want %#v", want.id, got, err, want.attempt)
		}
	}
	if run.FinalReviewRounds != 2 {
		t.Fatalf("final review rounds = %d, want 2", run.FinalReviewRounds)
	}
	if err := run.AddRunResult(OperationResult{ID: "final-result", OperationID: "final-2", Status: ResultSucceeded, State: run.CurrentState, Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("final-2"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("technical retry after an operation result = %v, want invalid state", err)
	}
}

func TestLimitedAttemptPausesBeforeExceedingAndResumeResetsOnlyItsCause(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", testBrief()); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []Operation{
		{ID: "requested", Kind: OperationCheck, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterChecksRequested},
		{ID: "requested-2", Kind: OperationCheck, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterChecksRequested},
		{ID: "review-1", Kind: OperationReview, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterAssignmentReview},
		{ID: "review-2", Kind: OperationReview, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterAssignmentReview},
	} {
		if err := run.AddOperation("assignment", operation); err != nil {
			t.Fatal(err)
		}
	}
	limits := CycleLimits{AssignmentReview: 1, MandatoryChecks: 3, ChecksRequested: 1, BriefRefinement: 3, Explorer: 10, TechnicalAttempts: 3, FinalReview: 3}
	if got, err := run.StartAssignmentAttemptWithLimits("assignment", "requested", limits); err != nil || got.SemanticRound != 1 {
		t.Fatalf("requested check = %#v, %v", got, err)
	}
	if got, err := run.StartAssignmentAttemptWithLimits("assignment", "review-1", limits); err != nil || got != (OperationAttempt{Number: 1, SemanticRound: 1}) {
		t.Fatalf("last permitted review = %#v, %v", got, err)
	}
	if _, err := run.StartAssignmentAttemptWithLimits("assignment", "review-2", limits); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("attempt beyond review limit = %v, want limit pause", err)
	}
	if run.Status != RunPaused || run.LimitPause == nil || run.LimitPause.Counter != CycleCounterAssignmentReview || len(run.Assignments[0].Operations[3].Attempts) != 0 {
		t.Fatalf("limit pause must precede dispatch: %#v", run)
	}
	if err := run.Resume(); err != nil {
		t.Fatalf("resume after limit pause: %v", err)
	}
	if counters := run.Assignments[0].Counters; counters.AssignmentReview != 0 || counters.ChecksRequested != 1 {
		t.Fatalf("targeted reset = %#v", counters)
	}
	if got, err := run.StartAssignmentAttemptWithLimits("assignment", "review-2", limits); err != nil || got != (OperationAttempt{Number: 1, SemanticRound: 1}) {
		t.Fatalf("review in new counter cycle = %#v, %v", got, err)
	}
	if run.Assignments[0].Operations[3].SemanticCycle == run.Assignments[0].Operations[2].SemanticCycle {
		t.Fatalf("new review must retain a distinct historical cycle")
	}
	if _, err := run.StartAssignmentAttemptWithLimits("assignment", "requested-2", limits); !errors.Is(err, ErrLimitExceeded) || run.LimitPause == nil || run.LimitPause.Counter != CycleCounterChecksRequested {
		t.Fatalf("other exhausted counter must still pause: %v, %#v", err, run.LimitPause)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("state after targeted reset is invalid: %v", err)
	}
}

func TestSuccessfulMandatorySetAndExplorerEpisodeOpenOnlyTheirNewCycles(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", testBrief()); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []Operation{
		{ID: "mandatory-1", Kind: OperationCheck, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterMandatoryChecks},
		{ID: "mandatory-2", Kind: OperationCheck, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterMandatoryChecks},
		{ID: "refinement", Kind: OperationAgent, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterBriefRefinement},
		{ID: "explorer-1", Kind: OperationAgent, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterExplorer, Episode: "implementation"},
		{ID: "explorer-2", Kind: OperationAgent, BriefID: "brief-1", Basis: testBasis(), Counter: CycleCounterExplorer, Episode: "implementation"},
	} {
		if err := run.AddOperation("assignment", operation); err != nil {
			t.Fatal(err)
		}
	}
	limits := CycleLimits{AssignmentReview: 3, MandatoryChecks: 1, ChecksRequested: 5, BriefRefinement: 3, Explorer: 10, TechnicalAttempts: 3, FinalReview: 3}
	if counters := run.Assignments[0].Counters; counters.BriefRefinement != 0 {
		t.Fatalf("initial brief must not consume a refinement: %#v", counters)
	}
	if got, err := run.StartAssignmentAttemptWithLimits("assignment", "refinement", limits); err != nil || got.SemanticRound != 1 {
		t.Fatalf("first post-brief refinement = %#v, %v", got, err)
	}
	if _, err := run.StartAssignmentAttemptWithLimits("assignment", "mandatory-1", limits); err != nil {
		t.Fatal(err)
	}
	if err := run.AddResult("assignment", OperationResult{ID: "mandatory-result", OperationID: "mandatory-1", Status: ResultSucceeded, State: run.CurrentState, Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	if counters := run.Assignments[0].Counters; counters.MandatoryChecks != 0 || currentCycle(counters.MandatoryChecksCycle) != 2 {
		t.Fatalf("successful mandatory set did not finish its cycle: %#v", counters)
	}
	if got, err := run.StartAssignmentAttemptWithLimits("assignment", "mandatory-2", limits); err != nil || got.SemanticRound != 1 {
		t.Fatalf("mandatory set after review fixes = %#v, %v", got, err)
	}
	if _, err := run.StartAssignmentAttemptWithLimits("assignment", "explorer-1", limits); err != nil {
		t.Fatal(err)
	}
	if err := run.EndExplorerEpisode("assignment", "implementation"); err != nil {
		t.Fatal(err)
	}
	if got, err := run.StartAssignmentAttemptWithLimits("assignment", "explorer-2", limits); err != nil || got.SemanticRound != 1 {
		t.Fatalf("explorer in next episode = %#v, %v", got, err)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("cycle boundary state is invalid: %v", err)
	}
}

func TestFinalReviewLimitIncludesPrimaryRound(t *testing.T) {
	run := newSingleTaskRun(t)
	for _, id := range []OperationID{"final-1", "final-2"} {
		if err := run.AddRunOperation(Operation{ID: id, Kind: OperationReview, Basis: testBasis(), Counter: CycleCounterFinalReview}); err != nil {
			t.Fatal(err)
		}
	}
	limits := CycleLimits{AssignmentReview: 3, MandatoryChecks: 3, ChecksRequested: 5, BriefRefinement: 3, Explorer: 10, TechnicalAttempts: 3, FinalReview: 1}
	if got, err := run.StartRunAttemptWithLimits("final-1", limits); err != nil || got.SemanticRound != 1 {
		t.Fatalf("primary final review = %#v, %v", got, err)
	}
	if _, err := run.StartRunAttemptWithLimits("final-2", limits); !errors.Is(err, ErrLimitExceeded) || run.LimitPause == nil || run.LimitPause.Counter != CycleCounterFinalReview {
		t.Fatalf("second final review = %v, pause=%#v", err, run.LimitPause)
	}
	if err := run.Resume(); err != nil {
		t.Fatal(err)
	}
	if got, err := run.StartRunAttemptWithLimits("final-2", limits); err != nil || got.SemanticRound != 1 {
		t.Fatalf("final review after targeted reset = %#v, %v", got, err)
	}
}

func TestTechnicalLimitResumesTheSameOperationWithoutCreatingSemanticRound(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", testBrief()); err != nil {
		t.Fatal(err)
	}
	if err := run.AddOperation("assignment", Operation{ID: "agent", Kind: OperationAgent, BriefID: "brief-1", Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	limits := CycleLimits{AssignmentReview: 3, MandatoryChecks: 3, ChecksRequested: 5, BriefRefinement: 3, Explorer: 10, TechnicalAttempts: 1, FinalReview: 3}
	if got, err := run.StartAssignmentAttemptWithLimits("assignment", "agent", limits); err != nil || got.Number != 1 || got.SemanticRound != 0 {
		t.Fatalf("first technical attempt = %#v, %v", got, err)
	}
	if _, err := run.StartAssignmentAttemptWithLimits("assignment", "agent", limits); !errors.Is(err, ErrLimitExceeded) || run.LimitPause == nil || !run.LimitPause.Technical {
		t.Fatalf("technical limit = %v, pause=%#v", err, run.LimitPause)
	}
	if err := run.Resume(); err != nil {
		t.Fatal(err)
	}
	if got, err := run.StartAssignmentAttemptWithLimits("assignment", "agent", limits); err != nil || got.Number != 2 || got.SemanticRound != 0 {
		t.Fatalf("technical retry after targeted reset = %#v, %v", got, err)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("technical retry state is invalid: %v", err)
	}
}

func TestRunExplorerLimitsCoverPreBriefAndFinalReviewerEpisodes(t *testing.T) {
	run := newSingleTaskRun(t)
	for _, operation := range []Operation{
		{ID: "final-explorer", Kind: OperationAgent, Basis: testBasis(), Counter: CycleCounterExplorer, Episode: "final-reviewer"},
		{ID: "brief-explorer-1", Kind: OperationAgent, Basis: testBasis(), Counter: CycleCounterExplorer, Episode: "initial-briefer"},
		{ID: "brief-explorer-2", Kind: OperationAgent, Basis: testBasis(), Counter: CycleCounterExplorer, Episode: "initial-briefer"},
	} {
		if err := run.AddRunOperation(operation); err != nil {
			t.Fatal(err)
		}
	}
	limits := CycleLimits{AssignmentReview: 3, MandatoryChecks: 3, ChecksRequested: 5, BriefRefinement: 3, Explorer: 1, TechnicalAttempts: 3, FinalReview: 3}
	for _, operationID := range []OperationID{"final-explorer", "brief-explorer-1"} {
		if got, err := run.StartRunAttemptWithLimits(operationID, limits); err != nil || got.SemanticRound != 1 {
			t.Fatalf("last permitted run explorer %s = %#v, %v", operationID, got, err)
		}
	}
	if _, err := run.StartRunAttemptWithLimits("brief-explorer-2", limits); !errors.Is(err, ErrLimitExceeded) || run.LimitPause == nil || run.LimitPause.Episode != "initial-briefer" {
		t.Fatalf("pre-brief Explorer limit = %v, pause=%#v", err, run.LimitPause)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("paused run Explorer state is invalid: %v", err)
	}
	if err := run.Resume(); err != nil {
		t.Fatal(err)
	}
	if run.RunExplorerCounters["initial-briefer"] != 0 || run.RunExplorerCounters["final-reviewer"] != 1 {
		t.Fatalf("run Explorer reset was not episode-specific: %#v", run.RunExplorerCounters)
	}
	if got, err := run.StartRunAttemptWithLimits("brief-explorer-2", limits); err != nil || got.SemanticRound != 1 {
		t.Fatalf("run Explorer after reset = %#v, %v", got, err)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("resumed run Explorer state is invalid: %v", err)
	}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var restarted Run
	if err := json.Unmarshal(data, &restarted); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Validate(); err != nil {
		t.Fatalf("persisted run Explorer state is invalid: %v", err)
	}
}

func TestCounterNoneStillRecordsTechnicalAttemptsAndRequiresStartForResults(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment", testBrief()); err != nil {
		t.Fatal(err)
	}
	assignmentOperation := Operation{ID: "ordinary-agent", Kind: OperationAgent, BriefID: "brief-1", Basis: testBasis()}
	if err := run.AddOperation("assignment", assignmentOperation); err != nil {
		t.Fatal(err)
	}
	if err := run.AddResult("assignment", OperationResult{ID: "too-early", OperationID: "ordinary-agent", Status: ResultSucceeded, State: run.CurrentState, Basis: testBasis()}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("result without a recorded start = %v, want invalid state", err)
	}
	for number := uint64(1); number <= 2; number++ {
		if got, err := run.StartAssignmentAttempt("assignment", "ordinary-agent"); err != nil || got != (OperationAttempt{Number: number}) {
			t.Fatalf("assignment technical attempt %d = %#v, %v", number, got, err)
		}
	}
	if err := run.AddRunOperation(Operation{ID: "ordinary-run-agent", Kind: OperationAgent, Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(OperationResult{ID: "run-too-early", OperationID: "ordinary-run-agent", Status: ResultSucceeded, State: run.CurrentState, Basis: testBasis()}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("run result without a recorded start = %v, want invalid state", err)
	}
	if got, err := run.StartRunAttempt("ordinary-run-agent"); err != nil || got != (OperationAttempt{Number: 1}) {
		t.Fatalf("run technical attempt = %#v, %v", got, err)
	}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var restarted Run
	if err := json.Unmarshal(data, &restarted); err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.StartAssignmentAttempt("assignment", "ordinary-agent"); err != nil || got != (OperationAttempt{Number: 3}) {
		t.Fatalf("assignment technical retry after JSON restart = %#v, %v", got, err)
	}
	if got, err := restarted.StartRunAttempt("ordinary-run-agent"); err != nil || got != (OperationAttempt{Number: 2}) {
		t.Fatalf("run technical retry after JSON restart = %#v, %v", got, err)
	}
}

func TestUncountedResumeCheckIsTheOnlyResultWithoutAttemptException(t *testing.T) {
	run := newSingleTaskRun(t)
	operation := Operation{ID: "resume-check", Kind: OperationCheck, Basis: testBasis(), UncountedResumeCheck: true}
	if err := run.AddRunOperation(operation); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(OperationResult{ID: "resume-result", OperationID: "resume-check", Status: ResultSucceeded, State: run.CurrentState, Basis: testBasis()}); err != nil {
		t.Fatalf("uncounted resume result: %v", err)
	}
	if _, err := run.StartRunAttempt("resume-check"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("start of uncounted resume check = %v, want invalid state", err)
	}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAssignmentTransitionsRequireAcceptanceThenCommit(t *testing.T) {
	run := newTestRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task-a", "task-b"}); err != nil {
		t.Fatalf("StartAssignment() error = %v", err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")

	if got, _ := run.TaskStatus("parent"); got != TaskPending {
		t.Fatalf("parent status after acceptance = %q, want pending", got)
	}
	if got, _ := run.TaskStatus("task-a"); got != TaskAcceptedAwaitingCommit {
		t.Fatalf("leaf status after acceptance = %q, want accepted awaiting commit", got)
	}
	if err := run.Succeed(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Succeed() before commit error = %v, want transition error", err)
	}

	commit := CommitEvidence{OperationID: "commit-1", CommitID: "commit-sha", ParentCommit: "base", Tree: "tree", Message: "implement tasks", State: acceptedState(), Basis: testBasis()}
	if err := run.CommitAssignment("assignment-1", commit); err != nil {
		t.Fatalf("CommitAssignment() error = %v", err)
	}
	for _, taskID := range []TaskID{"task-a", "task-b", "parent"} {
		if got, _ := run.TaskStatus(taskID); got != TaskComplete {
			t.Errorf("TaskStatus(%q) = %q, want complete", taskID, got)
		}
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("Validate() after commit = %v", err)
	}
}

func TestAssignmentRequiresWholeContiguousPendingLeafPrefix(t *testing.T) {
	run := newTestRun(t)
	for _, selection := range [][]TaskID{{"parent"}, {"task-b"}, {"task-a", "task-c"}, nil} {
		if err := run.StartAssignment("bad", selection); !errors.Is(err, ErrInvalidState) {
			t.Errorf("StartAssignment(%v) error = %v, want invalid state", selection, err)
		}
	}
	if err := run.StartAssignment("good", []TaskID{"task-a", "task-b"}); err != nil {
		t.Fatalf("contiguous leaf prefix rejected: %v", err)
	}
}

func TestStartAssignmentRequiresDurableCurrentInitialBaseline(t *testing.T) {
	run, err := NewRun(RunIdentity{ID: "run", Change: "change", Repository: "/repo", WorkCopy: "/repo", Branch: "feature", BaselineCommit: "base", BaselineState: acceptedState(), Specification: testBasis().Specification, TaskList: EvidenceRef{ID: "tasks", Digest: "tasks-digest"}, Configuration: testBasis().Configuration}, []Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("assignment", []TaskID{"task"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("assignment without initial baseline error = %v, want invalid transition", err)
	}
	recordInitialBaseline(t, run, "initial-baseline", "initial-baseline-result")
	if err := run.StartAssignment("assignment", []TaskID{"task"}); err != nil {
		t.Fatalf("assignment after durable initial baseline: %v", err)
	}

	invalid := newSingleTaskRun(t)
	invalid.InitialBaseline = nil
	invalid.Assignments = append(invalid.Assignments, Assignment{ID: "assignment", TaskIDs: []TaskID{"task"}, Status: AssignmentActive})
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("restored assignment without baseline error = %v, want invalid state", err)
	}
}

func TestCommittedFirstAssignmentAllowsNextAssignmentAfterBaselineStateChanges(t *testing.T) {
	run := newTestRun(t)
	if err := run.StartAssignment("first", []TaskID{"task-a"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "first")
	state := run.CurrentState
	intent := testCommitIntent()
	if err := run.CommitAssignment("first", CommitEvidence{OperationID: intent.OperationID, CommitID: "commit-a", ParentCommit: intent.ParentCommit, Tree: intent.Tree, Message: intent.Message, State: state, Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	changed := EvidenceRef{ID: "code-after-first", Digest: "code-after-first-digest"}
	if err := run.ObserveCodeState(changed); err != nil {
		t.Fatal(err)
	}
	if run.InitialBaseline == nil || run.InitialBaseline.State == run.CurrentState {
		t.Fatalf("test did not establish changed post-baseline state: %#v", run)
	}
	if err := run.StartAssignment("second", []TaskID{"task-b"}); err != nil {
		t.Fatalf("next assignment after committed first assignment: %v", err)
	}
}

func TestAcceptanceRequiresResultsForSameBriefAndExactState(t *testing.T) {
	run := newTestRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task-a"}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddBriefVersion("assignment-1", testBrief()); err != nil {
		t.Fatal(err)
	}
	if err := run.AddOperation("assignment-1", Operation{ID: "check", Kind: OperationCheck, BriefID: "brief-1", Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddOperation("assignment-1", Operation{ID: "review", Kind: OperationReview, BriefID: "brief-1", Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []OperationID{"check", "review"} {
		if _, err := run.StartAssignmentAttempt("assignment-1", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.AddResult("assignment-1", OperationResult{ID: "check-result", OperationID: "check", Status: ResultSucceeded, State: acceptedState(), Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	wrongState := EvidenceRef{ID: "code-2", Digest: "digest-2"}
	if err := run.AddResult("assignment-1", OperationResult{ID: "review-result", OperationID: "review", Status: ResultSucceeded, State: wrongState, Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	evidence := AcceptanceEvidence{BriefID: "brief-1", State: acceptedState(), Basis: testBasis(), CheckResultIDs: []ResultID{"check-result"}, ReviewResultID: "review-result", PendingCommit: testCommitIntent()}
	if err := run.AcceptAssignment("assignment-1", evidence); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("AcceptAssignment() error = %v, want invalid exact-state evidence", err)
	}
}

func TestPauseResumesButClosedAndSucceededRunsDoNot(t *testing.T) {
	run := newTestRun(t)
	if err := run.Pause("environment"); err != nil {
		t.Fatal(err)
	}
	if err := run.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := run.Close("needs a product decision"); err != nil {
		t.Fatal(err)
	}
	if err := run.Resume(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Resume() closed run error = %v", err)
	}
	if err := run.Succeed(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Succeed() closed run error = %v", err)
	}
}

func TestExecutionBlockedIsResumableButPreservesUserRemediationDetails(t *testing.T) {
	run := newTestRun(t)
	block := ExecutionBlock{
		BlockedAction:      "run required checks",
		Diagnostic:         "configured compiler is not installed",
		Attempts:           []string{"checked PATH", "read project settings"},
		RequiredUserAction: "install the configured compiler",
	}
	if err := run.PauseExecutionBlocked(block); err != nil {
		t.Fatal(err)
	}
	if run.Status != RunPaused || run.ExecutionBlock == nil || !reflect.DeepEqual(*run.ExecutionBlock, block) || run.PauseReason != "execution_blocked: run required checks" {
		t.Fatalf("execution block was not retained: %#v", run)
	}
	// A block pauses dependent work; it cannot be converted into a new scope
	// or a relaxed acceptance by a state transition.
	if err := run.StartAssignment("assignment", []TaskID{"task-a"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("blocked run started assignment: %v", err)
	}
	event, err := NewRunStateEvent(1, run)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := event.Apply(nil)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ExecutionBlock == nil || !reflect.DeepEqual(*restored.ExecutionBlock, block) {
		t.Fatalf("durable execution block = %#v", restored.ExecutionBlock)
	}
	if err := restored.Resume(); err != nil {
		t.Fatal(err)
	}
	if restored.ExecutionBlock != nil {
		t.Fatalf("resume retained already-remediated execution block: %#v", restored.ExecutionBlock)
	}
}

func TestSucceedRejectsTasksWithoutCommitEvidence(t *testing.T) {
	run := newTestRun(t)
	for _, taskID := range run.PendingLeafTasks() {
		run.LeafStatus[taskID] = TaskComplete
	}
	if err := run.Succeed(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Succeed() with fabricated completed tasks error = %v, want transition error", err)
	}
}

func TestCommitRejectsAcceptedEvidenceAfterSpecificationOrConfigurationChanges(t *testing.T) {
	for _, replace := range []struct {
		name  string
		apply func(*Run)
	}{
		{"specification", func(run *Run) { run.Identity.Specification = EvidenceRef{ID: "spec-2", Digest: "spec-digest-2"} }},
		{"configuration", func(run *Run) { run.Identity.Configuration = EvidenceRef{ID: "config-2", Digest: "config-digest-2"} }},
	} {
		t.Run(replace.name, func(t *testing.T) {
			run := newSingleTaskRun(t)
			if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
				t.Fatal(err)
			}
			prepareAcceptedAssignment(t, run, "assignment-1")
			replace.apply(run)
			if err := run.Validate(); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("Validate() stale pending acceptance error = %v, want invalid state", err)
			}
			commit := CommitEvidence{OperationID: "commit-1", CommitID: "commit-sha", ParentCommit: "base", Tree: "tree", Message: "implement tasks", State: acceptedState(), Basis: testBasis()}
			if err := run.CommitAssignment("assignment-1", commit); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("CommitAssignment() stale basis error = %v, want transition error", err)
			}
		})
	}
}

func TestValidatePreservesCommittedHistoricalEvidenceAfterInputsChange(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")
	if err := run.CommitAssignment("assignment-1", CommitEvidence{OperationID: "commit-1", CommitID: "commit-sha", ParentCommit: "base", Tree: "tree", Message: "implement tasks", State: acceptedState(), Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	run.Identity.Configuration = EvidenceRef{ID: "config-2", Digest: "config-digest-2"}
	if err := run.Validate(); err != nil {
		t.Fatalf("Validate() historical committed evidence error = %v", err)
	}
}

func TestReopenAssignmentPreservesEvidenceAndAllowsNewBasisAfterInputAndCodeChange(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")
	run.Identity.Configuration = EvidenceRef{ID: "config-2", Digest: "config-digest-2"}
	changedState := EvidenceRef{ID: "code-2", Digest: "digest-2"}
	if err := run.ReopenAssignment("assignment-1", changedState); err != nil {
		t.Fatalf("ReopenAssignment() error = %v", err)
	}
	assignment := run.Assignments[0]
	if assignment.Status != AssignmentActive || assignment.Acceptance != nil || len(assignment.AcceptanceHistory) != 1 || run.LeafStatus["task"] != TaskPending {
		t.Fatalf("reopened assignment = %#v, leaf = %q", assignment, run.LeafStatus["task"])
	}
	basis := run.currentBasis()
	brief := BriefVersion{ID: "brief-2", Number: 2, Document: EvidenceRef{ID: "brief-document-2", Digest: "brief-digest-2"}}
	if err := run.AddBriefVersion("assignment-1", brief); err != nil {
		t.Fatalf("AddBriefVersion() after reopen error = %v", err)
	}
	for _, operation := range []Operation{{ID: "check-2", Kind: OperationCheck, BriefID: "brief-2", Basis: basis}, {ID: "review-2", Kind: OperationReview, BriefID: "brief-2", Basis: basis}} {
		if err := run.AddOperation("assignment-1", operation); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []OperationID{"check-2", "review-2"} {
		if _, err := run.StartAssignmentAttempt("assignment-1", id); err != nil {
			t.Fatal(err)
		}
	}
	for _, result := range []OperationResult{{ID: "check-result-2", OperationID: "check-2", Status: ResultSucceeded, State: changedState, Basis: basis}, {ID: "review-result-2", OperationID: "review-2", Status: ResultSucceeded, State: changedState, Basis: basis}} {
		if err := run.AddResult("assignment-1", result); err != nil {
			t.Fatal(err)
		}
	}
	acceptance := AcceptanceEvidence{BriefID: "brief-2", State: changedState, Basis: basis, CheckResultIDs: []ResultID{"check-result-2"}, ReviewResultID: "review-result-2", PendingCommit: CommitIntent{OperationID: "commit-2", ParentCommit: "base", Tree: "tree-2", Message: "implement changed task"}}
	if err := run.AcceptAssignment("assignment-1", acceptance); err != nil {
		t.Fatalf("AcceptAssignment() with refreshed evidence error = %v", err)
	}
	commit := CommitEvidence{OperationID: "commit-2", CommitID: "commit-2", ParentCommit: "base", Tree: "tree-2", Message: "implement changed task", State: changedState, Basis: basis}
	if err := run.CommitAssignment("assignment-1", commit); err != nil {
		t.Fatalf("CommitAssignment() with refreshed evidence error = %v", err)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("Validate() after reacceptance error = %v", err)
	}
}

func TestObserveCodeStateInvalidatesAcceptedAssignmentAndFinalAcceptance(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")
	changedState := EvidenceRef{ID: "code-2", Digest: "digest-2"}
	if err := run.ObserveCodeState(changedState); err != nil {
		t.Fatal(err)
	}
	assignment := run.Assignments[0]
	if assignment.Status != AssignmentActive || assignment.Acceptance != nil || len(assignment.AcceptanceHistory) != 1 || run.LeafStatus["task"] != TaskPending {
		t.Fatalf("ObserveCodeState() did not reopen accepted assignment: %#v, leaf = %q", assignment, run.LeafStatus["task"])
	}

	run = newSingleTaskRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")
	if err := run.CommitAssignment("assignment-1", CommitEvidence{OperationID: "commit-1", CommitID: "commit-sha", ParentCommit: "base", Tree: "tree", Message: "implement tasks", State: acceptedState(), Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	final := addFinalEvidence(t, run, "final", acceptedState())
	if err := run.RecordFinalAcceptance(final); err != nil {
		t.Fatal(err)
	}
	if err := run.ObserveCodeState(changedState); err != nil {
		t.Fatal(err)
	}
	if run.FinalAcceptance != nil || len(run.FinalAcceptanceHistory) != 1 {
		t.Fatalf("ObserveCodeState() did not invalidate final acceptance: %#v", run)
	}
}

func TestValidateRejectsRestoredAcceptedAssignmentWithStaleCodeState(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")
	run.CurrentState = EvidenceRef{ID: "changed-code", Digest: "changed-code-digest"}
	if err := run.Validate(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Validate() stale pending code state error = %v, want invalid state", err)
	}
}

func TestRefreshAcceptanceInputsAtomicallyReopensPendingAndArchivesFinalEvidence(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")
	newConfig := EvidenceRef{ID: "config-2", Digest: "config-digest-2"}
	if err := run.RefreshAcceptanceInputs(testBasis().Specification, newConfig); err != nil {
		t.Fatal(err)
	}
	assignment := run.Assignments[0]
	if assignment.Status != AssignmentActive || assignment.Acceptance != nil || len(assignment.AcceptanceHistory) != 1 || run.LeafStatus["task"] != TaskPending {
		t.Fatalf("RefreshAcceptanceInputs() did not reopen pending assignment: %#v", assignment)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("Validate() after input refresh error = %v", err)
	}

	run = newSingleTaskRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")
	if err := run.CommitAssignment("assignment-1", CommitEvidence{OperationID: "commit-1", CommitID: "commit-sha", ParentCommit: "base", Tree: "tree", Message: "implement tasks", State: acceptedState(), Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	final := addFinalEvidence(t, run, "final", acceptedState())
	if err := run.RecordFinalAcceptance(final); err != nil {
		t.Fatal(err)
	}
	if err := run.RefreshAcceptanceInputs(testBasis().Specification, newConfig); err != nil {
		t.Fatal(err)
	}
	if run.FinalAcceptance != nil || len(run.FinalAcceptanceHistory) != 1 {
		t.Fatalf("RefreshAcceptanceInputs() did not archive final evidence: %#v", run)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("Validate() with archived final evidence error = %v", err)
	}

	basis := run.currentBasis()
	refreshed := addFinalEvidence(t, run, "refreshed", acceptedState())
	refreshed.Basis = basis
	if err := run.RecordFinalAcceptance(refreshed); err != nil {
		t.Fatalf("RecordFinalAcceptance() refreshed basis error = %v", err)
	}
	if len(run.FinalAcceptanceHistory) != 1 {
		t.Fatalf("refreshed final acceptance should retain exactly prior final history, got %d", len(run.FinalAcceptanceHistory))
	}
}

func TestSucceedRequiresFinalChecksReviewAndNoOpenFindings(t *testing.T) {
	run := newSingleTaskRun(t)
	if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")
	if err := run.CommitAssignment("assignment-1", CommitEvidence{OperationID: "commit-1", CommitID: "commit-sha", ParentCommit: "base", Tree: "tree", Message: "implement tasks", State: acceptedState(), Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	if err := run.Succeed(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Succeed() without final evidence error = %v", err)
	}
	for _, operation := range []Operation{{ID: "final-check", Kind: OperationCheck, Basis: testBasis()}, {ID: "final-review", Kind: OperationReview, Basis: testBasis()}} {
		if err := run.AddRunOperation(operation); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []OperationID{"final-check", "final-review"} {
		if _, err := run.StartRunAttempt(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, result := range []OperationResult{{ID: "final-check-result", OperationID: "final-check", Status: ResultSucceeded, State: acceptedState(), Basis: testBasis()}, {ID: "final-review-result", OperationID: "final-review", Status: ResultSucceeded, State: acceptedState(), Basis: testBasis()}} {
		if err := run.AddRunResult(result); err != nil {
			t.Fatal(err)
		}
	}
	withFinding := FinalAcceptanceEvidence{State: acceptedState(), Basis: testBasis(), CheckResultIDs: []ResultID{"final-check-result"}, ReviewResultID: "final-review-result", OpenFindingIDs: []EvidenceID{"finding-1"}}
	if err := run.RecordFinalAcceptance(withFinding); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("RecordFinalAcceptance() open finding error = %v", err)
	}
	accepted := withFinding
	accepted.OpenFindingIDs = nil
	if err := run.RecordFinalAcceptance(accepted); err != nil {
		t.Fatal(err)
	}
	if err := run.Succeed(); err != nil {
		t.Fatalf("Succeed() with final evidence error = %v", err)
	}
}

func TestFinalAcceptanceCannotBeRecordedOrRestoredBeforeTasksAndCannotSurviveCodeChange(t *testing.T) {
	run := newSingleTaskRun(t)
	preTaskState := EvidenceRef{ID: "pre-task", Digest: "pre-task-digest"}
	if err := run.ObserveCodeState(preTaskState); err != nil {
		t.Fatal(err)
	}
	finalEvidence := addFinalEvidence(t, run, "pre", preTaskState)
	if err := run.RecordFinalAcceptance(finalEvidence); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("RecordFinalAcceptance() before tasks error = %v", err)
	}
	run.FinalAcceptance = cloneFinalAcceptance(finalEvidence)
	if err := run.Validate(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Validate() restored final evidence before tasks error = %v", err)
	}
	run.FinalAcceptance = nil

	committedState := EvidenceRef{ID: "committed", Digest: "committed-digest"}
	if err := run.ObserveCodeState(committedState); err != nil {
		t.Fatal(err)
	}
	refreshInitialBaseline(t, run, "committed")
	if err := run.StartAssignment("assignment-1", []TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "assignment-1")
	if err := run.CommitAssignment("assignment-1", CommitEvidence{OperationID: "commit-1", CommitID: "commit-sha", ParentCommit: "base", Tree: "tree", Message: "implement tasks", State: committedState, Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordFinalAcceptance(finalEvidence); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("RecordFinalAcceptance() stale pre-task code evidence error = %v", err)
	}
}

func TestValidateRejectsInvalidRestoredLifecycleAndAssignmentOrdering(t *testing.T) {
	for _, test := range []struct {
		name  string
		apply func(*Run)
	}{
		{"paused without reason", func(run *Run) { run.Status = RunPaused }},
		{"closed without reason", func(run *Run) { run.Status = RunClosed }},
		{"succeeded with pending leaf", func(run *Run) { run.Status = RunSucceeded }},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := newTestRun(t)
			test.apply(run)
			if err := run.Validate(); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("Validate() error = %v, want invalid state", err)
			}
		})
	}

	run := newTestRun(t)
	if err := run.StartAssignment("first", []TaskID{"task-a"}); err != nil {
		t.Fatal(err)
	}
	prepareAcceptedAssignment(t, run, "first")
	if err := run.CommitAssignment("first", CommitEvidence{OperationID: "commit-1", CommitID: "commit-a", ParentCommit: "base", Tree: "tree", Message: "implement tasks", State: acceptedState(), Basis: testBasis()}); err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("second", []TaskID{"task-b"}); err != nil {
		t.Fatal(err)
	}
	run.Assignments[0], run.Assignments[1] = run.Assignments[1], run.Assignments[0]
	if err := run.Validate(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Validate() open-before-committed assignment order error = %v", err)
	}
}

func TestValidateRejectsPersistedParentStatusAndPartialLeaf(t *testing.T) {
	run := newTestRun(t)
	run.LeafStatus["parent"] = TaskComplete
	if err := run.Validate(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Validate() parent state error = %v", err)
	}

	run = newTestRun(t)
	run.LeafStatus["task-a"] = TaskAcceptedAwaitingCommit
	if err := run.Validate(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Validate() unattached accepted leaf error = %v", err)
	}
}

func newTestRun(t *testing.T) *Run {
	t.Helper()
	run, err := NewRun(RunIdentity{ID: "run-1", Change: "change", Repository: "/repo", WorkCopy: "/repo", Branch: "feature", BaselineCommit: "base", BaselineState: acceptedState(), Specification: EvidenceRef{ID: "spec", Digest: "spec-digest"}, TaskList: EvidenceRef{ID: "tasks", Digest: "tasks-digest"}, Configuration: EvidenceRef{ID: "config", Digest: "config-digest"}}, []Task{
		{ID: "parent", Order: 0, Title: "parent"},
		{ID: "task-a", ParentID: "parent", Order: 1, Title: "A"},
		{ID: "task-b", ParentID: "parent", Order: 2, Title: "B"},
		{ID: "task-c", Order: 3, Title: "C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	seedInitialBaseline(t, run)
	return run
}

func newSingleTaskRun(t *testing.T) *Run {
	t.Helper()
	run, err := NewRun(RunIdentity{ID: "run-1", Change: "change", Repository: "/repo", WorkCopy: "/repo", Branch: "feature", BaselineCommit: "base", BaselineState: acceptedState(), Specification: testBasis().Specification, TaskList: EvidenceRef{ID: "tasks", Digest: "tasks-digest"}, Configuration: testBasis().Configuration}, []Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	seedInitialBaseline(t, run)
	return run
}

func seedInitialBaseline(t *testing.T, run *Run) {
	t.Helper()
	recordInitialBaseline(t, run, "initial-baseline", "initial-baseline-result")
}

func refreshInitialBaseline(t *testing.T, run *Run, suffix string) {
	t.Helper()
	recordInitialBaseline(t, run, OperationID("initial-baseline-"+suffix), ResultID("initial-baseline-"+suffix+"-result"))
}

func recordInitialBaseline(t *testing.T, run *Run, operationID OperationID, resultID ResultID) {
	t.Helper()
	basis := AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	if err := run.AddRunOperation(Operation{ID: operationID, Kind: OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt(operationID); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(OperationResult{ID: resultID, OperationID: operationID, Status: ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordInitialBaselinePass(operationID, resultID); err != nil {
		t.Fatal(err)
	}
}

func prepareAcceptedAssignment(t *testing.T, run *Run, assignmentID AssignmentID) {
	t.Helper()
	state := run.CurrentState
	if err := run.AddBriefVersion(assignmentID, testBrief()); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []Operation{{ID: "check", Kind: OperationCheck, BriefID: "brief-1", Basis: testBasis()}, {ID: "review", Kind: OperationReview, BriefID: "brief-1", Basis: testBasis()}} {
		if err := run.AddOperation(assignmentID, operation); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []OperationID{"check", "review"} {
		if _, err := run.StartAssignmentAttempt(assignmentID, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, result := range []OperationResult{{ID: "check-result", OperationID: "check", Status: ResultSucceeded, State: state, Basis: testBasis()}, {ID: "review-result", OperationID: "review", Status: ResultSucceeded, State: state, Basis: testBasis()}} {
		if err := run.AddResult(assignmentID, result); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.AcceptAssignment(assignmentID, AcceptanceEvidence{BriefID: "brief-1", State: state, Basis: testBasis(), CheckResultIDs: []ResultID{"check-result"}, ReviewResultID: "review-result", PendingCommit: testCommitIntent()}); err != nil {
		t.Fatal(err)
	}
}

func testBrief() BriefVersion {
	return BriefVersion{ID: "brief-1", Number: 1, Document: EvidenceRef{ID: "brief-document", Digest: "brief-digest"}}
}

func acceptedState() EvidenceRef {
	return EvidenceRef{ID: "code-1", Digest: "digest-1"}
}

func testBasis() AcceptanceBasis {
	return AcceptanceBasis{Specification: EvidenceRef{ID: "spec", Digest: "spec-digest"}, Configuration: EvidenceRef{ID: "config", Digest: "config-digest"}}
}

func testCommitIntent() CommitIntent {
	return CommitIntent{OperationID: "commit-1", ParentCommit: "base", Tree: "tree", Message: "implement tasks"}
}

func addFinalEvidence(t *testing.T, run *Run, prefix string, state EvidenceRef) FinalAcceptanceEvidence {
	t.Helper()
	basis := run.currentBasis()
	checkID, reviewID := OperationID(prefix+"-check"), OperationID(prefix+"-review")
	checkResultID, reviewResultID := ResultID(prefix+"-check-result"), ResultID(prefix+"-review-result")
	if err := run.AddRunOperation(Operation{ID: checkID, Kind: OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunOperation(Operation{ID: reviewID, Kind: OperationReview, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []OperationID{checkID, reviewID} {
		if _, err := run.StartRunAttempt(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.AddRunResult(OperationResult{ID: checkResultID, OperationID: checkID, Status: ResultSucceeded, State: state, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(OperationResult{ID: reviewResultID, OperationID: reviewID, Status: ResultSucceeded, State: state, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	return FinalAcceptanceEvidence{State: state, Basis: basis, CheckResultIDs: []ResultID{checkResultID}, ReviewResultID: reviewResultID}
}
