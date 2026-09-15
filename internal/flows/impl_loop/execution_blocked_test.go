package impl_loop

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestPersistExecutionBlockKeepsConfigurationScopeAndRequiredGateUntouched(t *testing.T) {
	run, state, journal, _ := newInitialCheckRun(t)
	defer state.Close()
	configuration := run.Identity.Configuration
	tasks := append([]implementationstate.Task(nil), run.Tasks...)
	block, err := ExecutionBlockForUserRemediation(
		"validate the configured required checks", "required check compiler has no command for this platform",
		[]string{"read .stepan/settings.json", "resolved the configured required-check catalog"},
		"install or configure the required tool, then explicitly resume or close the run",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := PersistExecutionBlock(context.Background(), state, run, block); err != nil {
		t.Fatal(err)
	}
	if run.Status != implementationstate.RunPaused || run.ExecutionBlock == nil || !reflect.DeepEqual(*run.ExecutionBlock, block) {
		t.Fatalf("configuration block was not retained in memory: %#v", run)
	}
	if run.Identity.Configuration != configuration || !reflect.DeepEqual(run.Tasks, tasks) || run.InitialBaselineStatus != implementationstate.InitialBaselinePending {
		t.Fatalf("execution block altered configuration, task scope, or required-check gate: %#v", run)
	}
	if err := run.StartAssignment("must-not-start", []implementationstate.TaskID{"task"}); !errors.Is(err, implementationstate.ErrInvalidTransition) {
		t.Fatalf("blocked run advanced dependent work: %v", err)
	}
	current, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil {
		t.Fatal(err)
	}
	if current.ExecutionBlock == nil || !reflect.DeepEqual(*current.ExecutionBlock, block) || current.InitialBaselineStatus != implementationstate.InitialBaselinePending {
		t.Fatalf("configuration block was not durable or relaxed required checks: %#v", current)
	}
}
