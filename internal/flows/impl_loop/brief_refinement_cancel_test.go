package impl_loop

import (
	"context"
	"errors"
	"testing"
	"time"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func TestRefineBriefRecoveryRespectsCancellation(t *testing.T) {
	for _, test := range []struct {
		name           string
		failAt         int
		duringRecovery bool
	}{
		{"source_request_before_recovery", 3, false},
		{"explorer_result_before_recovery", 4, false},
		{"explorer_result_during_recovery", 4, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefExplorationRequest(t)}, {raw: responsePayload(t, ResponseExplorationResult)}, {raw: briefReadyResponseWithContent(t, []implstate.TaskID{"A"}, "recovered Explorer route")}}}
			run, store, journal, repository, owner, _ := briefRefinementFixtureInRepository(t, runtime, newFilesystemWorkspace(t))
			defer store.Close()
			defer owner.Close()
			input := refinementInput(run, store, journal, repository, owner, "refine-explorer-recovery", "explorer-recovery-call")
			input.Explorer = &BriefRefinementExplorer{ExplorerOperationID: "explorer-recovery", ExplorerResultID: "explorer-recovery-result", ExplorerCallID: "explorer-recovery-call", ContinuationOperationID: "after-explorer-recovery", ContinuationResultID: "after-explorer-recovery-result"}
			original, calls := recordBriefRefinementState, 0
			t.Cleanup(func() { recordBriefRefinementState = original })
			recordBriefRefinementState = func(ctx context.Context, stateStore *runstore.StateStore, state *implstate.Run) (implstate.Event, error) {
				calls++
				if calls == test.failAt {
					return implstate.Event{}, errors.New("simulated persistence failure")
				}
				return original(ctx, stateStore, state)
			}
			if _, err := RefineBrief(context.Background(), input); err == nil {
				t.Fatal("missing initial failure")
			}
			recordBriefRefinementState = original
			before := len(runtime.messages)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.duringRecovery {
				// Cancel after the recovered provider turn has actually started.
				// The turn timeout bounds a regression that loses this cancellation.
				input.Timeout = time.Second
				runtime.turns[before] = controlledTurn{before: cancel, waitForInterrupt: true}
			} else {
				cancel()
			}
			result, err := RefineBrief(ctx, input)
			wantCalls := before
			if test.duringRecovery {
				wantCalls++
			}
			if len(runtime.messages) != wantCalls || result.Brief != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled recovery: calls before=%d after=%d, brief published=%t, error=%v", before, len(runtime.messages), result.Brief != nil, err)
			}
		})
	}
}
