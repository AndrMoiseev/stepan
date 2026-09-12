package implementationstate

import (
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

// Operation records a controller-requested agent, check, or review action.
// It is intentionally descriptive; dispatch and attempt accounting are added
// by later layers.
type Operation struct {
	ID          OperationID
	Kind        OperationKind
	BriefID     BriefID
	Basis       AcceptanceBasis
	Description string
}

func (o Operation) valid() bool {
	return o.ID != "" && o.Kind.valid() && o.Basis.valid()
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
	Identity               RunIdentity
	Status                 RunStatus
	CurrentState           EvidenceRef
	Tasks                  []Task
	LeafStatus             map[TaskID]TaskStatus
	Assignments            []Assignment
	RunOperations          []Operation
	RunResults             []OperationResult
	FinalAcceptance        *FinalAcceptanceEvidence
	FinalAcceptanceHistory []FinalAcceptanceEvidence
	PauseReason            string
	CloseReason            string
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
		if !operation.valid() || operation.BriefID != "" || operations[operation.ID] {
			return fmt.Errorf("%w: invalid run operation", ErrInvalidState)
		}
		operations[operation.ID] = true
	}
	for _, result := range r.RunResults {
		operation := r.runOperation(result.OperationID)
		if !result.valid() || operation == nil || results[result.ID] || result.Basis != operation.Basis {
			return fmt.Errorf("%w: invalid run result", ErrInvalidState)
		}
		results[result.ID] = true
	}
	if r.FinalAcceptance != nil {
		if err := r.validateFinalAcceptance(*r.FinalAcceptance, true); err != nil {
			return err
		}
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
		if r.PauseReason != "" || r.CloseReason != "" {
			return fmt.Errorf("%w: active run has stop reason", ErrInvalidState)
		}
	case RunPaused:
		if strings.TrimSpace(r.PauseReason) == "" || r.CloseReason != "" {
			return fmt.Errorf("%w: paused run lacks pause reason", ErrInvalidState)
		}
	case RunClosed:
		if strings.TrimSpace(r.CloseReason) == "" || r.PauseReason != "" {
			return fmt.Errorf("%w: closed run lacks close reason", ErrInvalidState)
		}
	case RunSucceeded:
		if r.PauseReason != "" || r.CloseReason != "" {
			return fmt.Errorf("%w: succeeded run has stop reason", ErrInvalidState)
		}
	}
	return nil
}

func (r *Run) validateStructure() error {
	if r.Identity.validate() != nil || len(r.Tasks) == 0 {
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
	if !operation.valid() || operation.BriefID == "" || r.operationExists(operation.ID) || !assignment.hasBrief(operation.BriefID) {
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
	if !result.valid() || r.resultExists(result.ID) || operation == nil || result.Basis != operation.Basis {
		return fmt.Errorf("%w: invalid operation result", ErrInvalidState)
	}
	assignment.Results = append(assignment.Results, cloneResult(result))
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
	if !operation.valid() || operation.BriefID != "" || r.operationExists(operation.ID) {
		return fmt.Errorf("%w: invalid run operation", ErrInvalidState)
	}
	r.RunOperations = append(r.RunOperations, operation)
	return nil
}

func (r *Run) AddRunResult(result OperationResult) error {
	if err := r.requireActive(); err != nil {
		return err
	}
	operation := r.runOperation(result.OperationID)
	if !result.valid() || r.resultExists(result.ID) || operation == nil || result.Basis != operation.Basis {
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
	r.Status, r.PauseReason = RunPaused, reason
	return nil
}

func (r *Run) Resume() error {
	if r.Status != RunPaused {
		return fmt.Errorf("%w: only a paused run can resume", ErrInvalidTransition)
	}
	r.Status, r.PauseReason = RunActive, ""
	return nil
}

func (r *Run) Close(reason string) error {
	if (r.Status != RunActive && r.Status != RunPaused) || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: run cannot close", ErrInvalidTransition)
	}
	r.Status, r.CloseReason, r.PauseReason = RunClosed, reason, ""
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
		if !operation.valid() || operation.BriefID == "" || !assignment.hasBrief(operation.BriefID) {
			return fmt.Errorf("%w: invalid operation", ErrInvalidState)
		}
	}
	for _, result := range assignment.Results {
		operation := assignment.operation(result.OperationID)
		if !result.valid() || operation == nil || result.Basis != operation.Basis {
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
