package implementationstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var (
	// ErrInvalidState reports malformed state or transition input.
	ErrInvalidState = errors.New("invalid implementation state")
	// ErrInvalidTransition reports a transition that is not allowed from the
	// current state.
	ErrInvalidTransition = errors.New("invalid implementation state transition")
	// ErrLimitExceeded reports that the next semantic or technical attempt was
	// not started because its configured limit is already exhausted. The Run is
	// paused before this error is returned.
	ErrLimitExceeded = errors.New("implementation cycle limit exceeded")
)

type (
	RunID        string
	TaskID       string
	AssignmentID string
	BriefID      string
	OperationID  string
	ResultID     string
	EvidenceID   string
)

// RunIdentity ties a run to one exact implementation target. Paths and Git
// references are opaque to this package; their observation is the controller's
// responsibility.
type RunIdentity struct {
	ID             RunID
	Change         string
	Repository     string
	WorkCopy       string
	Branch         string
	BaselineCommit string
	BaselineState  EvidenceRef
	Specification  EvidenceRef
	TaskList       EvidenceRef
	Configuration  EvidenceRef
}

func (i RunIdentity) validate() error {
	if i.ID == "" || i.Change == "" || i.Repository == "" || i.WorkCopy == "" || i.Branch == "" || i.BaselineCommit == "" || !i.BaselineState.valid() || !i.Specification.valid() || !i.TaskList.valid() || !i.Configuration.valid() {
		return fmt.Errorf("%w: run identity is incomplete", ErrInvalidState)
	}
	return nil
}

// RunStatus separates a resumable pause from terminal closure and successful
// completion. Closed and succeeded runs cannot be reopened.
type RunStatus string

const (
	RunActive    RunStatus = "active"
	RunPaused    RunStatus = "paused"
	RunClosed    RunStatus = "closed"
	RunSucceeded RunStatus = "succeeded"
)

func (s RunStatus) valid() bool {
	return s == RunActive || s == RunPaused || s == RunClosed || s == RunSucceeded
}

// Task is one ordered item in the extracted task hierarchy. A task without a
// child is a unit of implementation. Parent status is calculated from children
// and is never independently persisted or changed.
type Task struct {
	ID       TaskID
	ParentID TaskID
	Order    int
	Title    string
}

// TaskStatus is persisted only for leaves. Parent status is derived by
// TaskStatus below.
type TaskStatus string

const (
	TaskPending                TaskStatus = "pending"
	TaskAcceptedAwaitingCommit TaskStatus = "accepted_awaiting_commit"
	TaskComplete               TaskStatus = "complete"
)

func (s TaskStatus) valid() bool {
	return s == TaskPending || s == TaskAcceptedAwaitingCommit || s == TaskComplete
}

type AssignmentStatus string

const (
	AssignmentActive                 AssignmentStatus = "active"
	AssignmentAcceptedAwaitingCommit AssignmentStatus = "accepted_awaiting_commit"
	AssignmentCommitted              AssignmentStatus = "committed"
)

func (s AssignmentStatus) valid() bool {
	return s == AssignmentActive || s == AssignmentAcceptedAwaitingCommit || s == AssignmentCommitted
}

// EvidenceRef identifies an immutable captured input or observed code state.
// The store resolves ID and verifies Digest when it writes the referenced file.
type EvidenceRef struct {
	ID     EvidenceID
	Digest string
}

func (e EvidenceRef) valid() bool { return e.ID != "" && e.Digest != "" }

// AcceptanceBasis versions the specification and effective configuration used
// by an operation, result, or acceptance decision. Task-list progress is not
// an acceptance input: it is informational after extraction.
type AcceptanceBasis struct {
	Specification EvidenceRef
	Configuration EvidenceRef
}

func (b AcceptanceBasis) valid() bool {
	return b.Specification.valid() && b.Configuration.valid()
}

// BriefVersion is an immutable version of one assignment's task contract.
type BriefVersion struct {
	ID       BriefID
	Number   int
	Document EvidenceRef
}

func (b BriefVersion) valid() bool {
	return b.ID != "" && b.Number > 0 && b.Document.valid()
}

type OperationKind string

const (
	OperationAgent  OperationKind = "agent"
	OperationCheck  OperationKind = "check"
	OperationReview OperationKind = "review"
)

func (k OperationKind) valid() bool {
	return k == OperationAgent || k == OperationCheck || k == OperationReview
}

// CycleCounter identifies the semantic cycle that an operation consumes when
// its first attempt starts. It deliberately has no "assignment calls" value:
// the implementation loop limits only the concrete cycles from its
// configuration.
type CycleCounter string

const (
	CycleCounterNone             CycleCounter = ""
	CycleCounterAssignmentReview CycleCounter = "assignment_review"
	CycleCounterMandatoryChecks  CycleCounter = "mandatory_checks"
	CycleCounterChecksRequested  CycleCounter = "checks_requested"
	CycleCounterBriefRefinement  CycleCounter = "brief_refinement"
	CycleCounterExplorer         CycleCounter = "explorer"
	CycleCounterFinalReview      CycleCounter = "final_review"
)

func (c CycleCounter) valid() bool {
	return c == CycleCounterNone || c == CycleCounterAssignmentReview || c == CycleCounterMandatoryChecks || c == CycleCounterChecksRequested || c == CycleCounterBriefRefinement || c == CycleCounterExplorer || c == CycleCounterFinalReview
}

func (c CycleCounter) matchesOperationKind(kind OperationKind) bool {
	switch c {
	case CycleCounterNone:
		return true
	case CycleCounterAssignmentReview, CycleCounterFinalReview:
		return kind == OperationReview
	case CycleCounterMandatoryChecks, CycleCounterChecksRequested:
		return kind == OperationCheck
	case CycleCounterBriefRefinement, CycleCounterExplorer:
		return kind == OperationAgent
	default:
		return false
	}
}

// CycleCounters are the independent, currently active semantic-cycle
// counters for one assignment. Explorer counters are partitioned by episode;
// the controller supplies a stable episode ID on the operation.
//
// Reset boundaries are intentionally not defined here. They are controller
// policy and are added with the lifecycle work that opens a new cycle.
type CycleCounters struct {
	AssignmentReview uint64            `json:"assignment_review"`
	MandatoryChecks  uint64            `json:"mandatory_checks"`
	ChecksRequested  uint64            `json:"checks_requested"`
	BriefRefinement  uint64            `json:"brief_refinement"`
	Explorer         map[string]uint64 `json:"explorer,omitempty"`
	// The generation fields identify the active cycle behind each current
	// counter. They keep completed-cycle attempts immutable when a successful
	// mandatory set, or an explicit limit-pause continuation, opens a new one.
	AssignmentReviewCycle uint64            `json:"assignment_review_cycle,omitempty"`
	MandatoryChecksCycle  uint64            `json:"mandatory_checks_cycle,omitempty"`
	ChecksRequestedCycle  uint64            `json:"checks_requested_cycle,omitempty"`
	BriefRefinementCycle  uint64            `json:"brief_refinement_cycle,omitempty"`
	ExplorerCycle         map[string]uint64 `json:"explorer_cycle,omitempty"`
}

// CycleLimits is the effective configured limit set needed by the durable
// state model. It has no aggregate call budget: each field limits only its
// named cycle. TechnicalAttempts applies independently to one operation.
type CycleLimits struct {
	AssignmentReview  int
	MandatoryChecks   int
	ChecksRequested   int
	BriefRefinement   int
	Explorer          int
	TechnicalAttempts int
	FinalReview       int
}

func (l CycleLimits) valid() bool {
	return l.AssignmentReview > 0 && l.MandatoryChecks > 0 && l.ChecksRequested > 0 && l.BriefRefinement > 0 && l.Explorer > 0 && l.TechnicalAttempts > 0 && l.FinalReview > 0
}

// LimitPause identifies exactly the exhausted counter that paused a run.
// Counter is CycleCounterNone only when Technical is true; OperationID then
// selects the one operation whose technical retry window is reset on resume.
type LimitPause struct {
	Counter      CycleCounter `json:"counter"`
	AssignmentID AssignmentID `json:"assignment_id,omitempty"`
	OperationID  OperationID  `json:"operation_id"`
	Episode      string       `json:"episode,omitempty"`
	Technical    bool         `json:"technical,omitempty"`
}

func (p LimitPause) valid() bool {
	if p.OperationID == "" || (!p.Counter.valid()) || (p.Technical && p.Episode != "") {
		return false
	}
	if p.Technical {
		return true
	}
	if p.Counter == CycleCounterNone || p.Counter == CycleCounterExplorer && strings.TrimSpace(p.Episode) == "" {
		return false
	}
	if p.Counter == CycleCounterFinalReview {
		return p.AssignmentID == "" && p.Episode == ""
	}
	if p.Counter == CycleCounterExplorer {
		return strings.TrimSpace(p.Episode) != ""
	}
	return p.AssignmentID != "" && p.Episode == ""
}

// OperationAttempt is written into the run state before dispatch. A missing
// result is intentional: after a crash it conservatively means that this
// attempt was spent even if the controller cannot establish whether the
// external action actually started.
type OperationAttempt struct {
	Number        uint64 `json:"number"`
	SemanticRound uint64 `json:"semantic_round"`
	// Outcome and Diagnostic are populated after an externally dispatched
	// attempt returns. They deliberately live on the attempt rather than an
	// OperationResult: a technical failure must be retained while the same
	// operation remains eligible for a retry.
	Outcome    AttemptOutcome `json:"outcome,omitempty"`
	Diagnostic string         `json:"diagnostic,omitempty"`
}

// AttemptOutcome describes the controller's observation of one externally
// dispatched attempt. An empty outcome is the conservative durable state left
// by a crash between recording the start and observing the external action.
type AttemptOutcome string

const (
	AttemptSucceeded   AttemptOutcome = "succeeded"
	AttemptFailed      AttemptOutcome = "failed"
	AttemptInterrupted AttemptOutcome = "interrupted"
	AttemptRejected    AttemptOutcome = "rejected"
)

func (outcome AttemptOutcome) valid() bool {
	return outcome == "" || outcome == AttemptSucceeded || outcome == AttemptFailed || outcome == AttemptInterrupted || outcome == AttemptRejected
}

// Operation records a controller-requested agent, check, or review action.
// It is intentionally descriptive; dispatch and attempt accounting are added
// by later layers.
type Operation struct {
	ID          OperationID
	Kind        OperationKind
	BriefID     BriefID
	Basis       AcceptanceBasis
	Description string
	Counter     CycleCounter
	// Episode is required for Explorer operations and is otherwise empty. It
	// makes an Explorer episode's counter independent from other episodes.
	Episode  string
	Attempts []OperationAttempt
	// SemanticCycle is assigned when the operation first consumes a semantic
	// counter. Keeping it on the operation retains completed-cycle history when
	// the corresponding current counter is reset.
	SemanticCycle uint64 `json:"semantic_cycle,omitempty"`
	// TechnicalAttemptStart is the number of already recorded attempts at the
	// start of the current technical retry window. It preserves prior attempts
	// when a technical-limit pause is explicitly continued.
	TechnicalAttemptStart uint64 `json:"technical_attempt_start,omitempty"`
	// UncountedResumeCheck is the explicit exception for the mandatory set
	// executed during /resume. It records an externally observed result without
	// consuming either a semantic cycle or a technical attempt.
	UncountedResumeCheck bool
}

func (o Operation) valid() bool {
	if o.ID == "" || !o.Kind.valid() || !o.Basis.valid() || !o.Counter.valid() || !o.Counter.matchesOperationKind(o.Kind) || (o.Counter == CycleCounterExplorer && strings.TrimSpace(o.Episode) == "") || (o.Counter != CycleCounterExplorer && o.Episode != "") || (o.UncountedResumeCheck && (o.Kind != OperationCheck || o.Counter != CycleCounterNone || len(o.Attempts) != 0)) {
		return false
	}
	for index, attempt := range o.Attempts {
		if attempt.Number != uint64(index+1) || !attempt.Outcome.valid() || (attempt.Outcome == "" && attempt.Diagnostic != "") || (o.Counter == CycleCounterNone && (attempt.SemanticRound != 0 || o.SemanticCycle != 0)) || (o.Counter != CycleCounterNone && (attempt.SemanticRound == 0 || o.SemanticCycle == 0 || (index > 0 && attempt.SemanticRound != o.Attempts[0].SemanticRound))) {
			return false
		}
	}
	if o.TechnicalAttemptStart > uint64(len(o.Attempts)) {
		return false
	}
	return true
}

type ResultStatus string

const (
	ResultSucceeded   ResultStatus = "succeeded"
	ResultFailed      ResultStatus = "failed"
	ResultInterrupted ResultStatus = "interrupted"
)

func (s ResultStatus) valid() bool {
	return s == ResultSucceeded || s == ResultFailed || s == ResultInterrupted
}

// OperationResult records an outcome and the exact code state it observed.
type OperationResult struct {
	ID          ResultID
	OperationID OperationID
	Status      ResultStatus
	State       EvidenceRef
	Basis       AcceptanceBasis
	Evidence    []EvidenceRef
}

func (r OperationResult) valid() bool {
	if r.ID == "" || r.OperationID == "" || !r.Status.valid() || !r.State.valid() || !r.Basis.valid() {
		return false
	}
	for _, evidence := range r.Evidence {
		if !evidence.valid() {
			return false
		}
	}
	return true
}

// AcceptanceEvidence identifies successful checks and review for one exact
// brief and code state. It is retained while a commit is pending.
type AcceptanceEvidence struct {
	BriefID        BriefID
	State          EvidenceRef
	Basis          AcceptanceBasis
	CheckResultIDs []ResultID
	ReviewResultID ResultID
	PendingCommit  CommitIntent
}

// CommitIntent is captured before Git is invoked. Recovery uses it to compare
// a candidate commit with the accepted parent, tree, message, and operation.
type CommitIntent struct {
	OperationID  OperationID
	ParentCommit string
	Tree         string
	Message      string
}

func (c CommitIntent) valid() bool {
	return c.OperationID != "" && c.ParentCommit != "" && c.Tree != "" && strings.TrimSpace(c.Message) != ""
}

// CommitEvidence links the accepted code state to the commit that makes a leaf
// task complete. The controller supplies the observed Git facts.
type CommitEvidence struct {
	OperationID  OperationID
	CommitID     string
	ParentCommit string
	Tree         string
	Message      string
	State        EvidenceRef
	Basis        AcceptanceBasis
}

func (c CommitEvidence) valid() bool {
	return c.OperationID != "" && c.CommitID != "" && c.ParentCommit != "" && c.Tree != "" && strings.TrimSpace(c.Message) != "" && c.State.valid() && c.Basis.valid()
}

// Assignment retains selection, brief versions, operations and results for a
// contiguous block of leaf tasks. A selected task is not accepted or complete
// until its respective transition is applied.
type Assignment struct {
	ID                AssignmentID
	TaskIDs           []TaskID
	Status            AssignmentStatus
	Briefs            []BriefVersion
	Operations        []Operation
	Results           []OperationResult
	Acceptance        *AcceptanceEvidence
	AcceptanceHistory []AcceptanceEvidence
	Commit            *CommitEvidence
	Counters          CycleCounters
}

// FinalAcceptanceEvidence proves completion of the run rather than of one
// assignment. OpenFindingIDs must be empty for a successful final review; the
// referenced review result retains the full finding evidence either way.
type FinalAcceptanceEvidence struct {
	State          EvidenceRef
	Basis          AcceptanceBasis
	CheckResultIDs []ResultID
	ReviewResultID ResultID
	OpenFindingIDs []EvidenceID
}

// Run is the portable in-memory representation for future JSONL and SQLite
// stores. Markdown checkbox state is deliberately absent.
type Run struct {
	Identity     RunIdentity
	Status       RunStatus
	CurrentState EvidenceRef
	Tasks        []Task
	// TaskExtractionPending is true only during the durable pre-extraction
	// boundary. It permits an otherwise complete run identity before the first
	// orchestrator response supplies the machine task hierarchy.
	TaskExtractionPending  bool `json:"task_extraction_pending,omitempty"`
	LeafStatus             map[TaskID]TaskStatus
	Assignments            []Assignment
	RunOperations          []Operation
	RunResults             []OperationResult
	FinalAcceptance        *FinalAcceptanceEvidence
	FinalAcceptanceHistory []FinalAcceptanceEvidence
	FinalReviewRounds      uint64
	FinalReviewCycle       uint64
	// RunExplorerCounters are Explorer budgets for sources that have no
	// assignment or brief yet (for example the initial briefer, final reviewer,
	// and bootstrapper). They remain independent per source episode.
	RunExplorerCounters map[string]uint64 `json:"run_explorer_counters,omitempty"`
	RunExplorerCycles   map[string]uint64 `json:"run_explorer_cycles,omitempty"`
	PauseReason         string
	LimitPause          *LimitPause
	CloseReason         string
}

// EventKind identifies one durable state transition. New event kinds can be
// added without making the journal depend on a storage implementation.
type EventKind string

const (
	// EventRunStateRecorded records the complete validated state after one
	// controller transition. It is intentionally self-contained so the first
	// journal format can rebuild a projection without provider session data.
	EventRunStateRecorded EventKind = "run_state_recorded"
)

// Event is one ordered journal record. Sequence is assigned by the run store;
// callers use NewRunStateEvent rather than constructing events directly.
type Event struct {
	Sequence uint64    `json:"sequence"`
	Kind     EventKind `json:"kind"`
	State    *Run      `json:"state"`
}

// NewRunStateEvent makes an immutable event payload from a validated run.
func NewRunStateEvent(sequence uint64, run *Run) (Event, error) {
	if sequence == 0 || run == nil || run.Validate() != nil {
		return Event{}, fmt.Errorf("%w: invalid run-state event", ErrInvalidState)
	}
	state, err := cloneRun(run)
	if err != nil {
		return Event{}, err
	}
	return Event{Sequence: sequence, Kind: EventRunStateRecorded, State: state}, nil
}

// Validate checks that an event is safe to persist or apply.
func (e Event) Validate() error {
	if e.Sequence == 0 || e.Kind != EventRunStateRecorded || e.State == nil || e.State.Validate() != nil {
		return fmt.Errorf("%w: invalid run-state event", ErrInvalidState)
	}
	return nil
}

// Apply returns the state represented by this event. The current argument is
// reserved for future incremental event kinds, whose validation can then keep
// the same replay boundary.
func (e Event) Apply(_ *Run) (*Run, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return cloneRun(e.State)
}

func cloneRun(run *Run) (*Run, error) {
	data, err := json.Marshal(run)
	if err != nil {
		return nil, fmt.Errorf("%w: encode run state: %v", ErrInvalidState, err)
	}
	var clone Run
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, fmt.Errorf("%w: decode run state: %v", ErrInvalidState, err)
	}
	if err := clone.Validate(); err != nil {
		return nil, err
	}
	return &clone, nil
}

// NewRun validates the extracted ordered hierarchy and creates a new active
// run. All leaf tasks start pending.
func NewRun(identity RunIdentity, tasks []Task) (*Run, error) {
	run := &Run{Identity: identity, Status: RunActive, CurrentState: identity.BaselineState, Tasks: slices.Clone(tasks)}
	if err := run.validateStructure(); err != nil {
		return nil, err
	}
	run.LeafStatus = make(map[TaskID]TaskStatus)
	for _, task := range run.Tasks {
		if run.isLeaf(task.ID) {
			run.LeafStatus[task.ID] = TaskPending
		}
	}
	return run, nil
}

// NewRunPendingTaskExtraction creates the durable state recorded before the
// initial orchestrator turn. It cannot be used after tasks have been supplied.
func NewRunPendingTaskExtraction(identity RunIdentity) (*Run, error) {
	run := &Run{Identity: identity, Status: RunActive, CurrentState: identity.BaselineState, TaskExtractionPending: true, LeafStatus: make(map[TaskID]TaskStatus)}
	if err := run.Validate(); err != nil {
		return nil, err
	}
	return run, nil
}

// CompleteInitialTaskExtraction atomically installs the first and only source
// hierarchy after its formal response has been accepted.
func (r *Run) CompleteInitialTaskExtraction(tasks []Task) error {
	if err := r.requireActive(); err != nil || !r.TaskExtractionPending {
		return fmt.Errorf("%w: task extraction is not pending", ErrInvalidState)
	}
	candidate := &Run{Identity: r.Identity, Status: r.Status, CurrentState: r.CurrentState, Tasks: slices.Clone(tasks)}
	if err := candidate.validateStructure(); err != nil {
		return err
	}
	r.Tasks, r.TaskExtractionPending = candidate.Tasks, false
	r.LeafStatus = make(map[TaskID]TaskStatus)
	for _, task := range r.Tasks {
		if r.isLeaf(task.ID) {
			r.LeafStatus[task.ID] = TaskPending
		}
	}
	return nil
}

// Validate checks serializable state loaded by a future store.
func (r *Run) Validate() error {
	if r == nil || r.Identity.validate() != nil || !r.Status.valid() || !r.CurrentState.valid() {
		return fmt.Errorf("%w: invalid run", ErrInvalidState)
	}
	if err := r.validateStructure(); err != nil {
		return err
	}
	if err := r.validateLifecycle(); err != nil {
		return err
	}
	for _, task := range r.Tasks {
		_, stored := r.LeafStatus[task.ID]
		if r.isLeaf(task.ID) {
			if !stored || !r.LeafStatus[task.ID].valid() {
				return fmt.Errorf("%w: leaf task %q lacks status", ErrInvalidState, task.ID)
			}
		} else if stored {
			return fmt.Errorf("%w: parent task %q has stored status", ErrInvalidState, task.ID)
		}
	}
	for id := range r.LeafStatus {
		if !r.isLeaf(id) {
			return fmt.Errorf("%w: status for unknown or parent task %q", ErrInvalidState, id)
		}
	}
	seen := make(map[AssignmentID]bool)
	assigned := make(map[TaskID]AssignmentStatus)
	briefs := make(map[BriefID]bool)
	operations := make(map[OperationID]bool)
	results := make(map[ResultID]bool)
	openAssignments := 0
	seenOpenAssignment := false
	leafOrder := r.allLeafTasks()
	leafCursor := 0
	for _, assignment := range r.Assignments {
		if assignment.ID == "" || seen[assignment.ID] || !assignment.Status.valid() {
			return fmt.Errorf("%w: invalid assignment", ErrInvalidState)
		}
		seen[assignment.ID] = true
		if err := r.validateAssignment(assignment); err != nil {
			return err
		}
		if assignment.Status == AssignmentAcceptedAwaitingCommit && (assignment.Acceptance.Basis != r.currentBasis() || assignment.Acceptance.State != r.CurrentState) {
			return fmt.Errorf("%w: pending acceptance has stale basis or code state", ErrInvalidState)
		}
		if len(assignment.TaskIDs) > len(leafOrder)-leafCursor || !slices.Equal(assignment.TaskIDs, leafOrder[leafCursor:leafCursor+len(assignment.TaskIDs)]) {
			return fmt.Errorf("%w: assignment tasks are not a contiguous leaf prefix", ErrInvalidState)
		}
		leafCursor += len(assignment.TaskIDs)
		if assignment.Status != AssignmentCommitted {
			openAssignments++
			seenOpenAssignment = true
		} else if seenOpenAssignment {
			return fmt.Errorf("%w: committed assignment follows an open assignment", ErrInvalidState)
		}
		lastVersion := 0
		for _, brief := range assignment.Briefs {
			if briefs[brief.ID] || brief.Number != lastVersion+1 {
				return fmt.Errorf("%w: duplicate or unordered brief", ErrInvalidState)
			}
			briefs[brief.ID], lastVersion = true, brief.Number
		}
		for _, operation := range assignment.Operations {
			if operations[operation.ID] {
				return fmt.Errorf("%w: duplicate operation", ErrInvalidState)
			}
			operations[operation.ID] = true
		}
		for _, result := range assignment.Results {
			if results[result.ID] {
				return fmt.Errorf("%w: duplicate result", ErrInvalidState)
			}
			results[result.ID] = true
		}
		for _, taskID := range assignment.TaskIDs {
			if _, exists := assigned[taskID]; exists {
				return fmt.Errorf("%w: task %q belongs to multiple assignments", ErrInvalidState, taskID)
			}
			assigned[taskID] = assignment.Status
			want := TaskPending
			switch assignment.Status {
			case AssignmentAcceptedAwaitingCommit:
				want = TaskAcceptedAwaitingCommit
			case AssignmentCommitted:
				want = TaskComplete
			}
			if r.LeafStatus[taskID] != want {
				return fmt.Errorf("%w: task %q does not match assignment state", ErrInvalidState, taskID)
			}
		}
	}
	if openAssignments > 1 {
		return fmt.Errorf("%w: more than one open assignment", ErrInvalidState)
	}
	for taskID, status := range r.LeafStatus {
		if status == TaskAcceptedAwaitingCommit && assigned[taskID] != AssignmentAcceptedAwaitingCommit {
			return fmt.Errorf("%w: accepted task %q lacks accepted assignment", ErrInvalidState, taskID)
		}
	}
	for _, taskID := range leafOrder[leafCursor:] {
		if r.LeafStatus[taskID] != TaskPending {
			return fmt.Errorf("%w: unassigned task %q is not pending", ErrInvalidState, taskID)
		}
	}
	for _, operation := range r.RunOperations {
		if !operation.valid() || operation.BriefID != "" || !validRunCounter(operation.Counter) || operations[operation.ID] {
			return fmt.Errorf("%w: invalid run operation", ErrInvalidState)
		}
		operations[operation.ID] = true
	}
	for _, result := range r.RunResults {
		operation := r.runOperation(result.OperationID)
		if !result.valid() || operation == nil || (!operation.UncountedResumeCheck && len(operation.Attempts) == 0) || results[result.ID] || result.Basis != operation.Basis {
			return fmt.Errorf("%w: invalid run result", ErrInvalidState)
		}
		results[result.ID] = true
	}
	if r.FinalAcceptance != nil {
		if err := r.validateFinalAcceptance(*r.FinalAcceptance, true); err != nil {
			return err
		}
	}
	if err := r.validateCounters(); err != nil {
		return err
	}
	for _, evidence := range r.FinalAcceptanceHistory {
		if err := r.validateFinalAcceptance(evidence, false); err != nil {
			return err
		}
	}
	if r.Status == RunSucceeded {
		if r.FinalAcceptance == nil {
			return fmt.Errorf("%w: succeeded run lacks final acceptance", ErrInvalidState)
		}
		for _, status := range r.LeafStatus {
			if status != TaskComplete {
				return fmt.Errorf("%w: succeeded run has unfinished task", ErrInvalidState)
			}
		}
	}
	return nil
}

func (r *Run) validateLifecycle() error {
	switch r.Status {
	case RunActive:
		if r.PauseReason != "" || r.CloseReason != "" || r.LimitPause != nil {
			return fmt.Errorf("%w: active run has stop reason", ErrInvalidState)
		}
	case RunPaused:
		if strings.TrimSpace(r.PauseReason) == "" || r.CloseReason != "" || (r.LimitPause != nil && !r.LimitPause.valid()) {
			return fmt.Errorf("%w: paused run lacks pause reason", ErrInvalidState)
		}
	case RunClosed:
		if strings.TrimSpace(r.CloseReason) == "" || r.PauseReason != "" || r.LimitPause != nil {
			return fmt.Errorf("%w: closed run lacks close reason", ErrInvalidState)
		}
	case RunSucceeded:
		if r.PauseReason != "" || r.CloseReason != "" || r.LimitPause != nil {
			return fmt.Errorf("%w: succeeded run has stop reason", ErrInvalidState)
		}
	}
	return nil
}

func (r *Run) validateStructure() error {
	if r.Identity.validate() != nil || (len(r.Tasks) == 0 && !r.TaskExtractionPending) || (len(r.Tasks) != 0 && r.TaskExtractionPending) {
		return fmt.Errorf("%w: run must have an identity and tasks", ErrInvalidState)
	}
	seen := make(map[TaskID]Task, len(r.Tasks))
	for index, task := range r.Tasks {
		if task.ID == "" || task.Title == "" || task.Order != index || seen[task.ID].ID != "" {
			return fmt.Errorf("%w: invalid ordered task at %d", ErrInvalidState, index)
		}
		if task.ParentID != "" {
			parent, ok := seen[task.ParentID]
			if !ok || parent.Order >= task.Order {
				return fmt.Errorf("%w: task %q has invalid parent", ErrInvalidState, task.ID)
			}
		}
		seen[task.ID] = task
	}
	return nil
}

func (r *Run) isLeaf(id TaskID) bool {
	for _, task := range r.Tasks {
		if task.ParentID == id {
			return false
		}
	}
	for _, task := range r.Tasks {
		if task.ID == id {
			return true
		}
	}
	return false
}

// TaskStatus derives a parent result. A parent is complete only when every
// descendant is complete; it never becomes accepted-awaiting-commit itself.
func (r *Run) TaskStatus(id TaskID) (TaskStatus, error) {
	if r == nil || !r.hasTask(id) {
		return "", fmt.Errorf("%w: unknown task %q", ErrInvalidState, id)
	}
	if r.isLeaf(id) {
		return r.LeafStatus[id], nil
	}
	for _, child := range r.children(id) {
		status, err := r.TaskStatus(child.ID)
		if err != nil || status != TaskComplete {
			return TaskPending, err
		}
	}
	return TaskComplete, nil
}

// PendingLeafTasks returns leaves that have not yet entered an assignment's
// accepted-awaiting-commit state, in extracted order.
func (r *Run) PendingLeafTasks() []TaskID {
	if r == nil {
		return nil
	}
	var pending []TaskID
	for _, task := range r.Tasks {
		if r.isLeaf(task.ID) && r.LeafStatus[task.ID] == TaskPending {
			pending = append(pending, task.ID)
		}
	}
	return pending
}

// StartAssignment selects a non-empty contiguous prefix of pending leaves.
// The sequential model permits only one non-committed assignment at a time.
func (r *Run) StartAssignment(id AssignmentID, taskIDs []TaskID) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	if id == "" || r.assignmentIndex(id) >= 0 || r.hasOpenAssignment() {
		return fmt.Errorf("%w: invalid or concurrent assignment", ErrInvalidState)
	}
	pending := r.PendingLeafTasks()
	if len(taskIDs) == 0 || len(taskIDs) > len(pending) || !slices.Equal(taskIDs, pending[:len(taskIDs)]) {
		return fmt.Errorf("%w: assignment must select a contiguous pending leaf prefix", ErrInvalidState)
	}
	r.invalidateFinalAcceptance()
	r.Assignments = append(r.Assignments, Assignment{ID: id, TaskIDs: slices.Clone(taskIDs), Status: AssignmentActive})
	return nil
}

// AddBriefVersion appends a new immutable contract version to an active
// assignment. The controller allocates the version number and document record.
func (r *Run) AddBriefVersion(assignmentID AssignmentID, brief BriefVersion) error {
	assignment, err := r.activeAssignment(assignmentID)
	if err != nil {
		return err
	}
	if !brief.valid() || r.briefExists(brief.ID) || (len(assignment.Briefs) == 0 && brief.Number != 1) || (len(assignment.Briefs) > 0 && brief.Number != assignment.Briefs[len(assignment.Briefs)-1].Number+1) {
		return fmt.Errorf("%w: invalid brief version", ErrInvalidState)
	}
	assignment.Briefs = append(assignment.Briefs, brief)
	return nil
}

// AddOperation and AddResult retain controller-mediated work and its outcome.
func (r *Run) AddOperation(assignmentID AssignmentID, operation Operation) error {
	assignment, err := r.activeAssignment(assignmentID)
	if err != nil {
		return err
	}
	if !operation.valid() || operation.BriefID == "" || operation.UncountedResumeCheck || !validAssignmentCounter(operation.Counter) || r.operationExists(operation.ID) || !assignment.hasBrief(operation.BriefID) {
		return fmt.Errorf("%w: invalid operation", ErrInvalidState)
	}
	assignment.Operations = append(assignment.Operations, operation)
	return nil
}

func (r *Run) AddResult(assignmentID AssignmentID, result OperationResult) error {
	assignment, err := r.activeAssignment(assignmentID)
	if err != nil {
		return err
	}
	operation := assignment.operation(result.OperationID)
	if !result.valid() || r.resultExists(result.ID) || operation == nil || (!operation.UncountedResumeCheck && len(operation.Attempts) == 0) || result.Basis != operation.Basis {
		return fmt.Errorf("%w: invalid operation result", ErrInvalidState)
	}
	assignment.Results = append(assignment.Results, cloneResult(result))
	if operation.Counter == CycleCounterMandatoryChecks && result.Status == ResultSucceeded {
		assignment.Counters.MandatoryChecks = 0
		assignment.Counters.MandatoryChecksCycle = nextCycle(assignment.Counters.MandatoryChecksCycle)
	}
	return nil
}

// AcceptAssignment records the successful evidence before a commit is tried.
// Selected leaves then have the distinct accepted-awaiting-commit state.
func (r *Run) AcceptAssignment(assignmentID AssignmentID, evidence AcceptanceEvidence) error {
	assignment, err := r.activeAssignment(assignmentID)
	if err != nil {
		return err
	}
	if err := assignment.accept(evidence); err != nil || evidence.Basis != r.currentBasis() || evidence.State != r.CurrentState {
		if err == nil {
			err = fmt.Errorf("%w: acceptance basis or code state is stale", ErrInvalidState)
		}
		return err
	}
	assignment.Acceptance = cloneAcceptance(evidence)
	assignment.Status = AssignmentAcceptedAwaitingCommit
	for _, taskID := range assignment.TaskIDs {
		r.LeafStatus[taskID] = TaskAcceptedAwaitingCommit
	}
	return nil
}

// CommitAssignment is the only transition that completes selected leaves.
func (r *Run) CommitAssignment(assignmentID AssignmentID, evidence CommitEvidence) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	index := r.assignmentIndex(assignmentID)
	if index < 0 {
		return fmt.Errorf("%w: unknown assignment", ErrInvalidState)
	}
	assignment := &r.Assignments[index]
	if assignment.Status != AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil || !evidence.valid() || !matchesIntent(evidence, assignment.Acceptance.PendingCommit) || evidence.State != assignment.Acceptance.State || evidence.State != r.CurrentState || evidence.Basis != assignment.Acceptance.Basis || evidence.Basis != r.currentBasis() {
		return fmt.Errorf("%w: assignment is not accepted for this commit state", ErrInvalidTransition)
	}
	assignment.Commit = &evidence
	assignment.Status = AssignmentCommitted
	r.CurrentState = evidence.State
	r.invalidateFinalAcceptance()
	for _, taskID := range assignment.TaskIDs {
		r.LeafStatus[taskID] = TaskComplete
	}
	return nil
}

// ReopenAssignment invalidates a pending acceptance when either its inputs or
// observed code state changed. It retains every prior operation, result, and
// acceptance record while returning its selected leaves to pending.
func (r *Run) ReopenAssignment(assignmentID AssignmentID, observedState EvidenceRef) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	index := r.assignmentIndex(assignmentID)
	if index < 0 {
		return fmt.Errorf("%w: unknown assignment", ErrInvalidState)
	}
	assignment := &r.Assignments[index]
	if assignment.Status != AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil || !observedState.valid() {
		return fmt.Errorf("%w: assignment cannot reopen", ErrInvalidTransition)
	}
	if assignment.Acceptance.Basis == r.currentBasis() && assignment.Acceptance.State == observedState {
		return fmt.Errorf("%w: accepted assignment is still current", ErrInvalidTransition)
	}
	r.CurrentState = observedState
	r.reopenAssignment(assignment)
	return nil
}

// ObserveCodeState records a controller-observed code state. A changed state
// automatically invalidates an accepted assignment and any final acceptance.
func (r *Run) ObserveCodeState(state EvidenceRef) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	if !state.valid() {
		return fmt.Errorf("%w: invalid observed code state", ErrInvalidState)
	}
	if state == r.CurrentState {
		return nil
	}
	r.CurrentState = state
	r.invalidateFinalAcceptance()
	for index := range r.Assignments {
		assignment := &r.Assignments[index]
		if assignment.Status == AssignmentAcceptedAwaitingCommit && assignment.Acceptance != nil && assignment.Acceptance.State != state {
			r.reopenAssignment(assignment)
		}
	}
	return nil
}

// RefreshAcceptanceInputs records newly observed specification and effective
// configuration versions as one transition. It archives final evidence and
// reopens a pending acceptance, so no evidence from the old inputs can be
// reused for the refreshed run.
func (r *Run) RefreshAcceptanceInputs(specification, configuration EvidenceRef) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	if !specification.valid() || !configuration.valid() {
		return fmt.Errorf("%w: invalid acceptance inputs", ErrInvalidState)
	}
	if r.Identity.Specification == specification && r.Identity.Configuration == configuration {
		return nil
	}
	r.Identity.Specification = specification
	r.Identity.Configuration = configuration
	r.invalidateFinalAcceptance()
	for index := range r.Assignments {
		assignment := &r.Assignments[index]
		if assignment.Status == AssignmentAcceptedAwaitingCommit && assignment.Acceptance != nil {
			r.reopenAssignment(assignment)
		}
	}
	return nil
}

func (r *Run) reopenAssignment(assignment *Assignment) {
	assignment.AcceptanceHistory = append(assignment.AcceptanceHistory, *cloneAcceptance(*assignment.Acceptance))
	assignment.Acceptance = nil
	assignment.Status = AssignmentActive
	for _, taskID := range assignment.TaskIDs {
		r.LeafStatus[taskID] = TaskPending
	}
	r.invalidateFinalAcceptance()
}

func (r *Run) invalidateFinalAcceptance() {
	if r.FinalAcceptance == nil {
		return
	}
	r.FinalAcceptanceHistory = append(r.FinalAcceptanceHistory, *cloneFinalAcceptance(*r.FinalAcceptance))
	r.FinalAcceptance = nil
}

// AddRunOperation records baseline, resume, or final work that is not tied to
// an assignment brief.
func (r *Run) AddRunOperation(operation Operation) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	if !operation.valid() || operation.BriefID != "" || !validRunCounter(operation.Counter) || r.operationExists(operation.ID) {
		return fmt.Errorf("%w: invalid run operation", ErrInvalidState)
	}
	r.RunOperations = append(r.RunOperations, operation)
	return nil
}

// StartAssignmentAttempt reserves an attempt before the caller dispatches the
// external action. The caller MUST durably record the changed Run (normally
// through runstore.StateStore) before dispatching it. A later technical retry
// of this same operation receives a new technical number but retains the
// already-reserved semantic round.
func (r *Run) StartAssignmentAttempt(assignmentID AssignmentID, operationID OperationID) (OperationAttempt, error) {
	assignment, err := r.activeAssignment(assignmentID)
	if err != nil {
		return OperationAttempt{}, err
	}
	operation := assignment.operation(operationID)
	if operation == nil || operation.UncountedResumeCheck || assignment.hasResultForOperation(operationID) {
		return OperationAttempt{}, fmt.Errorf("%w: operation cannot consume an assignment counter", ErrInvalidState)
	}
	attempt, err := assignment.startAttempt(operation)
	if err != nil {
		return OperationAttempt{}, err
	}
	return attempt, nil
}

// StartAssignmentAttemptWithLimits is the checked counterpart of
// StartAssignmentAttempt. It pauses before recording an attempt that would
// exceed a configured semantic or technical limit, so callers have no
// external action to recover from in that case.
func (r *Run) StartAssignmentAttemptWithLimits(assignmentID AssignmentID, operationID OperationID, limits CycleLimits) (OperationAttempt, error) {
	if !limits.valid() {
		return OperationAttempt{}, fmt.Errorf("%w: invalid cycle limits", ErrInvalidState)
	}
	assignment, err := r.activeAssignment(assignmentID)
	if err != nil {
		return OperationAttempt{}, err
	}
	operation := assignment.operation(operationID)
	if operation == nil || operation.UncountedResumeCheck || assignment.hasResultForOperation(operationID) {
		return OperationAttempt{}, fmt.Errorf("%w: operation cannot consume an assignment counter", ErrInvalidState)
	}
	if err := r.allowAssignmentAttempt(assignmentID, assignment, operation, limits); err != nil {
		return OperationAttempt{}, err
	}
	return assignment.startAttempt(operation)
}

// RecordAssignmentAttemptOutcome records the post-dispatch technical outcome
// without completing the operation. A failed or malformed agent turn can
// therefore be retried while remaining visible after recovery.
func (r *Run) RecordAssignmentAttemptOutcome(assignmentID AssignmentID, operationID OperationID, outcome AttemptOutcome, diagnostic string) error {
	assignment, err := r.assignmentForAttemptOutcome(assignmentID)
	if err != nil {
		return err
	}
	return recordAttemptOutcome(assignment.operation(operationID), outcome, diagnostic)
}

// StartRunAttempt reserves a run-level operation attempt before external
// dispatch. It follows the same durable-record-before-dispatch rule as
// assignment work.
func (r *Run) StartRunAttempt(operationID OperationID) (OperationAttempt, error) {
	if err := r.requireActive(); err != nil {
		return OperationAttempt{}, err
	}
	operation := r.runOperation(operationID)
	if operation == nil || operation.UncountedResumeCheck || r.hasRunResultForOperation(operationID) {
		return OperationAttempt{}, fmt.Errorf("%w: operation cannot consume a run counter", ErrInvalidState)
	}
	return r.startRunAttempt(operation)
}

// StartRunAttemptWithLimits applies final-review, run-level Explorer, and
// per-operation technical limits before reserving the durable attempt.
func (r *Run) StartRunAttemptWithLimits(operationID OperationID, limits CycleLimits) (OperationAttempt, error) {
	if !limits.valid() {
		return OperationAttempt{}, fmt.Errorf("%w: invalid cycle limits", ErrInvalidState)
	}
	if err := r.requireActive(); err != nil {
		return OperationAttempt{}, err
	}
	operation := r.runOperation(operationID)
	if operation == nil || operation.UncountedResumeCheck || r.hasRunResultForOperation(operationID) {
		return OperationAttempt{}, fmt.Errorf("%w: operation cannot consume a run counter", ErrInvalidState)
	}
	if technicalAttempts(operation) >= uint64(limits.TechnicalAttempts) {
		return OperationAttempt{}, r.pauseForLimit(LimitPause{Counter: operation.Counter, OperationID: operationID, Technical: true})
	}
	if len(operation.Attempts) == 0 {
		if operation.Counter == CycleCounterFinalReview && r.FinalReviewRounds >= uint64(limits.FinalReview) {
			return OperationAttempt{}, r.pauseForLimit(LimitPause{Counter: CycleCounterFinalReview, OperationID: operationID})
		}
		if operation.Counter == CycleCounterExplorer && r.RunExplorerCounters[operation.Episode] >= uint64(limits.Explorer) {
			return OperationAttempt{}, r.pauseForLimit(LimitPause{Counter: CycleCounterExplorer, OperationID: operationID, Episode: operation.Episode})
		}
	}
	return r.startRunAttempt(operation)
}

// RecordRunAttemptOutcome is the run-scoped counterpart of
// RecordAssignmentAttemptOutcome.
func (r *Run) RecordRunAttemptOutcome(operationID OperationID, outcome AttemptOutcome, diagnostic string) error {
	if r.Status != RunActive && r.Status != RunPaused {
		return fmt.Errorf("%w: run cannot record an attempt outcome", ErrInvalidState)
	}
	return recordAttemptOutcome(r.runOperation(operationID), outcome, diagnostic)
}

func recordAttemptOutcome(operation *Operation, outcome AttemptOutcome, diagnostic string) error {
	if operation == nil || outcome == "" || !outcome.valid() || len(operation.Attempts) == 0 {
		return fmt.Errorf("%w: invalid attempt outcome", ErrInvalidState)
	}
	attempt := &operation.Attempts[len(operation.Attempts)-1]
	if attempt.Outcome != "" {
		return fmt.Errorf("%w: attempt outcome is already recorded", ErrInvalidState)
	}
	attempt.Outcome = outcome
	attempt.Diagnostic = diagnostic
	return nil
}

func (r *Run) allowAssignmentAttempt(assignmentID AssignmentID, assignment *Assignment, operation *Operation, limits CycleLimits) error {
	if technicalAttempts(operation) >= uint64(limits.TechnicalAttempts) {
		return r.pauseForLimit(LimitPause{Counter: operation.Counter, AssignmentID: assignmentID, OperationID: operation.ID, Technical: true})
	}
	if len(operation.Attempts) != 0 {
		return nil
	}
	var current uint64
	var limit int
	switch operation.Counter {
	case CycleCounterNone:
		return nil
	case CycleCounterAssignmentReview:
		current, limit = assignment.Counters.AssignmentReview, limits.AssignmentReview
	case CycleCounterMandatoryChecks:
		current, limit = assignment.Counters.MandatoryChecks, limits.MandatoryChecks
	case CycleCounterChecksRequested:
		current, limit = assignment.Counters.ChecksRequested, limits.ChecksRequested
	case CycleCounterBriefRefinement:
		current, limit = assignment.Counters.BriefRefinement, limits.BriefRefinement
	case CycleCounterExplorer:
		current, limit = assignment.Counters.Explorer[operation.Episode], limits.Explorer
	default:
		return fmt.Errorf("%w: unsupported assignment counter", ErrInvalidState)
	}
	if current >= uint64(limit) {
		return r.pauseForLimit(LimitPause{Counter: operation.Counter, AssignmentID: assignmentID, OperationID: operation.ID, Episode: operation.Episode})
	}
	return nil
}

func technicalAttempts(operation *Operation) uint64 {
	if operation == nil || uint64(len(operation.Attempts)) < operation.TechnicalAttemptStart {
		return 0
	}
	return uint64(len(operation.Attempts)) - operation.TechnicalAttemptStart
}

func (r *Run) pauseForLimit(limit LimitPause) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	if !limit.valid() {
		return fmt.Errorf("%w: invalid limit pause", ErrInvalidState)
	}
	r.Status = RunPaused
	r.PauseReason = "implementation cycle limit: " + string(limit.Counter)
	if limit.Technical {
		r.PauseReason = "implementation technical attempt limit"
	}
	copy := limit
	r.LimitPause = &copy
	return ErrLimitExceeded
}

func (r *Run) resetLimitPause(limit LimitPause) error {
	if !limit.valid() {
		return fmt.Errorf("%w: invalid persisted limit pause", ErrInvalidState)
	}
	if limit.Technical {
		operation := r.operationForLimit(limit)
		if operation == nil || operation.hasResult(r, limit.AssignmentID) {
			return fmt.Errorf("%w: technical limit operation cannot resume", ErrInvalidState)
		}
		operation.TechnicalAttemptStart = uint64(len(operation.Attempts))
		return nil
	}
	if limit.Counter == CycleCounterFinalReview {
		r.FinalReviewRounds = 0
		r.FinalReviewCycle = nextCycle(r.FinalReviewCycle)
		return nil
	}
	if limit.Counter == CycleCounterExplorer && limit.AssignmentID == "" {
		if r.RunExplorerCounters == nil {
			r.RunExplorerCounters = make(map[string]uint64)
		}
		if r.RunExplorerCycles == nil {
			r.RunExplorerCycles = make(map[string]uint64)
		}
		r.RunExplorerCounters[limit.Episode] = 0
		r.RunExplorerCycles[limit.Episode] = nextCycle(r.RunExplorerCycles[limit.Episode])
		return nil
	}
	assignment := r.assignment(limit.AssignmentID)
	if assignment == nil || assignment.Status != AssignmentActive {
		return fmt.Errorf("%w: limit assignment cannot resume", ErrInvalidState)
	}
	switch limit.Counter {
	case CycleCounterAssignmentReview:
		assignment.Counters.AssignmentReview, assignment.Counters.AssignmentReviewCycle = 0, nextCycle(assignment.Counters.AssignmentReviewCycle)
	case CycleCounterMandatoryChecks:
		assignment.Counters.MandatoryChecks, assignment.Counters.MandatoryChecksCycle = 0, nextCycle(assignment.Counters.MandatoryChecksCycle)
	case CycleCounterChecksRequested:
		assignment.Counters.ChecksRequested, assignment.Counters.ChecksRequestedCycle = 0, nextCycle(assignment.Counters.ChecksRequestedCycle)
	case CycleCounterBriefRefinement:
		assignment.Counters.BriefRefinement, assignment.Counters.BriefRefinementCycle = 0, nextCycle(assignment.Counters.BriefRefinementCycle)
	case CycleCounterExplorer:
		if assignment.Counters.Explorer == nil {
			assignment.Counters.Explorer = make(map[string]uint64)
		}
		if assignment.Counters.ExplorerCycle == nil {
			assignment.Counters.ExplorerCycle = make(map[string]uint64)
		}
		assignment.Counters.Explorer[limit.Episode] = 0
		assignment.Counters.ExplorerCycle[limit.Episode] = nextCycle(assignment.Counters.ExplorerCycle[limit.Episode])
	default:
		return fmt.Errorf("%w: unsupported limit counter", ErrInvalidState)
	}
	return nil
}

func nextCycle(current uint64) uint64 {
	if current == 0 {
		return 2
	}
	return current + 1
}

func currentCycle(current uint64) uint64 {
	if current == 0 {
		return 1
	}
	return current
}

func (r *Run) operationForLimit(limit LimitPause) *Operation {
	if limit.AssignmentID == "" {
		return r.runOperation(limit.OperationID)
	}
	assignment := r.assignment(limit.AssignmentID)
	if assignment == nil {
		return nil
	}
	return assignment.operation(limit.OperationID)
}

func (o *Operation) hasResult(r *Run, assignmentID AssignmentID) bool {
	if assignmentID == "" {
		return r.hasRunResultForOperation(o.ID)
	}
	assignment := r.assignment(assignmentID)
	return assignment == nil || assignment.hasResultForOperation(o.ID)
}

// EndExplorerEpisode opens a fresh per-episode counter after the source
// agent returns or transitions. Continuing the source agent after Explorer
// deliberately does not call this method and therefore preserves the count.
func (r *Run) EndExplorerEpisode(assignmentID AssignmentID, episode string) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	assignment := r.assignment(assignmentID)
	if assignment == nil || assignment.Status != AssignmentActive || strings.TrimSpace(episode) == "" {
		return fmt.Errorf("%w: invalid explorer episode boundary", ErrInvalidState)
	}
	if assignment.Counters.Explorer == nil {
		assignment.Counters.Explorer = make(map[string]uint64)
	}
	if assignment.Counters.ExplorerCycle == nil {
		assignment.Counters.ExplorerCycle = make(map[string]uint64)
	}
	assignment.Counters.Explorer[episode] = 0
	assignment.Counters.ExplorerCycle[episode] = nextCycle(assignment.Counters.ExplorerCycle[episode])
	return nil
}

// EndRunExplorerEpisode opens a fresh Explorer budget for a run-level source
// after it returns or moves to a new stage. This covers sources before an
// assignment brief exists as well as the final reviewer and bootstrapper.
func (r *Run) EndRunExplorerEpisode(episode string) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	if strings.TrimSpace(episode) == "" {
		return fmt.Errorf("%w: invalid run explorer episode boundary", ErrInvalidState)
	}
	if r.RunExplorerCounters == nil {
		r.RunExplorerCounters = make(map[string]uint64)
	}
	if r.RunExplorerCycles == nil {
		r.RunExplorerCycles = make(map[string]uint64)
	}
	r.RunExplorerCounters[episode] = 0
	r.RunExplorerCycles[episode] = nextCycle(r.RunExplorerCycles[episode])
	return nil
}

func (r *Run) AddRunResult(result OperationResult) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	operation := r.runOperation(result.OperationID)
	if !result.valid() || r.resultExists(result.ID) || operation == nil || (!operation.UncountedResumeCheck && len(operation.Attempts) == 0) || result.Basis != operation.Basis {
		return fmt.Errorf("%w: invalid run result", ErrInvalidState)
	}
	r.RunResults = append(r.RunResults, cloneResult(result))
	return nil
}

// RecordFinalAcceptance preserves the required checks, final review, and its
// absence of open findings before Succeed can close a run successfully.
func (r *Run) RecordFinalAcceptance(evidence FinalAcceptanceEvidence) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	if err := r.validateFinalAcceptance(evidence, true); err != nil {
		return err
	}
	r.invalidateFinalAcceptance()
	r.FinalAcceptance = cloneFinalAcceptance(evidence)
	return nil
}

// Pause preserves resumable work. Resume is unavailable after Close or
// Succeed, so terminal runs can never be reopened.
func (r *Run) Pause(reason string) error {
	if r.Status != RunActive || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: run cannot be paused", ErrInvalidTransition)
	}
	r.Status, r.PauseReason, r.LimitPause = RunPaused, reason, nil
	return nil
}

func (r *Run) Resume() error {
	if r.Status != RunPaused {
		return fmt.Errorf("%w: only a paused run can resume", ErrInvalidTransition)
	}
	if r.LimitPause != nil {
		if err := r.resetLimitPause(*r.LimitPause); err != nil {
			return err
		}
	}
	r.Status, r.PauseReason, r.LimitPause = RunActive, "", nil
	return nil
}

func (r *Run) Close(reason string) error {
	if (r.Status != RunActive && r.Status != RunPaused) || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: run cannot close", ErrInvalidTransition)
	}
	r.Status, r.CloseReason, r.PauseReason, r.LimitPause = RunClosed, reason, "", nil
	return nil
}

// Succeed closes a fully committed run only after final required checks and a
// final review with no open findings have been recorded for the current basis.
func (r *Run) Succeed() error {
	if err := r.requireActive(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return fmt.Errorf("%w: run cannot succeed with invalid persisted state: %v", ErrInvalidTransition, err)
	}
	for _, taskID := range r.PendingLeafTasks() {
		if r.LeafStatus[taskID] != TaskComplete {
			return fmt.Errorf("%w: unfinished task %q", ErrInvalidTransition, taskID)
		}
	}
	for _, status := range r.LeafStatus {
		if status != TaskComplete {
			return fmt.Errorf("%w: unfinished accepted task", ErrInvalidTransition)
		}
	}
	if r.FinalAcceptance == nil {
		return fmt.Errorf("%w: final acceptance is required", ErrInvalidTransition)
	}
	if err := r.validateFinalAcceptance(*r.FinalAcceptance, true); err != nil {
		return fmt.Errorf("%w: invalid final acceptance: %v", ErrInvalidTransition, err)
	}
	r.Status = RunSucceeded
	return nil
}

func (r *Run) validateFinalAcceptance(evidence FinalAcceptanceEvidence, current bool) error {
	if !evidence.State.valid() || !evidence.Basis.valid() || len(evidence.CheckResultIDs) == 0 || evidence.ReviewResultID == "" || hasDuplicateResultIDs(evidence.CheckResultIDs) || len(evidence.OpenFindingIDs) != 0 {
		return fmt.Errorf("%w: incomplete final acceptance", ErrInvalidState)
	}
	if current && evidence.Basis != r.currentBasis() {
		return fmt.Errorf("%w: final acceptance basis is stale", ErrInvalidState)
	}
	if current && evidence.State != r.CurrentState {
		return fmt.Errorf("%w: final acceptance code state is stale", ErrInvalidState)
	}
	if current {
		if r.hasOpenAssignment() {
			return fmt.Errorf("%w: final acceptance has open assignment", ErrInvalidState)
		}
		for _, status := range r.LeafStatus {
			if status != TaskComplete {
				return fmt.Errorf("%w: final acceptance has unfinished task", ErrInvalidState)
			}
		}
	}
	for _, resultID := range evidence.CheckResultIDs {
		result := r.runResult(resultID)
		operation := Operation{}
		if result != nil {
			if found := r.runOperation(result.OperationID); found != nil {
				operation = *found
			}
		}
		if result == nil || !operation.valid() || result.Status != ResultSucceeded || result.State != evidence.State || result.Basis != evidence.Basis || operation.Kind != OperationCheck || operation.Basis != evidence.Basis {
			return fmt.Errorf("%w: final check result %q does not prove accepted state", ErrInvalidState, resultID)
		}
	}
	review := r.runResult(evidence.ReviewResultID)
	reviewOperation := Operation{}
	if review != nil {
		if found := r.runOperation(review.OperationID); found != nil {
			reviewOperation = *found
		}
	}
	if review == nil || !reviewOperation.valid() || review.Status != ResultSucceeded || review.State != evidence.State || review.Basis != evidence.Basis || reviewOperation.Kind != OperationReview || reviewOperation.Basis != evidence.Basis {
		return fmt.Errorf("%w: final review does not prove accepted state", ErrInvalidState)
	}
	return nil
}

func (r *Run) requireActive() error {
	if r == nil || r.Status != RunActive {
		return fmt.Errorf("%w: run is not active", ErrInvalidTransition)
	}
	return nil
}

func (r *Run) validateAssignment(assignment Assignment) error {
	if len(assignment.TaskIDs) == 0 || hasDuplicates(assignment.TaskIDs) {
		return fmt.Errorf("%w: invalid assignment tasks", ErrInvalidState)
	}
	for _, id := range assignment.TaskIDs {
		if !r.isLeaf(id) {
			return fmt.Errorf("%w: assignment contains non-leaf task %q", ErrInvalidState, id)
		}
	}
	for _, brief := range assignment.Briefs {
		if !brief.valid() {
			return fmt.Errorf("%w: invalid brief", ErrInvalidState)
		}
	}
	for _, operation := range assignment.Operations {
		if !operation.valid() || operation.BriefID == "" || operation.UncountedResumeCheck || !assignment.hasBrief(operation.BriefID) {
			return fmt.Errorf("%w: invalid operation", ErrInvalidState)
		}
	}
	for _, result := range assignment.Results {
		operation := assignment.operation(result.OperationID)
		if !result.valid() || operation == nil || (!operation.UncountedResumeCheck && len(operation.Attempts) == 0) || result.Basis != operation.Basis {
			return fmt.Errorf("%w: invalid result", ErrInvalidState)
		}
	}
	for _, evidence := range assignment.AcceptanceHistory {
		if assignment.validateAcceptance(evidence, false) != nil {
			return fmt.Errorf("%w: invalid historical acceptance", ErrInvalidState)
		}
	}
	if assignment.Status == AssignmentActive && (assignment.Acceptance != nil || assignment.Commit != nil) {
		return fmt.Errorf("%w: active assignment has terminal evidence", ErrInvalidState)
	}
	if assignment.Status == AssignmentAcceptedAwaitingCommit {
		if assignment.Acceptance == nil || assignment.Commit != nil || assignment.accept(*assignment.Acceptance) != nil {
			return fmt.Errorf("%w: invalid accepted assignment", ErrInvalidState)
		}
	}
	if assignment.Status == AssignmentCommitted {
		if assignment.Acceptance == nil || assignment.accept(*assignment.Acceptance) != nil || assignment.Commit == nil || !assignment.Commit.valid() || !matchesIntent(*assignment.Commit, assignment.Acceptance.PendingCommit) || assignment.Commit.State != assignment.Acceptance.State || assignment.Commit.Basis != assignment.Acceptance.Basis {
			return fmt.Errorf("%w: invalid committed assignment", ErrInvalidState)
		}
	}
	return nil
}

func validAssignmentCounter(counter CycleCounter) bool {
	return counter == CycleCounterNone || counter == CycleCounterAssignmentReview || counter == CycleCounterMandatoryChecks || counter == CycleCounterChecksRequested || counter == CycleCounterBriefRefinement || counter == CycleCounterExplorer
}

func validRunCounter(counter CycleCounter) bool {
	return counter == CycleCounterNone || counter == CycleCounterFinalReview || counter == CycleCounterExplorer
}

func (r *Run) validateCounters() error {
	finalRounds := make(map[uint64]uint64)
	var currentFinalReviews uint64
	runExplorerRounds := make(map[string]uint64)
	wantRunExplorer := make(map[string]uint64)
	for _, operation := range r.RunOperations {
		if len(operation.Attempts) == 0 {
			continue
		}
		switch operation.Counter {
		case CycleCounterFinalReview:
			cycle := operation.SemanticCycle
			if cycle == 0 {
				return fmt.Errorf("%w: final review lacks semantic cycle", ErrInvalidState)
			}
			finalRounds[cycle]++
			if operation.Attempts[0].SemanticRound != finalRounds[cycle] {
				return fmt.Errorf("%w: final review semantic rounds are not sequential", ErrInvalidState)
			}
			if cycle == currentCycle(r.FinalReviewCycle) {
				currentFinalReviews++
			}
		case CycleCounterExplorer:
			cycle := operation.SemanticCycle
			if cycle == 0 {
				return fmt.Errorf("%w: run explorer lacks semantic cycle", ErrInvalidState)
			}
			key := operation.Episode + "\x00" + fmt.Sprint(cycle)
			runExplorerRounds[key]++
			if operation.Attempts[0].SemanticRound != runExplorerRounds[key] {
				return fmt.Errorf("%w: run explorer semantic rounds are not sequential", ErrInvalidState)
			}
			if cycle == currentCycle(r.RunExplorerCycles[operation.Episode]) {
				wantRunExplorer[operation.Episode]++
			}
		}
	}
	if r.FinalReviewRounds != currentFinalReviews {
		return fmt.Errorf("%w: final review counter does not match attempts", ErrInvalidState)
	}
	if !mapsEqual(r.RunExplorerCounters, wantRunExplorer) {
		return fmt.Errorf("%w: run explorer counters do not match attempts", ErrInvalidState)
	}
	for _, assignment := range r.Assignments {
		want := CycleCounters{Explorer: make(map[string]uint64)}
		rounds := make(map[string]uint64)
		for _, operation := range assignment.Operations {
			if len(operation.Attempts) == 0 {
				continue
			}
			round := operation.Attempts[0].SemanticRound
			if operation.Counter == CycleCounterNone {
				continue
			}
			if operation.SemanticCycle == 0 {
				return fmt.Errorf("%w: operation lacks semantic cycle", ErrInvalidState)
			}
			key := string(operation.Counter) + "\x00" + operation.Episode + "\x00" + fmt.Sprint(operation.SemanticCycle)
			rounds[key]++
			if round != rounds[key] {
				return fmt.Errorf("%w: semantic rounds are not sequential", ErrInvalidState)
			}
			switch operation.Counter {
			case CycleCounterAssignmentReview:
				if operation.SemanticCycle == currentCycle(assignment.Counters.AssignmentReviewCycle) {
					want.AssignmentReview++
				}
			case CycleCounterMandatoryChecks:
				if operation.SemanticCycle == currentCycle(assignment.Counters.MandatoryChecksCycle) {
					want.MandatoryChecks++
				}
			case CycleCounterChecksRequested:
				if operation.SemanticCycle == currentCycle(assignment.Counters.ChecksRequestedCycle) {
					want.ChecksRequested++
				}
			case CycleCounterBriefRefinement:
				if operation.SemanticCycle == currentCycle(assignment.Counters.BriefRefinementCycle) {
					want.BriefRefinement++
				}
			case CycleCounterExplorer:
				if operation.SemanticCycle == currentCycle(assignment.Counters.ExplorerCycle[operation.Episode]) {
					want.Explorer[operation.Episode]++
				}
			}
		}
		if assignment.Counters.AssignmentReview != want.AssignmentReview || assignment.Counters.MandatoryChecks != want.MandatoryChecks || assignment.Counters.ChecksRequested != want.ChecksRequested || assignment.Counters.BriefRefinement != want.BriefRefinement || !mapsEqual(assignment.Counters.Explorer, want.Explorer) {
			return fmt.Errorf("%w: assignment counters do not match attempts", ErrInvalidState)
		}
	}
	return nil
}

func mapsEqual(got, want map[string]uint64) bool {
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	for key, value := range got {
		if value != 0 && want[key] != value {
			return false
		}
	}
	return true
}

func (a *Assignment) startAttempt(operation *Operation) (OperationAttempt, error) {
	if operation == nil || !validAssignmentCounter(operation.Counter) || operation.UncountedResumeCheck {
		return OperationAttempt{}, fmt.Errorf("%w: invalid assignment attempt", ErrInvalidState)
	}
	attempt := OperationAttempt{Number: uint64(len(operation.Attempts) + 1)}
	if len(operation.Attempts) != 0 {
		attempt.SemanticRound = operation.Attempts[0].SemanticRound
	} else {
		switch operation.Counter {
		case CycleCounterNone:
			// Technical attempts without a semantic-cycle counter are still
			// durable and independently numbered.
		case CycleCounterAssignmentReview:
			a.Counters.AssignmentReview++
			attempt.SemanticRound = a.Counters.AssignmentReview
			operation.SemanticCycle = currentCycle(a.Counters.AssignmentReviewCycle)
		case CycleCounterMandatoryChecks:
			a.Counters.MandatoryChecks++
			attempt.SemanticRound = a.Counters.MandatoryChecks
			operation.SemanticCycle = currentCycle(a.Counters.MandatoryChecksCycle)
		case CycleCounterChecksRequested:
			a.Counters.ChecksRequested++
			attempt.SemanticRound = a.Counters.ChecksRequested
			operation.SemanticCycle = currentCycle(a.Counters.ChecksRequestedCycle)
		case CycleCounterBriefRefinement:
			a.Counters.BriefRefinement++
			attempt.SemanticRound = a.Counters.BriefRefinement
			operation.SemanticCycle = currentCycle(a.Counters.BriefRefinementCycle)
		case CycleCounterExplorer:
			if a.Counters.Explorer == nil {
				a.Counters.Explorer = make(map[string]uint64)
			}
			if a.Counters.ExplorerCycle == nil {
				a.Counters.ExplorerCycle = make(map[string]uint64)
			}
			a.Counters.Explorer[operation.Episode]++
			attempt.SemanticRound = a.Counters.Explorer[operation.Episode]
			operation.SemanticCycle = currentCycle(a.Counters.ExplorerCycle[operation.Episode])
		}
	}
	operation.Attempts = append(operation.Attempts, attempt)
	return attempt, nil
}

func (r *Run) startRunAttempt(operation *Operation) (OperationAttempt, error) {
	if operation == nil || !validRunCounter(operation.Counter) || operation.UncountedResumeCheck {
		return OperationAttempt{}, fmt.Errorf("%w: invalid run attempt", ErrInvalidState)
	}
	attempt := OperationAttempt{Number: uint64(len(operation.Attempts) + 1)}
	if len(operation.Attempts) == 0 {
		switch operation.Counter {
		case CycleCounterFinalReview:
			r.FinalReviewRounds++
			attempt.SemanticRound = r.FinalReviewRounds
			operation.SemanticCycle = currentCycle(r.FinalReviewCycle)
		case CycleCounterExplorer:
			if r.RunExplorerCounters == nil {
				r.RunExplorerCounters = make(map[string]uint64)
			}
			if r.RunExplorerCycles == nil {
				r.RunExplorerCycles = make(map[string]uint64)
			}
			r.RunExplorerCounters[operation.Episode]++
			attempt.SemanticRound = r.RunExplorerCounters[operation.Episode]
			operation.SemanticCycle = currentCycle(r.RunExplorerCycles[operation.Episode])
		}
	} else if len(operation.Attempts) != 0 {
		attempt.SemanticRound = operation.Attempts[0].SemanticRound
	}
	operation.Attempts = append(operation.Attempts, attempt)
	return attempt, nil
}

func (a *Assignment) accept(e AcceptanceEvidence) error {
	return a.validateAcceptance(e, true)
}

func (a *Assignment) validateAcceptance(e AcceptanceEvidence, requireCurrentBrief bool) error {
	if !e.State.valid() || !e.Basis.valid() || !e.PendingCommit.valid() || !a.hasBrief(e.BriefID) || (requireCurrentBrief && (len(a.Briefs) == 0 || a.Briefs[len(a.Briefs)-1].ID != e.BriefID)) || len(e.CheckResultIDs) == 0 || e.ReviewResultID == "" || hasDuplicateResultIDs(e.CheckResultIDs) {
		return fmt.Errorf("%w: incomplete acceptance evidence", ErrInvalidState)
	}
	for _, resultID := range e.CheckResultIDs {
		result := a.result(resultID)
		if result == nil || result.Status != ResultSucceeded || result.State != e.State || result.Basis != e.Basis || a.operation(result.OperationID).Kind != OperationCheck || a.operation(result.OperationID).BriefID != e.BriefID || a.operation(result.OperationID).Basis != e.Basis {
			return fmt.Errorf("%w: check result %q does not prove accepted state", ErrInvalidState, resultID)
		}
	}
	review := a.result(e.ReviewResultID)
	if review == nil || review.Status != ResultSucceeded || review.State != e.State || review.Basis != e.Basis || a.operation(review.OperationID).Kind != OperationReview || a.operation(review.OperationID).BriefID != e.BriefID || a.operation(review.OperationID).Basis != e.Basis {
		return fmt.Errorf("%w: review result does not prove accepted state", ErrInvalidState)
	}
	return nil
}

func (r *Run) assignmentIndex(id AssignmentID) int {
	for index := range r.Assignments {
		if r.Assignments[index].ID == id {
			return index
		}
	}
	return -1
}

func (r *Run) activeAssignment(id AssignmentID) (*Assignment, error) {
	if err := r.requireActive(); err != nil {
		return nil, err
	}
	index := r.assignmentIndex(id)
	if index < 0 || r.Assignments[index].Status != AssignmentActive {
		return nil, fmt.Errorf("%w: assignment is not active", ErrInvalidTransition)
	}
	return &r.Assignments[index], nil
}

// assignmentForAttemptOutcome admits a paused run as well as an active one:
// a post-call safety check can pause the run before its already-dispatched
// attempt has been durably described.
func (r *Run) assignmentForAttemptOutcome(id AssignmentID) (*Assignment, error) {
	if r.Status != RunActive && r.Status != RunPaused {
		return nil, fmt.Errorf("%w: run cannot record an attempt outcome", ErrInvalidState)
	}
	index := r.assignmentIndex(id)
	if index < 0 || r.Assignments[index].Status != AssignmentActive {
		return nil, fmt.Errorf("%w: assignment is not active", ErrInvalidTransition)
	}
	return &r.Assignments[index], nil
}

func (r *Run) hasOpenAssignment() bool {
	for _, assignment := range r.Assignments {
		if assignment.Status != AssignmentCommitted {
			return true
		}
	}
	return false
}

func (r *Run) hasTask(id TaskID) bool {
	for _, task := range r.Tasks {
		if task.ID == id {
			return true
		}
	}
	return false
}

func (r *Run) children(id TaskID) []Task {
	var children []Task
	for _, task := range r.Tasks {
		if task.ParentID == id {
			children = append(children, task)
		}
	}
	return children
}

func (r *Run) currentBasis() AcceptanceBasis {
	return AcceptanceBasis{Specification: r.Identity.Specification, Configuration: r.Identity.Configuration}
}

func (r *Run) allLeafTasks() []TaskID {
	var leaves []TaskID
	for _, task := range r.Tasks {
		if r.isLeaf(task.ID) {
			leaves = append(leaves, task.ID)
		}
	}
	return leaves
}

func (r *Run) briefExists(id BriefID) bool {
	for _, assignment := range r.Assignments {
		if assignment.hasBrief(id) {
			return true
		}
	}
	return false
}

func (r *Run) operationExists(id OperationID) bool {
	for _, assignment := range r.Assignments {
		if assignment.operation(id) != nil {
			return true
		}
	}
	return r.runOperation(id) != nil
}

func (r *Run) resultExists(id ResultID) bool {
	for _, assignment := range r.Assignments {
		if assignment.result(id) != nil {
			return true
		}
	}
	return r.runResult(id) != nil
}

func (r *Run) runOperation(id OperationID) *Operation {
	for index := range r.RunOperations {
		if r.RunOperations[index].ID == id {
			return &r.RunOperations[index]
		}
	}
	return nil
}

func (r *Run) runResult(id ResultID) *OperationResult {
	for index := range r.RunResults {
		if r.RunResults[index].ID == id {
			return &r.RunResults[index]
		}
	}
	return nil
}

func (r *Run) hasRunResultForOperation(id OperationID) bool {
	for _, result := range r.RunResults {
		if result.OperationID == id {
			return true
		}
	}
	return false
}

func (r *Run) assignment(id AssignmentID) *Assignment {
	for index := range r.Assignments {
		if r.Assignments[index].ID == id {
			return &r.Assignments[index]
		}
	}
	return nil
}

func (a *Assignment) hasBrief(id BriefID) bool {
	for _, brief := range a.Briefs {
		if brief.ID == id {
			return true
		}
	}
	return false
}

func (a *Assignment) operation(id OperationID) *Operation {
	for index := range a.Operations {
		if a.Operations[index].ID == id {
			return &a.Operations[index]
		}
	}
	return nil
}

func (a *Assignment) result(id ResultID) *OperationResult {
	for index := range a.Results {
		if a.Results[index].ID == id {
			return &a.Results[index]
		}
	}
	return nil
}

func (a *Assignment) hasResultForOperation(id OperationID) bool {
	for _, result := range a.Results {
		if result.OperationID == id {
			return true
		}
	}
	return false
}

func hasDuplicates(values []TaskID) bool {
	seen := make(map[TaskID]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func hasDuplicateResultIDs(values []ResultID) bool {
	seen := make(map[ResultID]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func matchesIntent(evidence CommitEvidence, intent CommitIntent) bool {
	return evidence.OperationID == intent.OperationID && evidence.ParentCommit == intent.ParentCommit && evidence.Tree == intent.Tree && evidence.Message == intent.Message
}

func cloneResult(result OperationResult) OperationResult {
	result.Evidence = slices.Clone(result.Evidence)
	return result
}

func cloneAcceptance(evidence AcceptanceEvidence) *AcceptanceEvidence {
	evidence.CheckResultIDs = slices.Clone(evidence.CheckResultIDs)
	return &evidence
}

func cloneFinalAcceptance(evidence FinalAcceptanceEvidence) *FinalAcceptanceEvidence {
	evidence.CheckResultIDs = slices.Clone(evidence.CheckResultIDs)
	evidence.OpenFindingIDs = slices.Clone(evidence.OpenFindingIDs)
	return &evidence
}
