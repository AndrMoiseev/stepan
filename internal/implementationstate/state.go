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
	Specification  EvidenceRef
	TaskList       EvidenceRef
	Configuration  EvidenceRef
}

func (i RunIdentity) validate() error {
	if i.ID == "" || i.Change == "" || i.Repository == "" || i.WorkCopy == "" || i.Branch == "" || i.BaselineCommit == "" || !i.Specification.valid() || !i.TaskList.valid() || !i.Configuration.valid() {
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
	Description string
}

func (o Operation) valid() bool {
	return o.ID != "" && o.Kind.valid() && o.BriefID != ""
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
	Evidence    []EvidenceRef
}

func (r OperationResult) valid() bool {
	if r.ID == "" || r.OperationID == "" || !r.Status.valid() {
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
}

func (c CommitEvidence) valid() bool {
	return c.OperationID != "" && c.CommitID != "" && c.ParentCommit != "" && c.Tree != "" && strings.TrimSpace(c.Message) != "" && c.State.valid()
}

// Assignment retains selection, brief versions, operations and results for a
// contiguous block of leaf tasks. A selected task is not accepted or complete
// until its respective transition is applied.
type Assignment struct {
	ID         AssignmentID
	TaskIDs    []TaskID
	Status     AssignmentStatus
	Briefs     []BriefVersion
	Operations []Operation
	Results    []OperationResult
	Acceptance *AcceptanceEvidence
	Commit     *CommitEvidence
}

// Run is the portable in-memory representation for future JSONL and SQLite
// stores. Markdown checkbox state is deliberately absent.
type Run struct {
	Identity    RunIdentity
	Status      RunStatus
	Tasks       []Task
	LeafStatus  map[TaskID]TaskStatus
	Assignments []Assignment
	PauseReason string
	CloseReason string
}

// NewRun validates the extracted ordered hierarchy and creates a new active
// run. All leaf tasks start pending.
func NewRun(identity RunIdentity, tasks []Task) (*Run, error) {
	run := &Run{Identity: identity, Status: RunActive, Tasks: slices.Clone(tasks)}
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
	if r == nil || r.Identity.validate() != nil || !r.Status.valid() {
		return fmt.Errorf("%w: invalid run", ErrInvalidState)
	}
	if err := r.validateStructure(); err != nil {
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
		if len(assignment.TaskIDs) > len(leafOrder)-leafCursor || !slices.Equal(assignment.TaskIDs, leafOrder[leafCursor:leafCursor+len(assignment.TaskIDs)]) {
			return fmt.Errorf("%w: assignment tasks are not a contiguous leaf prefix", ErrInvalidState)
		}
		leafCursor += len(assignment.TaskIDs)
		if assignment.Status != AssignmentCommitted {
			openAssignments++
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
	if !operation.valid() || r.operationExists(operation.ID) || !assignment.hasBrief(operation.BriefID) {
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
	if !result.valid() || r.resultExists(result.ID) || assignment.operation(result.OperationID) == nil {
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
	if err := assignment.accept(evidence); err != nil {
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
	if assignment.Status != AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil || !evidence.valid() || !matchesIntent(evidence, assignment.Acceptance.PendingCommit) || evidence.State != assignment.Acceptance.State {
		return fmt.Errorf("%w: assignment is not accepted for this commit state", ErrInvalidTransition)
	}
	assignment.Commit = &evidence
	assignment.Status = AssignmentCommitted
	for _, taskID := range assignment.TaskIDs {
		r.LeafStatus[taskID] = TaskComplete
	}
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

// Succeed closes a fully committed run successfully. Final checks and review
// are represented by later controller work; this model enforces only the task
// completion invariant.
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
	r.Status = RunSucceeded
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
		if !operation.valid() || !assignment.hasBrief(operation.BriefID) {
			return fmt.Errorf("%w: invalid operation", ErrInvalidState)
		}
	}
	for _, result := range assignment.Results {
		if !result.valid() || assignment.operation(result.OperationID) == nil {
			return fmt.Errorf("%w: invalid result", ErrInvalidState)
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
		if assignment.Acceptance == nil || assignment.accept(*assignment.Acceptance) != nil || assignment.Commit == nil || !assignment.Commit.valid() || !matchesIntent(*assignment.Commit, assignment.Acceptance.PendingCommit) || assignment.Commit.State != assignment.Acceptance.State {
			return fmt.Errorf("%w: invalid committed assignment", ErrInvalidState)
		}
	}
	return nil
}

func (a *Assignment) accept(e AcceptanceEvidence) error {
	if !e.State.valid() || !e.PendingCommit.valid() || !a.hasBrief(e.BriefID) || len(a.Briefs) == 0 || a.Briefs[len(a.Briefs)-1].ID != e.BriefID || len(e.CheckResultIDs) == 0 || e.ReviewResultID == "" || hasDuplicateResultIDs(e.CheckResultIDs) {
		return fmt.Errorf("%w: incomplete acceptance evidence", ErrInvalidState)
	}
	for _, resultID := range e.CheckResultIDs {
		result := a.result(resultID)
		if result == nil || result.Status != ResultSucceeded || result.State != e.State || a.operation(result.OperationID).Kind != OperationCheck || a.operation(result.OperationID).BriefID != e.BriefID {
			return fmt.Errorf("%w: check result %q does not prove accepted state", ErrInvalidState, resultID)
		}
	}
	review := a.result(e.ReviewResultID)
	if review == nil || review.Status != ResultSucceeded || review.State != e.State || a.operation(review.OperationID).Kind != OperationReview || a.operation(review.OperationID).BriefID != e.BriefID {
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
	return false
}

func (r *Run) resultExists(id ResultID) bool {
	for _, assignment := range r.Assignments {
		if assignment.result(id) != nil {
			return true
		}
	}
	return false
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
