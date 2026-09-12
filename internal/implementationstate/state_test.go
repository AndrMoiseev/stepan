package implementationstate

import (
	"errors"
	"testing"
)

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
	return run
}

func newSingleTaskRun(t *testing.T) *Run {
	t.Helper()
	run, err := NewRun(RunIdentity{ID: "run-1", Change: "change", Repository: "/repo", WorkCopy: "/repo", Branch: "feature", BaselineCommit: "base", BaselineState: acceptedState(), Specification: testBasis().Specification, TaskList: EvidenceRef{ID: "tasks", Digest: "tasks-digest"}, Configuration: testBasis().Configuration}, []Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	return run
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
	if err := run.AddRunResult(OperationResult{ID: checkResultID, OperationID: checkID, Status: ResultSucceeded, State: state, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(OperationResult{ID: reviewResultID, OperationID: reviewID, Status: ResultSucceeded, State: state, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	return FinalAcceptanceEvidence{State: state, Basis: basis, CheckResultIDs: []ResultID{checkResultID}, ReviewResultID: reviewResultID}
}
