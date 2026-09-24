package specflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFlowStateRoundTripsIndependentStatusAxesAndMetadata(t *testing.T) {
	t.Parallel()

	stageStatuses := []StageStatus{StageNotStarted, StageDrafting, StagePublished, StageCommitted}
	reviewStatuses := []ReviewStatus{
		ReviewNotStarted,
		ReviewRunning,
		ReviewAwaitingDecisions,
		ReviewAutomaticRework,
		ReviewEscalated,
		ReviewCompleted,
	}
	for _, stageStatus := range stageStatuses {
		for _, reviewStatus := range reviewStatuses {
			stageStatus, reviewStatus := stageStatus, reviewStatus
			t.Run(string(stageStatus)+"/"+string(reviewStatus), func(t *testing.T) {
				t.Parallel()
				state := populatedFlowState(t, stageStatus, reviewStatus)
				encoded, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range []string{"commit_sha", "operation_id", "thread_handle", "provider", "model", "prompt"} {
					if strings.Contains(string(encoded), forbidden) {
						t.Errorf("state contains forbidden field %q: %s", forbidden, encoded)
					}
				}
				decoded, err := DecodeFlowState(encoded)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(decoded.Snapshot(), state.Snapshot()) {
					t.Fatalf("round trip changed state\n got: %#v\nwant: %#v", decoded.Snapshot(), state.Snapshot())
				}
			})
		}
	}
}

func TestFlowStateRejectsInvalidValuesAndTransitions(t *testing.T) {
	t.Parallel()

	base := NewFlowState().Snapshot()
	tests := map[string]func(*FlowStateSnapshot){
		"version":       func(value *FlowStateSnapshot) { value.Version = 0 },
		"flow status":   func(value *FlowStateSnapshot) { value.FlowStatus = "paused" },
		"current stage": func(value *FlowStateSnapshot) { value.CurrentStage = "implementation" },
		"missing stage": func(value *FlowStateSnapshot) { delete(value.Stages, StagePlan) },
		"stage status": func(value *FlowStateSnapshot) {
			stage := value.Stages[StageIntent]
			stage.Status = "approved"
			value.Stages[StageIntent] = stage
		},
		"review status": func(value *FlowStateSnapshot) {
			stage := value.Stages[StageIntent]
			stage.ReviewStatus = "paused"
			value.Stages[StageIntent] = stage
		},
		"retry counter": func(value *FlowStateSnapshot) {
			stage := value.Stages[StageIntent]
			stage.RetryCounters.ParserRepair = DefaultRetryLimit + 1
			value.Stages[StageIntent] = stage
		},
		"zero issued ID": func(value *FlowStateSnapshot) { value.IssuedIDs = []StableID{{}} },
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			value := cloneFlowStateSnapshot(base)
			mutate(&value)
			if _, err := NewFlowStateFromSnapshot(value); !errors.Is(err, ErrInvalidDomainValue) {
				t.Fatalf("NewFlowStateFromSnapshot error = %v", err)
			}
		})
	}

	state := NewFlowState()
	if err := state.CanAdvanceTo(StageSpec); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("advance before committed intent error = %v", err)
	}
	if err := state.CanAdvanceTo(StagePlan); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("skipped stage error = %v", err)
	}
	if err := state.CanAdvanceTo(StageIntent); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("same stage advance error = %v", err)
	}
}

func TestFlowStateSnapshotCannotMutateOwnedState(t *testing.T) {
	t.Parallel()

	state := NewFlowState()
	snapshot := state.Snapshot()
	intent := snapshot.Stages[StageIntent]
	intent.Status = StageCommitted
	snapshot.Stages[StageIntent] = intent
	snapshot.IssuedIDs = append(snapshot.IssuedIDs, mustStableID(t, "REQ-1"))

	owned, _ := state.Stage(StageIntent)
	if owned.Status != StageDrafting || len(state.IssuedIDs()) != 0 {
		t.Fatalf("external snapshot mutated owned state: %#v", state.Snapshot())
	}
}

func TestDecodeFlowStateRejectsForbiddenOrUnknownFields(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(NewFlowState())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"commit_sha", "operation_id", "thread_handle", "prompt_metadata"} {
		withUnknown := strings.TrimSuffix(string(encoded), "}") + `,"` + field + `":"forbidden"}`
		if _, err := DecodeFlowState([]byte(withUnknown)); err == nil {
			t.Errorf("DecodeFlowState accepted %s", field)
		}
	}
}

func TestProgressCarriesOrderedCommandHints(t *testing.T) {
	t.Parallel()

	review, err := NewCommandHint("/review", "Run agent review")
	if err != nil {
		t.Fatal(err)
	}
	approve, err := NewCommandHint("/approve", "Approve current stage")
	if err != nil {
		t.Fatal(err)
	}
	hints := []CommandHint{review, approve}
	progress, err := NewProgress(NewFlowState(), hints, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(progress.CommandHints, hints) || progress.CurrentStage != StageIntent || progress.StageStatus != StageDrafting || !progress.TextAllowed {
		t.Fatalf("progress = %#v", progress)
	}
	hints[0].Command = "/changed"
	if progress.CommandHints[0].Command != "/review" {
		t.Fatal("progress did not retain its own ordered command hint list")
	}
	if _, err := NewCommandHint("/review", " "); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("empty hint error = %v", err)
	}
}

func populatedFlowState(t *testing.T, stageStatus StageStatus, reviewStatus ReviewStatus) FlowState {
	t.Helper()
	original, err := NewFingerprint("spec-current", []UpstreamHash{{Stage: StageIntent, Hash: "intent-approved"}})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := NewFingerprint("spec-updated", []UpstreamHash{{Stage: StageIntent, Hash: "intent-approved"}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewFlowStateFromSnapshot(FlowStateSnapshot{
		Version:      FlowStateVersion,
		FlowStatus:   FlowActive,
		CurrentStage: StageSpec,
		Stages: map[Stage]StageState{
			StageIntent: {
				Status: StageCommitted, ReviewStatus: ReviewNotStarted,
				CurrentHash: "intent-current", ApprovedHash: "intent-approved",
				UpstreamHashes: []UpstreamHash{}, Reviews: []ReviewRun{},
			},
			StageSpec: {
				Status: stageStatus, ReviewStatus: reviewStatus,
				CurrentHash: "spec-current", ApprovedHash: "spec-approved",
				UpstreamHashes: []UpstreamHash{{Stage: StageIntent, Hash: "intent-approved"}},
				Outdated:       true, RetryCounters: RetryCounters{ParserRepair: 1, ReviewRework: 2},
				Reviews: []ReviewRun{{
					ID: 2, Path: "reviews/spec-2.md", Status: reviewStatus,
					OriginalFingerprint: original, AcceptedFingerprint: &accepted, Attempts: 2,
				}},
			},
			StagePlan: {Status: StageNotStarted, ReviewStatus: ReviewNotStarted, UpstreamHashes: []UpstreamHash{}, Reviews: []ReviewRun{}},
		},
		IssuedIDs:    []StableID{mustStableID(t, "REQ-001"), mustStableID(t, "SPEC-F-009")},
		Supersession: SupersessionLinks{Supersedes: "2026-08-29-old", SupersededBy: "2026-08-31-new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
