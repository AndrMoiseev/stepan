package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/openspec"
	"github.com/AndrMoiseev/stepan/internal/runstore"
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
	Run        *implementationstate.Run
	Journal    *runstore.Run
	StateStore *runstore.StateStore
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
	canonical, err := FindGitRoot(ctx, workCopy)
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
		stateWorkCopy, err := FindGitRoot(ctx, state.Identity.WorkCopy)
		if err != nil {
			return false, fmt.Errorf("run %s has invalid working copy: %w", id, err)
		}
		if lockIdentity(stateWorkCopy) == lockIdentity(canonical) && state.Identity.Change == change {
			return true, nil
		}
	}
	return false, nil
}

// NewRunFromTaskExtraction converts the orchestrator's formal response into
// the durable machine hierarchy. The task Markdown itself remains opaque: no
// Markdown parser or checkbox comparison participates in this conversion.
func NewRunFromTaskExtraction(identity implementationstate.RunIdentity, response AgentResponse) (*implementationstate.Run, error) {
	if response.Kind != ResponseTasksExtracted || strings.TrimSpace(response.Binding.CallID) == "" || response.Binding.RunID != identity.ID || response.Binding.AssignmentID != "" || response.Binding.BriefID != "" || response.Binding.Specification != identity.Specification || response.Binding.Configuration != identity.Configuration || response.Binding.TaskList != identity.TaskList {
		return nil, fmt.Errorf("%w: response is not bound to the new run inputs", ErrTaskExtraction)
	}
	tasks, err := DecodeExtractedTasks(response.TaskIDs, response.TaskPayloads)
	if err != nil {
		return nil, err
	}
	run, err := implementationstate.NewRun(identity, tasks)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTaskExtraction, err)
	}
	return run, nil
}

// DecodeExtractedTasks accepts only the formal task payload emitted by the
// orchestrator. Order is the response order; every parent must already have
// appeared, which makes the hierarchy and its source order unambiguous.
func DecodeExtractedTasks(ids []implementationstate.TaskID, payloads []string) ([]implementationstate.Task, error) {
	if len(ids) == 0 || len(ids) != len(payloads) {
		return nil, fmt.Errorf("%w: task IDs and payloads must be non-empty and have equal length", ErrTaskExtraction)
	}
	tasks := make([]implementationstate.Task, 0, len(ids))
	seen := make(map[implementationstate.TaskID]struct{}, len(ids))
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
		if implementationstate.TaskID(payload.ID) != id {
			return nil, fmt.Errorf("%w: payload ID %q does not match task_ids[%d]", ErrTaskExtraction, payload.ID, index)
		}
		if strings.TrimSpace(payload.Title) == "" {
			return nil, fmt.Errorf("%w: task %q has blank title", ErrTaskExtraction, id)
		}
		parent := implementationstate.TaskID(payload.ParentID)
		if parent != "" {
			if _, exists := seen[parent]; !exists {
				return nil, fmt.Errorf("%w: task %q has unknown or later parent %q", ErrTaskExtraction, id, parent)
			}
		}
		tasks = append(tasks, implementationstate.Task{ID: id, ParentID: parent, Order: index, Title: payload.Title})
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
