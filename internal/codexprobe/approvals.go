package codexprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime/codexapp"
	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
)

type RunStatus string

const (
	StatusStarting                   RunStatus = "starting"
	StatusReady                      RunStatus = "ready"
	StatusRunningTurn                RunStatus = "running_turn"
	StatusAwaitingControllerDecision RunStatus = "awaiting_controller_decision"
	StatusAwaitingOperator           RunStatus = "awaiting_operator"
	StatusCompleted                  RunStatus = "completed"
	StatusInterrupted                RunStatus = "interrupted"
	StatusFailed                     RunStatus = "failed"
)

type DecisionSource string

const (
	DecisionByPolicy   DecisionSource = "policy"
	DecisionByOperator DecisionSource = "operator"
)

type AccessPolicy = codexapp.AccessPolicy
type ApprovalKind = codexapp.ApprovalKind
type ApprovalDecision = codexapp.ApprovalDecision
type CommandForm = codexapp.CommandForm

const (
	CommandApproval       = codexapp.CommandApproval
	FileChangeApproval    = codexapp.FileChangeApproval
	PermissionsApproval   = codexapp.PermissionsApproval
	DecisionAccept        = codexapp.DecisionAccept
	DecisionDecline       = codexapp.DecisionDecline
	DecisionCancel        = codexapp.DecisionCancel
	DecisionAwaitOperator = codexapp.DecisionAwaitOperator
)

// PendingApproval is the complete durable representation. It intentionally
// excludes command text, reasons, permission payloads, and file contents.
type PendingApproval struct {
	SchemaVersion       int              `json:"schema_version"`
	RequestID           json.RawMessage  `json:"request_id"`
	ThreadID            string           `json:"thread_id"`
	TurnID              string           `json:"turn_id"`
	ItemID              string           `json:"item_id"`
	Kind                ApprovalKind     `json:"kind"`
	Status              string           `json:"status"`
	RequestedAt         time.Time        `json:"requested_at"`
	PolicySnapshotID    string           `json:"policy_snapshot_id"`
	CandidateSnapshotID *string          `json:"candidate_snapshot_id"`
	Decision            ApprovalDecision `json:"decision,omitempty"`
	DecisionSource      DecisionSource   `json:"decision_source,omitempty"`
	DecidedAt           *time.Time       `json:"decided_at,omitempty"`
	SentAt              *time.Time       `json:"sent_at,omitempty"`
}

type OperatorFunc func(PendingApproval) (ApprovalDecision, error)

type approvalRequest struct {
	message codexapp.Message
	decoded codexapp.ApprovalRequest
	pending PendingApproval
}

type operatorResult struct {
	key      string
	decision ApprovalDecision
	err      error
}

type approvalManager struct {
	mu               sync.Mutex
	statePath        string
	journal          *os.File
	workspace        string
	evaluator        *codexapp.ApprovalEvaluator
	policySnapshotID string
	operator         OperatorFunc
	status           RunStatus
	pending          map[string]*approvalRequest
	closed           []PendingApproval
	operatorOut      chan operatorResult
}

type durableProbeState struct {
	SchemaVersion int               `json:"schema_version"`
	Status        RunStatus         `json:"status"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Pending       []PendingApproval `json:"pending_approvals,omitempty"`
	Closed        []PendingApproval `json:"closed_approvals,omitempty"`
}

type approvalJournalEvent struct {
	SchemaVersion int             `json:"schema_version"`
	Timestamp     time.Time       `json:"timestamp"`
	Type          string          `json:"type"`
	Approval      PendingApproval `json:"approval"`
}

func newApprovalManager(statePath string, journal *os.File, workspace string, policy AccessPolicy, operator OperatorFunc) (*approvalManager, error) {
	evaluator, err := codexapp.NewApprovalEvaluator(workspace, policy)
	if err != nil {
		return nil, err
	}
	policySnapshotID, err := evaluator.PolicySnapshotID()
	if err != nil {
		return nil, err
	}
	manager := &approvalManager{
		statePath: statePath, journal: journal, workspace: workspace, evaluator: evaluator,
		policySnapshotID: policySnapshotID, operator: operator,
		status: StatusStarting, pending: make(map[string]*approvalRequest), operatorOut: make(chan operatorResult, 16),
	}
	if data, err := os.ReadFile(statePath); err == nil {
		var previous durableProbeState
		if err := json.Unmarshal(data, &previous); err != nil {
			return nil, fmt.Errorf("read previous approval state: %w", err)
		}
		now := time.Now().UTC()
		for _, pending := range previous.Pending {
			if pending.Status == "sent" || pending.Status == "failed_closed" {
				continue
			}
			pending.Status, pending.Decision, pending.DecisionSource, pending.DecidedAt = "failed_closed", DecisionCancel, DecisionByPolicy, &now
			manager.closed = append(manager.closed, pending)
			if err := manager.appendJournal("failed_closed_after_restart", pending); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := manager.persistState(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (manager *approvalManager) setStatus(status RunStatus) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.status = status
	return manager.persistStateLocked()
}

func (manager *approvalManager) persistState() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.persistStateLocked()
}

func (manager *approvalManager) persistStateLocked() error {
	pending := make([]PendingApproval, 0, len(manager.pending))
	for _, request := range manager.pending {
		pending = append(pending, request.pending)
	}
	sort.Slice(pending, func(i, j int) bool { return string(pending[i].RequestID) < string(pending[j].RequestID) })
	return writeJSONAtomic(manager.statePath, durableProbeState{1, manager.status, time.Now().UTC(), pending, manager.closed})
}

func (manager *approvalManager) appendJournal(eventType string, approval PendingApproval) error {
	if err := json.NewEncoder(manager.journal).Encode(approvalJournalEvent{1, time.Now().UTC(), eventType, approval}); err != nil {
		return err
	}
	return manager.journal.Sync()
}

func (manager *approvalManager) register(message codexapp.Message) (*approvalRequest, ApprovalDecision, error) {
	decoded, err := manager.evaluator.Decode(message)
	if err != nil {
		return nil, "", err
	}
	candidateID, err := candidateSnapshotID(manager.workspace)
	if err != nil {
		return nil, "", err
	}
	requestID, err := json.Marshal(message.ID)
	if err != nil {
		return nil, "", err
	}
	request := &approvalRequest{message: message, decoded: decoded, pending: PendingApproval{
		SchemaVersion: 1, RequestID: requestID, ThreadID: decoded.ThreadID,
		TurnID: decoded.TurnID, ItemID: decoded.ItemID, Kind: decoded.Kind, Status: "pending", RequestedAt: time.Now().UTC(),
		PolicySnapshotID: manager.policySnapshotID, CandidateSnapshotID: candidateID,
	}}
	manager.mu.Lock()
	manager.pending[message.ID.Key()] = request
	manager.status = StatusAwaitingControllerDecision
	if err := manager.persistStateLocked(); err != nil {
		delete(manager.pending, message.ID.Key())
		manager.mu.Unlock()
		return nil, "", err
	}
	if err := manager.appendJournal("pending", request.pending); err != nil {
		delete(manager.pending, message.ID.Key())
		manager.mu.Unlock()
		return nil, "", err
	}
	manager.mu.Unlock()

	decision := manager.evaluator.Evaluate(decoded)
	if decision == DecisionAwaitOperator {
		if manager.operator == nil {
			return request, DecisionDecline, nil
		}
		manager.mu.Lock()
		manager.status = StatusAwaitingOperator
		err = manager.persistStateLocked()
		manager.mu.Unlock()
		if err != nil {
			return nil, "", err
		}
		go func(key string, pending PendingApproval) {
			decision, err := manager.operator(pending)
			manager.operatorOut <- operatorResult{key, decision, err}
		}(message.ID.Key(), request.pending)
	}
	return request, decision, nil
}

func (manager *approvalManager) resolve(transport *codexapp.Transport, request *approvalRequest, decision ApprovalDecision, source DecisionSource) error {
	if decision != DecisionAccept && decision != DecisionDecline && decision != DecisionCancel {
		return fmt.Errorf("invalid final approval decision %q", decision)
	}
	manager.mu.Lock()
	current, ok := manager.pending[request.message.ID.Key()]
	if !ok || current.pending.Status != "pending" {
		manager.mu.Unlock()
		return codexapp.ErrDuplicateResponse
	}
	manager.mu.Unlock()

	candidateID, err := candidateSnapshotID(manager.workspace)
	if err != nil {
		return err
	}
	if manager.policySnapshotID != request.pending.PolicySnapshotID || !sameOptionalString(candidateID, request.pending.CandidateSnapshotID) {
		decision, source = DecisionDecline, DecisionByPolicy
	}
	if decision == DecisionAccept && manager.evaluator.Evaluate(request.decoded) == DecisionDecline {
		decision, source = DecisionDecline, DecisionByPolicy
	}
	now := time.Now().UTC()
	manager.mu.Lock()
	request.pending.Status, request.pending.Decision, request.pending.DecisionSource, request.pending.DecidedAt = "decided", decision, source, &now
	if err := manager.persistStateLocked(); err != nil {
		manager.mu.Unlock()
		return err
	}
	if err := manager.appendJournal("decided", request.pending); err != nil {
		manager.mu.Unlock()
		return err
	}
	manager.mu.Unlock()
	if err := transport.SendResult(request.message.ID, request.decoded.Response(decision)); err != nil {
		return err
	}
	sent := time.Now().UTC()
	manager.mu.Lock()
	request.pending.Status, request.pending.SentAt = "sent", &sent
	manager.closed = append(manager.closed, request.pending)
	delete(manager.pending, request.message.ID.Key())
	manager.status = StatusRunningTurn
	if err := manager.appendJournal("sent", request.pending); err != nil {
		manager.mu.Unlock()
		return err
	}
	err = manager.persistStateLocked()
	manager.mu.Unlock()
	return err
}

func (manager *approvalManager) continueOperator(result operatorResult, transport *codexapp.Transport) error {
	manager.mu.Lock()
	request, ok := manager.pending[result.key]
	manager.mu.Unlock()
	if !ok {
		return codexapp.ErrDuplicateResponse
	}
	if result.err != nil || result.decision != DecisionAccept && result.decision != DecisionDecline && result.decision != DecisionCancel {
		return manager.resolve(transport, request, DecisionDecline, DecisionByPolicy)
	}
	return manager.resolve(transport, request, result.decision, DecisionByOperator)
}

func (manager *approvalManager) failClosed() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := time.Now().UTC()
	for key, request := range manager.pending {
		if request.pending.Status == "sent" || request.pending.Status == "failed_closed" {
			continue
		}
		request.pending.Status, request.pending.Decision, request.pending.DecisionSource, request.pending.DecidedAt = "failed_closed", DecisionCancel, DecisionByPolicy, &now
		if err := manager.appendJournal("failed_closed", request.pending); err != nil {
			return err
		}
		manager.closed = append(manager.closed, request.pending)
		delete(manager.pending, key)
	}
	manager.status = StatusFailed
	return manager.persistStateLocked()
}

func (manager *approvalManager) hasPending() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return len(manager.pending) != 0
}

func (manager *approvalManager) observeFileChanges(message codexapp.Message, threadID, turnID string) error {
	return manager.evaluator.Observe(message, threadID, turnID)
}

func candidateSnapshotID(workspace string) (*string, error) {
	if _, err := os.Stat(filepath.Join(workspace, ".git")); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	snapshot, err := gitsnapshot.Capture(context.Background(), workspace)
	if err != nil {
		return nil, err
	}
	value := snapshot.HeadOID + ":" + snapshot.TreeOID
	return &value, nil
}

func sameOptionalString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
