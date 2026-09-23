package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	gitworkspace "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/git"
	"github.com/AndrMoiseev/stepan/internal/openspec"
)

var (
	// ErrNoResumableRun means that this working copy has no active or paused
	// Stepan run to continue. Closed runs are intentionally not resumable.
	ErrNoResumableRun = errors.New("no resumable implementation run for working copy")
	// ErrChangeAlreadyStarted prevents a closed run's task list from becoming
	// the starting point of another run for the same change and work copy.
	ErrChangeAlreadyStarted = errors.New("OpenSpec change already has an implementation run in this working copy")
	// ErrTaskExtraction identifies an orchestrator result which is not a formal
	// ordered task hierarchy for the run being created.
	ErrTaskExtraction = errors.New("invalid orchestrator task extraction")
)

// NewChangeStart reserves controller ownership for a genuinely new OpenSpec
// change and loads its versioned source package. It does not create a machine
// run yet: that happens only after the orchestrator returns its task hierarchy.
// The caller must close Lease if it does not proceed to durable run creation.
type NewChangeStart struct {
	Lease   *ControllerLease
	Package openspec.Package
}

// PrepareInitialTaskExtraction durably creates the pre-extraction machine
// state and its controller-owned agent operation before an orchestrator turn.
func PrepareInitialTaskExtraction(ctx context.Context, journal *runstore.Run, identity implstate.RunIdentity, operationID implstate.OperationID) (*implstate.Run, *runstore.StateStore, error) {
	if journal == nil || operationID == "" {
		return nil, nil, fmt.Errorf("%w: extraction journal and operation are required", ErrTaskExtraction)
	}
	run, err := implstate.NewRunPendingTaskExtraction(identity)
	if err != nil {
		return nil, nil, err
	}
	if err := run.AddRunOperation(implstate.Operation{ID: operationID, Kind: implstate.OperationAgent, Basis: implstate.AcceptanceBasis{Specification: identity.Specification, Configuration: identity.Configuration}}); err != nil {
		return nil, nil, err
	}
	stateStore, err := runstore.OpenState(journal)
	if err != nil {
		return nil, nil, err
	}
	if _, err := stateStore.Record(ctx, run); err != nil {
		_ = stateStore.Close()
		return nil, nil, err
	}
	return run, stateStore, nil
}

// PersistInitialTaskExtraction records the formally accepted hierarchy after
// the agent result. A crash before this record leaves TaskExtractionPending and
// the reserved attempt visible for recovery rather than importing work.
func PersistInitialTaskExtraction(ctx context.Context, stateStore *runstore.StateStore, run *implstate.Run, operationID implstate.OperationID, response AgentResponse) error {
	if stateStore == nil || run == nil {
		return fmt.Errorf("%w: extraction state is required", ErrTaskExtraction)
	}
	if err := validateInitialTaskExtractionResponse(run, response); err != nil {
		return err
	}
	tasks, err := DecodeExtractedTasks(response.TaskIDs, response.TaskPayloads)
	if err != nil {
		return err
	}
	if err := run.CompleteInitialTaskExtraction(tasks); err != nil {
		return err
	}
	if err := run.AddRunResult(implstate.OperationResult{ID: implstate.ResultID(string(operationID) + "-result"), OperationID: operationID, Status: implstate.ResultSucceeded, State: run.CurrentState, Basis: implstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}}); err != nil {
		return err
	}
	_, err = stateStore.Record(ctx, run)
	return err
}

// ExecuteInitialTaskExtraction connects the pending durable state to the
// controlled orchestrator turn. The existing controlled call reserves and
// records its attempt before dispatch; only its accepted response is persisted.
func ExecuteInitialTaskExtraction(ctx context.Context, call ControlledAgentCall) (ControlledAgentCallResult, error) {
	if call.Run == nil || !call.Run.TaskExtractionPending || call.AssignmentID != "" || call.Expectation.Role != ResponseRoleOrchestrator || call.Expectation.State != ResponseStateExtractingTasks {
		return ControlledAgentCallResult{}, fmt.Errorf("%w: invalid initial extraction call", ErrTaskExtraction)
	}
	callerValidation := call.ValidateResponse
	call.ValidateResponse = func(response AgentResponse) error {
		if response.Kind == ResponseExecutionBlocked {
			_, err := ExecutionBlockFromResponse(response)
			return err
		}
		if err := validateInitialTaskExtractionResponse(call.Run, response); err != nil {
			return err
		}
		if callerValidation != nil {
			return callerValidation(response)
		}
		return nil
	}
	result, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return result, err
	}
	if result.Response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(result.Response)
		if err != nil {
			return result, err
		}
		return result, PersistExecutionBlock(ctx, call.StateStore, call.Run, block)
	}
	if err := PersistInitialTaskExtraction(ctx, call.StateStore, call.Run, call.OperationID, result.Response); err != nil {
		return result, err
	}
	return result, nil
}

func validateInitialTaskExtractionResponse(run *implstate.Run, response AgentResponse) error {
	if run == nil || response.Kind != ResponseTasksExtracted || strings.TrimSpace(response.Binding.CallID) == "" || response.Binding.RunID != run.Identity.ID || response.Binding.AssignmentID != "" || response.Binding.BriefID != "" || response.Binding.Specification != run.Identity.Specification || response.Binding.Configuration != run.Identity.Configuration || response.Binding.TaskList != run.Identity.TaskList {
		return fmt.Errorf("%w: response is not bound to pending run", ErrTaskExtraction)
	}
	_, err := DecodeExtractedTasks(response.TaskIDs, response.TaskPayloads)
	return err
}

// BeginNewChange accepts only a change that has not already supplied a run in
// this work copy. In particular, it cannot silently reuse tasks from a closed
// run. Open runs are rejected by AcquireNewRunController.
func BeginNewChange(ctx context.Context, store *runstore.Store, workCopy, change string) (*NewChangeStart, error) {
	lease, err := AcquireNewRunController(ctx, store, workCopy)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*NewChangeStart, error) {
		return nil, errors.Join(err, lease.Close())
	}
	if started, err := changeHasRun(ctx, store, lease.WorkCopy(), change); err != nil {
		return fail(err)
	} else if started {
		return fail(fmt.Errorf("%w: %s", ErrChangeAlreadyStarted, change))
	}
	pkg, err := openspec.Load(lease.WorkCopy(), change)
	if err != nil {
		return fail(err)
	}
	return &NewChangeStart{Lease: lease, Package: pkg}, nil
}

// ResumedRun is the exclusively owned durable state selected by /resume. A
// continuation has no input for an arbitrary run ID: it can use only the one
// unclosed run already tied to this working copy.
type ResumedRun struct {
	Lease      *ControllerLease
	Run        *implstate.Run
	Journal    *runstore.Run
	StateStore *runstore.StateStore
}

// RecoverOwnRun acquires the controller lock only after an explicit user
// command. A durable active state with no lock owner is an interrupted prior
// process, not continuing work: it is first persisted as a pause and only the
// normal /resume reconciliation may make it active again.
func RecoverOwnRun(ctx context.Context, store *runstore.Store, workCopy string) (*ResumedRun, *UserRunControl, error) {
	resumed, err := ContinueOwnRun(ctx, store, workCopy)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*ResumedRun, *UserRunControl, error) {
		return nil, nil, errors.Join(err, resumed.Close())
	}
	if resumed.Run.Status == implstate.RunActive {
		if err := resumed.Run.Pause("previous Stepan controller was interrupted; explicit /resume required"); err != nil {
			return fail(err)
		}
		if _, err := resumed.StateStore.Record(ctx, resumed.Run); err != nil {
			return fail(fmt.Errorf("persist interrupted-controller pause: %w", err))
		}
	}
	control, err := NewUserRunControl(resumed.Run, resumed.StateStore)
	if err != nil {
		return fail(err)
	}
	return resumed, control, nil
}

// ContinueOwnRun acquires controller ownership and opens the sole unclosed run
// associated with workCopy. It never opens a closed run, and it cannot import
// a run belonging to another work copy.
func ContinueOwnRun(ctx context.Context, store *runstore.Store, workCopy string) (*ResumedRun, error) {
	lease, err := AcquireController(ctx, store, workCopy)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*ResumedRun, error) {
		return nil, errors.Join(err, lease.Close())
	}
	model, err := FindUnclosedRun(ctx, store, lease.WorkCopy())
	if err != nil {
		return fail(err)
	}
	if model == nil {
		return fail(ErrNoResumableRun)
	}
	journal, err := store.Open(model.Identity.ID)
	if err != nil {
		return fail(fmt.Errorf("open saved implementation run: %w", err))
	}
	stateStore, err := runstore.OpenState(journal)
	if err != nil {
		return fail(fmt.Errorf("open saved implementation state: %w", err))
	}
	return &ResumedRun{Lease: lease, Run: model, Journal: journal, StateStore: stateStore}, nil
}

// Close releases every resource retained by a resumed run.
func (r *ResumedRun) Close() error {
	if r == nil {
		return nil
	}
	return errors.Join(r.StateStore.Close(), r.Lease.Close())
}

func changeHasRun(ctx context.Context, store *runstore.Store, workCopy, change string) (bool, error) {
	if strings.TrimSpace(change) == "" {
		return false, fmt.Errorf("%w: blank change", ErrChangeAlreadyStarted)
	}
	canonical, err := (gitworkspace.Control{}).FindRoot(ctx, workCopy)
	if err != nil {
		return false, err
	}
	ids, err := store.RunIDs()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		journal, err := store.Open(id)
		if err != nil {
			return false, err
		}
		state, _, err := runstore.ReadJournalCurrent(journal)
		if errors.Is(err, runstore.ErrCurrentStateUnavailable) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("read run %s state: %w", id, err)
		}
		if lockIdentity(filepath.Clean(state.Identity.WorkCopy)) == lockIdentity(canonical) && state.Identity.Change == change {
			return true, nil
		}
	}
	return false, nil
}

// NewRunFromTaskExtraction converts the orchestrator's formal response into
// the durable machine hierarchy. The task Markdown itself remains opaque: no
// Markdown parser or checkbox comparison participates in this conversion.
func NewRunFromTaskExtraction(identity implstate.RunIdentity, response AgentResponse) (*implstate.Run, error) {
	if response.Kind != ResponseTasksExtracted || strings.TrimSpace(response.Binding.CallID) == "" || response.Binding.RunID != identity.ID || response.Binding.AssignmentID != "" || response.Binding.BriefID != "" || response.Binding.Specification != identity.Specification || response.Binding.Configuration != identity.Configuration || response.Binding.TaskList != identity.TaskList {
		return nil, fmt.Errorf("%w: response is not bound to the new run inputs", ErrTaskExtraction)
	}
	tasks, err := DecodeExtractedTasks(response.TaskIDs, response.TaskPayloads)
	if err != nil {
		return nil, err
	}
	run, err := implstate.NewRun(identity, tasks)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTaskExtraction, err)
	}
	return run, nil
}

// DecodeExtractedTasks accepts only the formal task payload emitted by the
// orchestrator. Order is the response order; every parent must already have
// appeared, which makes the hierarchy and its source order unambiguous.
func DecodeExtractedTasks(ids []implstate.TaskID, payloads []string) ([]implstate.Task, error) {
	if len(ids) == 0 || len(ids) != len(payloads) {
		return nil, fmt.Errorf("%w: task IDs and payloads must be non-empty and have equal length", ErrTaskExtraction)
	}
	tasks := make([]implstate.Task, 0, len(ids))
	seen := make(map[implstate.TaskID]struct{}, len(ids))
	for index, id := range ids {
		if strings.TrimSpace(string(id)) == "" {
			return nil, fmt.Errorf("%w: blank task ID at %d", ErrTaskExtraction, index)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: duplicate task ID %q", ErrTaskExtraction, id)
		}
		payload, err := decodeExtractedTaskPayload(payloads[index])
		if err != nil {
			return nil, fmt.Errorf("%w: task %q: %v", ErrTaskExtraction, id, err)
		}
		if implstate.TaskID(payload.ID) != id {
			return nil, fmt.Errorf("%w: payload ID %q does not match task_ids[%d]", ErrTaskExtraction, payload.ID, index)
		}
		if strings.TrimSpace(payload.Title) == "" {
			return nil, fmt.Errorf("%w: task %q has blank title", ErrTaskExtraction, id)
		}
		parent := implstate.TaskID(payload.ParentID)
		if parent != "" {
			if _, exists := seen[parent]; !exists {
				return nil, fmt.Errorf("%w: task %q has unknown or later parent %q", ErrTaskExtraction, id, parent)
			}
		}
		// A preorder traversal may return to an active ancestor, but cannot
		// reopen a subtree after a following sibling/root was emitted.
		if index > 0 {
			ancestor := tasks[len(tasks)-1].ID
			for ancestor != "" && ancestor != parent {
				found := implstate.TaskID("")
				for i := len(tasks) - 1; i >= 0; i-- {
					if tasks[i].ID == ancestor {
						found = tasks[i].ParentID
						break
					}
				}
				ancestor = found
			}
			if parent != "" && ancestor != parent {
				return nil, fmt.Errorf("%w: task %q reopens closed parent %q", ErrTaskExtraction, id, parent)
			}
		}
		tasks = append(tasks, implstate.Task{ID: id, ParentID: parent, Order: index, Title: payload.Title})
		seen[id] = struct{}{}
	}
	return tasks, nil
}

type extractedTaskPayload struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Title    string `json:"title"`
}

func decodeExtractedTaskPayload(raw string) (extractedTaskPayload, error) {
	var payload extractedTaskPayload
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return extractedTaskPayload{}, fmt.Errorf("decode formal payload: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return extractedTaskPayload{}, errors.New("formal payload has trailing values")
	}
	if strings.TrimSpace(payload.ID) == "" {
		return extractedTaskPayload{}, errors.New("formal payload has blank id")
	}
	return payload, nil
}
