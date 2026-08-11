package codexapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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

type ApprovalKind string

const (
	CommandApproval     ApprovalKind = "command"
	FileChangeApproval  ApprovalKind = "file_change"
	PermissionsApproval ApprovalKind = "permissions"
)

type ApprovalDecision string

const (
	DecisionAccept        ApprovalDecision = "accept"
	DecisionDecline       ApprovalDecision = "decline"
	DecisionCancel        ApprovalDecision = "cancel"
	DecisionAwaitOperator ApprovalDecision = "await_operator"
)

type DecisionSource string

const (
	DecisionByPolicy   DecisionSource = "policy"
	DecisionByOperator DecisionSource = "operator"
)

type CommandForm struct {
	Command string `json:"command"`
	CWD     string `json:"cwd"`
}

// AccessPolicy is deliberately an exact allowlist. Command strings are data,
// never shell-parsed, and permission paths must be contained by an allowed root.
type AccessPolicy struct {
	ReadableRoots     []string       `json:"readable_roots"`
	WritableRoots     []string       `json:"writable_roots"`
	ProtectedPaths    []string       `json:"protected_paths,omitempty"`
	ProtectedPatterns []string       `json:"protected_patterns,omitempty"`
	AllowedCommands   []CommandForm  `json:"allowed_commands,omitempty"`
	NetworkAccess     bool           `json:"network_access"`
	OperatorDecisions []ApprovalKind `json:"operator_decisions,omitempty"`
}

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

type normalizedPolicy struct {
	ReadableRoots     []string       `json:"readable_roots"`
	WritableRoots     []string       `json:"writable_roots"`
	ProtectedPaths    []string       `json:"protected_paths"`
	ProtectedPatterns []string       `json:"protected_patterns"`
	AllowedCommands   []CommandForm  `json:"allowed_commands"`
	NetworkAccess     bool           `json:"network_access"`
	OperatorDecisions []ApprovalKind `json:"operator_decisions"`
}

type approvalRequest struct {
	message     Message
	pending     PendingApproval
	ids         approvalIDs
	permissions permissionProfile
}

type operatorResult struct {
	key      string
	decision ApprovalDecision
	err      error
}

type approvalManager struct {
	mu          sync.Mutex
	statePath   string
	journal     *os.File
	workspace   string
	policy      normalizedPolicy
	operator    OperatorFunc
	status      RunStatus
	pending     map[string]*approvalRequest
	closed      []PendingApproval
	fileChanges map[string][]string
	operatorOut chan operatorResult
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

func normalizePolicy(policy AccessPolicy, workspace string) (normalizedPolicy, error) {
	if len(policy.ReadableRoots) == 0 {
		policy.ReadableRoots = []string{workspace}
	}
	result := normalizedPolicy{NetworkAccess: policy.NetworkAccess}
	var err error
	if result.ReadableRoots, err = normalizePaths(policy.ReadableRoots); err != nil {
		return normalizedPolicy{}, fmt.Errorf("readable roots: %w", err)
	}
	if result.WritableRoots, err = normalizePaths(policy.WritableRoots); err != nil {
		return normalizedPolicy{}, fmt.Errorf("writable roots: %w", err)
	}
	if result.ProtectedPaths, err = normalizePaths(policy.ProtectedPaths); err != nil {
		return normalizedPolicy{}, fmt.Errorf("protected paths: %w", err)
	}
	for _, pattern := range policy.ProtectedPatterns {
		pattern = filepath.ToSlash(filepath.Clean(pattern))
		if filepath.Separator == '\\' {
			pattern = strings.ToLower(pattern)
		}
		if _, err := path.Match(pattern, "probe"); err != nil {
			return normalizedPolicy{}, fmt.Errorf("protected pattern %q: %w", pattern, err)
		}
		result.ProtectedPatterns = append(result.ProtectedPatterns, pattern)
	}
	sort.Strings(result.ProtectedPatterns)
	for _, command := range policy.AllowedCommands {
		command.Command = normalizeCommand(command.Command)
		if command.Command == "" || unsafeCommand(command.Command) {
			return normalizedPolicy{}, fmt.Errorf("unsafe allowed command %q", command.Command)
		}
		command.CWD, err = canonicalPath(command.CWD)
		if err != nil {
			return normalizedPolicy{}, fmt.Errorf("allowed command cwd: %w", err)
		}
		result.AllowedCommands = append(result.AllowedCommands, command)
	}
	sort.Slice(result.AllowedCommands, func(i, j int) bool {
		if result.AllowedCommands[i].CWD == result.AllowedCommands[j].CWD {
			return result.AllowedCommands[i].Command < result.AllowedCommands[j].Command
		}
		return result.AllowedCommands[i].CWD < result.AllowedCommands[j].CWD
	})
	for _, kind := range policy.OperatorDecisions {
		if kind != CommandApproval && kind != FileChangeApproval && kind != PermissionsApproval {
			return normalizedPolicy{}, fmt.Errorf("unknown operator decision kind %q", kind)
		}
		result.OperatorDecisions = append(result.OperatorDecisions, kind)
	}
	sort.Slice(result.OperatorDecisions, func(i, j int) bool { return result.OperatorDecisions[i] < result.OperatorDecisions[j] })
	return result, nil
}

func normalizePaths(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	for _, value := range paths {
		normalized, err := canonicalPath(value)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result, nil
}

func canonicalPath(value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("path %q must be absolute", value)
	}
	value = filepath.Clean(value)
	probe := value
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", err
		}
		suffix = append(suffix, filepath.Base(probe))
		probe = parent
	}
}

func newApprovalManager(statePath string, journal *os.File, workspace string, policy AccessPolicy, operator OperatorFunc) (*approvalManager, error) {
	normalized, err := normalizePolicy(policy, workspace)
	if err != nil {
		return nil, err
	}
	manager := &approvalManager{
		statePath: statePath, journal: journal, workspace: workspace, policy: normalized, operator: operator,
		status: StatusStarting, pending: make(map[string]*approvalRequest), fileChanges: make(map[string][]string), operatorOut: make(chan operatorResult, 16),
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

func (manager *approvalManager) register(message Message) (*approvalRequest, ApprovalDecision, error) {
	kind, ids, permissions, err := decodeApproval(message)
	if err != nil {
		return nil, "", err
	}
	policyID, err := manager.policySnapshotID()
	if err != nil {
		return nil, "", err
	}
	candidateID, err := candidateSnapshotID(manager.workspace)
	if err != nil {
		return nil, "", err
	}
	request := &approvalRequest{message: message, ids: ids, permissions: permissions, pending: PendingApproval{
		SchemaVersion: 1, RequestID: append(json.RawMessage(nil), message.ID.raw...), ThreadID: ids.ThreadID,
		TurnID: ids.TurnID, ItemID: ids.ItemID, Kind: kind, Status: "pending", RequestedAt: time.Now().UTC(),
		PolicySnapshotID: policyID, CandidateSnapshotID: candidateID,
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

	decision := manager.evaluate(kind, ids, permissions)
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

func (manager *approvalManager) resolve(transport *Transport, request *approvalRequest, decision ApprovalDecision, source DecisionSource) error {
	if decision != DecisionAccept && decision != DecisionDecline && decision != DecisionCancel {
		return fmt.Errorf("invalid final approval decision %q", decision)
	}
	manager.mu.Lock()
	current, ok := manager.pending[request.message.ID.Key()]
	if !ok || current.pending.Status != "pending" {
		manager.mu.Unlock()
		return ErrDuplicateResponse
	}
	manager.mu.Unlock()

	policyID, err := manager.policySnapshotID()
	if err != nil {
		return err
	}
	candidateID, err := candidateSnapshotID(manager.workspace)
	if err != nil {
		return err
	}
	if policyID != request.pending.PolicySnapshotID || !sameOptionalString(candidateID, request.pending.CandidateSnapshotID) {
		decision, source = DecisionDecline, DecisionByPolicy
	}
	if decision == DecisionAccept && manager.evaluate(request.pending.Kind, request.ids, request.permissions) == DecisionDecline {
		decision, source = DecisionDecline, DecisionByPolicy
	}

	now := time.Now().UTC()
	manager.mu.Lock()
	current.pending.Status, current.pending.Decision, current.pending.DecisionSource, current.pending.DecidedAt = "decided", decision, source, &now
	if err := manager.persistStateLocked(); err != nil {
		manager.mu.Unlock()
		return err
	}
	if err := manager.appendJournal("decided", current.pending); err != nil {
		manager.mu.Unlock()
		return err
	}
	manager.mu.Unlock()

	if err := transport.SendResult(request.message.ID, approvalResponse(request, decision)); err != nil {
		return err
	}

	now = time.Now().UTC()
	manager.mu.Lock()
	current.pending.Status, current.pending.SentAt = "sent", &now
	if err := manager.appendJournal("sent", current.pending); err != nil {
		manager.mu.Unlock()
		return err
	}
	delete(manager.pending, request.message.ID.Key())
	if len(manager.pending) == 0 {
		manager.status = StatusRunningTurn
	}
	err = manager.persistStateLocked()
	manager.mu.Unlock()
	return err
}

func (manager *approvalManager) continueOperator(result operatorResult, transport *Transport) error {
	manager.mu.Lock()
	request, ok := manager.pending[result.key]
	manager.mu.Unlock()
	if !ok {
		return ErrDuplicateResponse
	}
	if result.err != nil {
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
		request.pending.Status = "failed_closed"
		request.pending.Decision = DecisionCancel
		request.pending.DecisionSource = DecisionByPolicy
		request.pending.DecidedAt = &now
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

func (manager *approvalManager) policySnapshotID() (string, error) {
	data, err := json.Marshal(manager.policy)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
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

type approvalIDs struct {
	ThreadID                        string            `json:"threadId"`
	TurnID                          string            `json:"turnId"`
	ItemID                          string            `json:"itemId"`
	Command                         *string           `json:"command"`
	CWD                             *string           `json:"cwd"`
	GrantRoot                       *string           `json:"grantRoot"`
	ProposedExecpolicyAmendment     []string          `json:"proposedExecpolicyAmendment"`
	ProposedNetworkPolicyAmendments []json.RawMessage `json:"proposedNetworkPolicyAmendments"`
	NetworkApprovalContext          json.RawMessage   `json:"networkApprovalContext"`
}

type permissionProfile struct {
	FileSystem *fileSystemPermissions `json:"fileSystem,omitempty"`
	Network    *networkPermissions    `json:"network,omitempty"`
}

type fileSystemPermissions struct {
	Entries          []permissionEntry `json:"entries,omitempty"`
	GlobScanMaxDepth *uint             `json:"globScanMaxDepth,omitempty"`
	Read             []string          `json:"read,omitempty"`
	Write            []string          `json:"write,omitempty"`
}

type permissionEntry struct {
	Access string         `json:"access"`
	Path   permissionPath `json:"path"`
}

type permissionPath struct {
	Type    string          `json:"type"`
	Path    string          `json:"path,omitempty"`
	Pattern string          `json:"pattern,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type networkPermissions struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type fileChangeEvidence struct {
	ThreadID string   `json:"threadId"`
	TurnID   string   `json:"turnId"`
	ItemID   string   `json:"itemId"`
	Paths    []string `json:"paths"`
}

func (manager *approvalManager) observeFileChanges(message Message, threadID, turnID string) error {
	evidence, relevant, err := decodeFileChangeEvidence(message)
	if err != nil || !relevant {
		return err
	}
	if evidence.ThreadID != threadID || evidence.TurnID != turnID || evidence.ItemID == "" {
		return errors.New("file change notification correlation IDs do not match the running turn")
	}
	paths := make([]string, 0, len(evidence.Paths))
	for _, value := range evidence.Paths {
		if !filepath.IsAbs(value) {
			value = filepath.Join(manager.workspace, value)
		}
		normalized, err := canonicalPath(value)
		if err != nil {
			return err
		}
		paths = append(paths, normalized)
	}
	manager.fileChanges[fileChangeKey(evidence.ThreadID, evidence.TurnID, evidence.ItemID)] = paths
	return nil
}

func decodeFileChangeEvidence(message Message) (fileChangeEvidence, bool, error) {
	type change struct {
		Path string `json:"path"`
	}
	var threadID, turnID, itemID string
	var changes []change
	switch message.Method {
	case "item/started":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Item     struct {
				ID      string   `json:"id"`
				Type    string   `json:"type"`
				Changes []change `json:"changes"`
			} `json:"item"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fileChangeEvidence{}, true, err
		}
		if params.Item.Type != "fileChange" {
			return fileChangeEvidence{}, false, nil
		}
		threadID, turnID, itemID, changes = params.ThreadID, params.TurnID, params.Item.ID, params.Item.Changes
	case "item/fileChange/patchUpdated":
		var params struct {
			ThreadID string   `json:"threadId"`
			TurnID   string   `json:"turnId"`
			ItemID   string   `json:"itemId"`
			Changes  []change `json:"changes"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return fileChangeEvidence{}, true, err
		}
		threadID, turnID, itemID, changes = params.ThreadID, params.TurnID, params.ItemID, params.Changes
	default:
		return fileChangeEvidence{}, false, nil
	}
	evidence := fileChangeEvidence{ThreadID: threadID, TurnID: turnID, ItemID: itemID}
	for _, change := range changes {
		if change.Path == "" {
			return fileChangeEvidence{}, true, errors.New("file change notification has empty path")
		}
		evidence.Paths = append(evidence.Paths, change.Path)
	}
	return evidence, true, nil
}

func fileChangeKey(threadID, turnID, itemID string) string {
	return threadID + "\x00" + turnID + "\x00" + itemID
}

func decodeApproval(message Message) (ApprovalKind, approvalIDs, permissionProfile, error) {
	var ids approvalIDs
	if err := json.Unmarshal(message.Params, &ids); err != nil || ids.ThreadID == "" || ids.TurnID == "" || ids.ItemID == "" {
		return "", approvalIDs{}, permissionProfile{}, errors.New("invalid approval correlation fields")
	}
	var kind ApprovalKind
	var permissions permissionProfile
	switch message.Method {
	case "item/commandExecution/requestApproval":
		kind = CommandApproval
	case "item/fileChange/requestApproval":
		kind = FileChangeApproval
	case "item/permissions/requestApproval":
		kind = PermissionsApproval
		var params map[string]json.RawMessage
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return "", approvalIDs{}, permissionProfile{}, errors.New("invalid permissions approval")
		}
		raw, ok := params["permissions"]
		if !ok {
			return "", approvalIDs{}, permissionProfile{}, errors.New("invalid permissions approval")
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&permissions); err != nil {
			return "", approvalIDs{}, permissionProfile{}, errors.New("invalid permissions approval")
		}
	default:
		return "", approvalIDs{}, permissionProfile{}, fmt.Errorf("unsupported server request %q", message.Method)
	}
	return kind, ids, permissions, nil
}

func (manager *approvalManager) evaluate(kind ApprovalKind, ids approvalIDs, permissions permissionProfile) ApprovalDecision {
	switch kind {
	case CommandApproval:
		if ids.Command == nil || ids.CWD == nil || len(ids.ProposedExecpolicyAmendment) != 0 || len(ids.ProposedNetworkPolicyAmendments) != 0 {
			return DecisionDecline
		}
		if len(ids.NetworkApprovalContext) != 0 && string(ids.NetworkApprovalContext) != "null" && !manager.policy.NetworkAccess {
			return DecisionDecline
		}
		cwd, err := canonicalPath(*ids.CWD)
		if err != nil {
			return DecisionDecline
		}
		command := normalizeCommand(*ids.Command)
		if unsafeCommand(command) {
			return DecisionDecline
		}
		for _, allowed := range manager.policy.AllowedCommands {
			if allowed.Command == command && samePath(allowed.CWD, cwd) {
				return manager.acceptOrAwait(kind)
			}
		}
		return DecisionDecline
	case FileChangeApproval:
		if ids.GrantRoot != nil {
			return DecisionDecline
		}
		paths := manager.fileChanges[fileChangeKey(ids.ThreadID, ids.TurnID, ids.ItemID)]
		if len(paths) == 0 {
			return DecisionDecline
		}
		for _, value := range paths {
			if !manager.allowedRequestedPath(value, manager.policy.WritableRoots) {
				return DecisionDecline
			}
		}
		return manager.acceptOrAwait(kind)
	case PermissionsApproval:
		if ids.CWD == nil {
			return DecisionDecline
		}
		cwd, err := canonicalPath(*ids.CWD)
		if err != nil || !manager.allowedPath(cwd, manager.policy.ReadableRoots) {
			return DecisionDecline
		}
		if permissions.Network != nil && permissions.Network.Enabled != nil && *permissions.Network.Enabled && !manager.policy.NetworkAccess {
			return DecisionDecline
		}
		if permissions.FileSystem != nil {
			if permissions.FileSystem.GlobScanMaxDepth != nil {
				return DecisionDecline
			}
			for _, value := range permissions.FileSystem.Read {
				if !manager.allowedRequestedPath(value, manager.policy.ReadableRoots) {
					return DecisionDecline
				}
			}
			for _, value := range permissions.FileSystem.Write {
				if !manager.allowedRequestedPath(value, manager.policy.WritableRoots) {
					return DecisionDecline
				}
			}
			for _, entry := range permissions.FileSystem.Entries {
				if entry.Path.Type != "path" || entry.Path.Path == "" {
					return DecisionDecline
				}
				roots := manager.policy.ReadableRoots
				if entry.Access == "write" {
					roots = manager.policy.WritableRoots
				} else if entry.Access != "read" && entry.Access != "deny" {
					return DecisionDecline
				}
				if !manager.allowedRequestedPath(entry.Path.Path, roots) {
					return DecisionDecline
				}
			}
		}
		return manager.acceptOrAwait(kind)
	default:
		return DecisionDecline
	}
}

func (manager *approvalManager) acceptOrAwait(kind ApprovalKind) ApprovalDecision {
	for _, required := range manager.policy.OperatorDecisions {
		if required == kind {
			return DecisionAwaitOperator
		}
	}
	return DecisionAccept
}

func (manager *approvalManager) allowedRequestedPath(value string, roots []string) bool {
	normalized, err := canonicalPath(value)
	return err == nil && manager.allowedPath(normalized, roots)
}

func (manager *approvalManager) allowedPath(value string, roots []string) bool {
	for _, protected := range manager.policy.ProtectedPaths {
		if within(protected, value) {
			return false
		}
	}
	slashed := filepath.ToSlash(value)
	if filepath.Separator == '\\' {
		slashed = strings.ToLower(slashed)
	}
	for _, pattern := range manager.policy.ProtectedPatterns {
		matched, _ := path.Match(pattern, slashed)
		if matched {
			return false
		}
	}
	for _, root := range roots {
		relative, err := filepath.Rel(root, value)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !protectedByName(relative) {
			return true
		}
	}
	return false
}

func protectedByName(value string) bool {
	for _, component := range strings.FieldsFunc(filepath.ToSlash(value), func(r rune) bool { return r == '/' }) {
		switch strings.ToLower(component) {
		case ".git", ".stepan", ".codex", ".agents", ".ssh", ".aws", ".azure", ".env", ".npmrc", ".pypirc", ".netrc", ".git-credentials", "credentials", "credentials.json", "secret", "secrets", "id_rsa", "id_ed25519":
			return true
		}
	}
	return false
}

func normalizeCommand(command string) string {
	return strings.TrimSpace(command)
}

func unsafeCommand(command string) bool {
	if command == "" || strings.ContainsAny(command, "\r\n;&|><`") || strings.Contains(command, "$(") {
		return true
	}
	fields := strings.Fields(strings.ToLower(command))
	if len(fields) == 0 {
		return true
	}
	executable := strings.TrimSuffix(filepath.Base(fields[0]), ".exe")
	switch executable {
	case "sh", "bash", "zsh", "cmd", "powershell", "pwsh", "rm", "rmdir", "del", "erase", "remove-item", "format", "shutdown", "diskpart":
		return true
	case "git":
		return len(fields) > 1 && (fields[1] == "clean" || fields[1] == "reset")
	}
	return false
}

func within(root, value string) bool {
	relative, err := filepath.Rel(root, value)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func approvalResponse(request *approvalRequest, decision ApprovalDecision) any {
	if request.pending.Kind == PermissionsApproval {
		if decision == DecisionAccept {
			return struct {
				Permissions permissionProfile `json:"permissions"`
				Scope       string            `json:"scope"`
			}{request.permissions, "turn"}
		}
		return struct {
			Permissions permissionProfile `json:"permissions"`
			Scope       string            `json:"scope"`
		}{permissionProfile{}, "turn"}
	}
	return struct {
		Decision ApprovalDecision `json:"decision"`
	}{decision}
}
