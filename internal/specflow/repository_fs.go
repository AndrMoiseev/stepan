package specflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const recoveryManifestName = ".stepan-recovery.json"

type repositoryFaults interface {
	Before(step, relativePath string) error
}

type noRepositoryFaults struct{}

func (noRepositoryFaults) Before(string, string) error { return nil }

type pendingDraft struct {
	hash string
	ids  []StableID
}

// FSFeatureRepository is the filesystem adapter at the FeatureRepository
// seam. Its internal transaction manifest makes a whole domain mutation
// recoverable without exposing file ordering to callers.
type FSFeatureRepository struct {
	mu        sync.Mutex
	root      string
	now       func() time.Time
	faults    repositoryFaults
	baselines map[string]map[string]string
	pending   map[string]pendingDraft
}

var _ FeatureRepository = (*FSFeatureRepository)(nil)

func NewFSFeatureRepository(root string) (*FSFeatureRepository, error) {
	return newFSFeatureRepository(root, noRepositoryFaults{})
}

func newFSFeatureRepository(root string, faults repositoryFaults) (*FSFeatureRepository, error) {
	root, err := canonicalExisting(root)
	if err != nil {
		return nil, fmt.Errorf("canonicalize repository root: %w", err)
	}
	if faults == nil {
		faults = noRepositoryFaults{}
	}
	return &FSFeatureRepository{
		root: root, now: time.Now, faults: faults,
		baselines: make(map[string]map[string]string), pending: make(map[string]pendingDraft),
	}, nil
}

func (r *FSFeatureRepository) Create(request CreateFeatureRequest) (FeatureSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ValidateFeatureID(request.FeatureID); err != nil {
		return FeatureSnapshot{}, err
	}
	if strings.TrimSpace(request.Brief) == "" {
		return FeatureSnapshot{}, fmt.Errorf("%w: feature brief is required", ErrInvalidDomainValue)
	}
	if request.At.IsZero() {
		request.At = r.now()
	}
	if err := r.ensureCleanRepository(); err != nil {
		return FeatureSnapshot{}, err
	}
	target, err := FeatureTargetForID(r.root, request.FeatureID)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if _, err := os.Lstat(target.Directory); err == nil {
		return FeatureSnapshot{}, fmt.Errorf("feature %s already exists", request.FeatureID)
	} else if !os.IsNotExist(err) {
		return FeatureSnapshot{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target.Directory), 0o755); err != nil {
		return FeatureSnapshot{}, fmt.Errorf("create features directory: %w", err)
	}
	if err := os.Mkdir(target.Directory, 0o755); err != nil {
		return FeatureSnapshot{}, fmt.Errorf("create feature directory: %w", err)
	}
	state := NewFlowState()
	stateBytes, err := encodeState(state)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	journalBytes, _, err := newMemLogBoundToState(request.FeatureID, request.Brief, request.At, hash(stateBytes))
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if err := r.applyMutation(target, "create", map[string][]byte{
		"state.json": stateBytes, "mem-log.md": journalBytes,
	}); err != nil {
		// A failure before the recovery manifest exists did not durably start the
		// mutation. Roll back only a provably empty directory; otherwise preserve
		// every byte for explicit recovery diagnostics.
		if _, manifestErr := os.Lstat(filepath.Join(target.Directory, recoveryManifestName)); os.IsNotExist(manifestErr) {
			if entries, readErr := os.ReadDir(target.Directory); readErr == nil && len(entries) == 0 {
				_ = os.Remove(target.Directory)
			}
		}
		return FeatureSnapshot{}, err
	}
	return r.loadLocked(request.FeatureID)
}

func (r *FSFeatureRepository) Load(featureID string) (FeatureSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadLocked(featureID)
}

func (r *FSFeatureRepository) loadLocked(featureID string) (FeatureSnapshot, error) {
	return r.loadLockedCore(featureID, true)
}

func (r *FSFeatureRepository) loadLockedCore(featureID string, recoverPhase bool) (FeatureSnapshot, error) {
	target, err := FeatureTargetForID(r.root, featureID)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	info, err := os.Lstat(target.Directory)
	if err != nil {
		return FeatureSnapshot{}, fmt.Errorf("load feature %s: %w", featureID, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return FeatureSnapshot{}, fmt.Errorf("%w: feature path must be a non-link directory", ErrRepositoryBlocked)
	}
	recovery, err := r.recoverLocked(target)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if len(recovery.Diagnostics) > 0 {
		return FeatureSnapshot{}, fmt.Errorf("%w: %s", ErrRepositoryBlocked, recovery.Diagnostics[0].Message)
	}
	if recoverPhase {
		phaseRecovery, err := r.recoverPhaseLocked(featureID)
		if err != nil {
			return FeatureSnapshot{}, err
		}
		if len(phaseRecovery.Diagnostics) > 0 {
			return FeatureSnapshot{}, fmt.Errorf("%w: %s", ErrRepositoryBlocked, phaseRecovery.Diagnostics[0].Message)
		}
		if phaseRecovery.Completed {
			info, err = os.Lstat(target.Directory)
			if err != nil {
				return FeatureSnapshot{}, fmt.Errorf("load feature %s after recovery: %w", featureID, err)
			}
		}
	}

	stateBytes, err := readRegularFile(target.StatePath)
	if err != nil {
		return FeatureSnapshot{}, fmt.Errorf("%w: read state.json: %v", ErrRepositoryBlocked, err)
	}
	state, err := DecodeFlowState(stateBytes)
	if err != nil {
		return FeatureSnapshot{}, fmt.Errorf("%w: invalid state.json: %v", ErrRepositoryBlocked, err)
	}
	canonicalState, err := encodeState(state)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if !bytes.Equal(stateBytes, canonicalState) {
		return FeatureSnapshot{}, fmt.Errorf("%w: state.json is not in Stepan canonical form", ErrRepositoryBlocked)
	}

	journalBytes, err := readRegularFile(target.JournalPath)
	if err != nil {
		return FeatureSnapshot{}, fmt.Errorf("%w: read mem-log.md: %v", ErrRepositoryBlocked, err)
	}
	journal, err := parseMemLog(journalBytes, featureID)
	if err != nil {
		return FeatureSnapshot{}, fmt.Errorf("%w: invalid mem-log.md: %v", ErrRepositoryBlocked, err)
	}
	if journal[len(journal)-1].StateHash == "" || journal[len(journal)-1].StateHash != hash(stateBytes) {
		return FeatureSnapshot{}, fmt.Errorf("%w: state.json does not match the state hash anchored by mem-log.md", ErrRepositoryBlocked)
	}

	snapshot := FeatureSnapshot{
		Target: target, State: state, Documents: make(map[Stage]DocumentArtifact), Journal: journal,
	}
	changes := ChangeInspection{}
	for _, stage := range stages {
		stageState, _ := state.Stage(stage)
		path, _ := target.DocumentPath(stage)
		data, readErr := readOptionalRegularFile(path)
		switch {
		case readErr != nil:
			return FeatureSnapshot{}, fmt.Errorf("%w: inspect %s.md: %v", ErrRepositoryBlocked, stage, readErr)
		case data == nil && stageState.CurrentHash == "":
			continue
		case data == nil:
			changes.DocumentRevisions = append(changes.DocumentRevisions, documentChange(target, stage, stageState.CurrentHash, "", true))
		case stageState.CurrentHash == "":
			changes.DocumentRevisions = append(changes.DocumentRevisions, documentChange(target, stage, "", hash(data), false))
			snapshot.Documents[stage] = documentArtifact(target, stage, data)
		case hash(data) != stageState.CurrentHash:
			changes.DocumentRevisions = append(changes.DocumentRevisions, documentChange(target, stage, stageState.CurrentHash, hash(data), false))
			snapshot.Documents[stage] = documentArtifact(target, stage, data)
		default:
			snapshot.Documents[stage] = documentArtifact(target, stage, data)
		}
	}

	reviews, reviewChanges, err := r.loadReviews(target, state)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	snapshot.Reviews = reviews
	changes.Blocking = append(changes.Blocking, reviewChanges...)
	snapshot.Changes = changes
	if len(changes.Blocking) > 0 {
		return FeatureSnapshot{}, changes.Error()
	}
	r.baselines[featureID] = managedHashes(target, state)
	return snapshot, nil
}

func documentArtifact(target FeatureTarget, stage Stage, data []byte) DocumentArtifact {
	path, _ := target.DisplayDocumentPath(stage)
	return DocumentArtifact{Stage: stage, Path: path, Hash: hash(data), Content: append([]byte(nil), data...)}
}

func documentChange(target FeatureTarget, stage Stage, expected, actual string, missing bool) ArtifactChange {
	path, _ := target.DisplayDocumentPath(stage)
	message := fmt.Sprintf("%s changed outside Stepan", path)
	if missing {
		message = fmt.Sprintf("%s was removed outside Stepan", path)
	}
	return ArtifactChange{Class: ChangeDocumentRevision, Path: path, Stage: stage, ExpectedHash: expected, ActualHash: actual, Missing: missing, Message: message}
}

func encodeState(state FlowState) ([]byte, error) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode flow state: %w", err)
	}
	return append(data, '\n'), nil
}

func (r *FSFeatureRepository) InspectAuthorDraft(request DraftArtifactRequest) (DraftInspection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inspectAuthorDraftLocked(request, false)
}

func (r *FSFeatureRepository) inspectAuthorDraftLocked(request DraftArtifactRequest, publish bool) (DraftInspection, error) {
	if err := validateDraftRequest(request); err != nil {
		return DraftInspection{}, err
	}
	if request.At.IsZero() {
		request.At = r.now()
	}
	feature, err := r.ensureMutableLocked(request.FeatureID)
	if err != nil {
		return DraftInspection{}, err
	}
	filename, _ := feature.Target.ArtifactFilename(request.Stage, false)
	data, err := r.readExternalArtifact(request.ArtifactRoot, filename)
	if err != nil {
		return DraftInspection{}, err
	}
	draftHash := hash(data)
	if request.ExpectedHash != "" && request.ExpectedHash != draftHash {
		return DraftInspection{}, fmt.Errorf("pending %s draft changed: expected %s, got %s", request.Stage, request.ExpectedHash, draftHash)
	}

	retained := append([]StableID(nil), request.RetainedIDs...)
	key := pendingDraftKey(request.FeatureID, request.Stage)
	if pending, ok := r.pending[key]; ok && pending.hash == draftHash {
		retained = append(retained, pending.ids...)
	}
	result := ParseDocument(DocumentRequest{
		Kind: documentKindForStage(request.Stage), Mode: request.Mode, Markdown: string(data),
		ActiveIDs: request.ActiveIDs, IssuedIDs: feature.State.IssuedIDs(), RetainedIDs: retained,
	})
	state := reserveObservedIDs(feature.State, result.ObservedIDs)
	r.pending[key] = pendingDraft{hash: draftHash, ids: observedStableIDs(result.ObservedIDs)}

	eventKind := MemLogAttempt
	body := fmt.Sprintf("author draft observed for %s: hash=%s valid=%t diagnostics=%d", request.Stage, draftHash, result.Valid(), len(result.Diagnostics))
	if publish && result.Valid() {
		eventKind = MemLogAgentMessage
		body = fmt.Sprintf("author draft published for %s: hash=%s", request.Stage, draftHash)
		snapshot := state.Snapshot()
		stageState := snapshot.Stages[request.Stage]
		stageState.Status = StagePublished
		stageState.CurrentHash = draftHash
		stageState.UpstreamHashes = append([]UpstreamHash(nil), request.UpstreamHashes...)
		snapshot.Stages[request.Stage] = stageState
		state, err = NewFlowStateFromSnapshot(snapshot)
		if err != nil {
			return DraftInspection{}, err
		}
	}
	entry, err := NewMemLogEntry(request.Stage, mustAuthorRole(request.Stage), eventKind, request.At, body)
	if err != nil {
		return DraftInspection{}, err
	}
	stateBytes, err := encodeState(state)
	if err != nil {
		return DraftInspection{}, err
	}
	journalBytes, err := appendFeatureJournal(feature, entry, hash(stateBytes))
	if err != nil {
		return DraftInspection{}, err
	}
	writes := map[string][]byte{"state.json": stateBytes, "mem-log.md": journalBytes}
	if publish && result.Valid() {
		writes[filename] = data
	}
	if err := r.applyMutation(feature.Target, "author-draft", writes); err != nil {
		return DraftInspection{}, err
	}
	if publish && result.Valid() {
		delete(r.pending, key)
	}
	loaded, err := r.loadLocked(request.FeatureID)
	if err != nil {
		return DraftInspection{}, err
	}
	return DraftInspection{Hash: draftHash, Validation: result, State: loaded.State}, nil
}

func (r *FSFeatureRepository) PublishAuthorDraft(request DraftArtifactRequest) (DraftPublication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inspection, err := r.inspectAuthorDraftLocked(request, true)
	if err != nil {
		return DraftPublication{}, err
	}
	feature, err := r.loadLocked(request.FeatureID)
	if err != nil {
		return DraftPublication{}, err
	}
	return DraftPublication{
		Published: inspection.Validation.Valid(), Hash: inspection.Hash,
		Validation: inspection.Validation, Feature: feature,
	}, nil
}

func validateDraftRequest(request DraftArtifactRequest) error {
	if strings.TrimSpace(request.FeatureID) == "" || !request.Stage.Valid() || !request.Mode.Valid() {
		return fmt.Errorf("%w: invalid author draft request", ErrInvalidDomainValue)
	}
	for _, upstream := range request.UpstreamHashes {
		if !upstream.Stage.Valid() || strings.TrimSpace(upstream.Hash) == "" || upstream.Stage.order() >= request.Stage.order() {
			return fmt.Errorf("%w: invalid upstream hash for %s draft", ErrInvalidDomainValue, request.Stage)
		}
	}
	return nil
}

func documentKindForStage(stage Stage) DocumentKind {
	switch stage {
	case StageIntent:
		return DocumentIntent
	case StageSpec:
		return DocumentSpec
	case StagePlan:
		return DocumentPlan
	default:
		return ""
	}
}

func reviewKindForStage(stage Stage) DocumentKind {
	if stage == StageSpec {
		return DocumentSpecReview
	}
	if stage == StagePlan {
		return DocumentPlanReview
	}
	return ""
}

func mustAuthorRole(stage Stage) Role {
	role, err := AuthorRole(stage)
	if err != nil {
		panic(err)
	}
	return role
}

func reserveObservedIDs(state FlowState, observed []ObservedID) FlowState {
	snapshot := state.Snapshot()
	seen := make(map[StableID]struct{}, len(snapshot.IssuedIDs)+len(observed))
	for _, id := range snapshot.IssuedIDs {
		seen[id] = struct{}{}
	}
	for _, item := range observed {
		if !item.ID.Valid() {
			continue
		}
		if _, exists := seen[item.ID]; exists {
			continue
		}
		seen[item.ID] = struct{}{}
		snapshot.IssuedIDs = append(snapshot.IssuedIDs, item.ID)
	}
	updated, err := NewFlowStateFromSnapshot(snapshot)
	if err != nil {
		panic("reserve parser-observed IDs: " + err.Error())
	}
	return updated
}

func observedStableIDs(observed []ObservedID) []StableID {
	result := make([]StableID, 0, len(observed))
	seen := make(map[StableID]struct{}, len(observed))
	for _, item := range observed {
		if !item.ID.Valid() {
			continue
		}
		if _, exists := seen[item.ID]; exists {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, item.ID)
	}
	return result
}

func pendingDraftKey(featureID string, stage Stage) string {
	return featureID + "\x00" + string(stage)
}

func appendFeatureJournal(feature FeatureSnapshot, entry MemLogEntry, stateHash string) ([]byte, error) {
	data, err := readRegularFile(feature.Target.JournalPath)
	if err != nil {
		return nil, err
	}
	if len(feature.Journal) == 0 {
		return nil, fmt.Errorf("mem-log has no entries")
	}
	entry.Sequence = uint64(len(feature.Journal))
	entry.StateHash = stateHash
	updated, _, err := appendMemLogBytes(data, entry, feature.Journal[len(feature.Journal)-1].Checksum)
	return updated, err
}

func (r *FSFeatureRepository) PublishReview(request ReviewArtifactRequest) (ReviewPublication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateReviewRequest(request); err != nil {
		return ReviewPublication{}, err
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = r.now()
	}
	if request.UpdatedAt.IsZero() {
		request.UpdatedAt = request.CreatedAt
	}
	feature, err := r.ensureMutableLocked(request.FeatureID)
	if err != nil {
		return ReviewPublication{}, err
	}
	data, err := r.readExternalArtifact(request.ArtifactRoot, "review.md")
	if err != nil {
		return ReviewPublication{}, err
	}
	result := ParseDocument(DocumentRequest{
		Kind: reviewKindForStage(request.Stage), Mode: ValidateDraft, Markdown: string(data),
		ActiveIDs: request.ActiveIDs, IssuedIDs: feature.State.IssuedIDs(), PreviousFindings: request.PreviousFindings,
	})
	state := reserveObservedIDs(feature.State, result.ObservedIDs)
	runID := request.RunID
	stageState, _ := state.Stage(request.Stage)
	if runID == 0 {
		for _, run := range stageState.Reviews {
			if run.ID >= runID {
				runID = run.ID + 1
			}
		}
		if runID == 0 {
			runID = 1
		}
	}

	role, _ := ReviewerRole(request.Stage)
	eventBody := fmt.Sprintf("review artifact rejected for %s run %d: diagnostics=%d", request.Stage, runID, len(result.Diagnostics))
	writes := make(map[string][]byte)
	if result.Valid() {
		report, err := formatReviewReport(request, runID, data)
		if err != nil {
			return ReviewPublication{}, err
		}
		relative := reviewRelativePath(request.Stage, runID)
		snapshot := state.Snapshot()
		stageState = snapshot.Stages[request.Stage]
		accepted := cloneFingerprintPointer(request.Accepted)
		run := ReviewRun{
			ID: runID, Path: filepath.ToSlash(relative), Status: request.Status,
			OriginalFingerprint: request.Original, AcceptedFingerprint: accepted,
			Attempts: request.Attempts, ReportHash: hash(report),
		}
		updated := false
		for index := range stageState.Reviews {
			if stageState.Reviews[index].ID == runID {
				if stageState.Reviews[index].Path != run.Path || !stageState.Reviews[index].OriginalFingerprint.Equal(request.Original) {
					return ReviewPublication{}, fmt.Errorf("%w: review run %d identity is immutable", ErrInvalidDomainValue, runID)
				}
				stageState.Reviews[index] = run
				updated = true
				break
			}
		}
		if !updated {
			stageState.Reviews = append(stageState.Reviews, run)
			sort.Slice(stageState.Reviews, func(i, j int) bool { return stageState.Reviews[i].ID < stageState.Reviews[j].ID })
		}
		stageState.ReviewStatus = request.Status
		snapshot.Stages[request.Stage] = stageState
		state, err = NewFlowStateFromSnapshot(snapshot)
		if err != nil {
			return ReviewPublication{}, err
		}
		writes[relative] = report
		eventBody = fmt.Sprintf("review artifact published for %s run %d: status=%s path=%s", request.Stage, runID, request.Status, filepath.ToSlash(relative))
	}
	entry, err := NewMemLogEntry(request.Stage, role, MemLogReview, request.UpdatedAt, eventBody)
	if err != nil {
		return ReviewPublication{}, err
	}
	stateBytes, err := encodeState(state)
	if err != nil {
		return ReviewPublication{}, err
	}
	journalBytes, err := appendFeatureJournal(feature, entry, hash(stateBytes))
	if err != nil {
		return ReviewPublication{}, err
	}
	writes["state.json"] = stateBytes
	writes["mem-log.md"] = journalBytes
	if err := r.applyMutation(feature.Target, "review", writes); err != nil {
		return ReviewPublication{}, err
	}
	loaded, err := r.loadLocked(request.FeatureID)
	if err != nil {
		return ReviewPublication{}, err
	}
	path := ""
	if result.Valid() {
		path = filepath.ToSlash(reviewRelativePath(request.Stage, runID))
	}
	return ReviewPublication{Published: result.Valid(), RunID: runID, Path: path, Validation: result, Feature: loaded}, nil
}

func validateReviewRequest(request ReviewArtifactRequest) error {
	if strings.TrimSpace(request.FeatureID) == "" || (request.Stage != StageSpec && request.Stage != StagePlan) ||
		!request.Status.Valid() || !request.Original.Valid() || request.Attempts < 0 || request.Attempts > DefaultRetryLimit {
		return fmt.Errorf("%w: invalid review artifact request", ErrInvalidDomainValue)
	}
	if request.Accepted != nil && !request.Accepted.Valid() {
		return fmt.Errorf("%w: invalid accepted review fingerprint", ErrInvalidDomainValue)
	}
	for name, value := range map[string]string{"provider": request.Provider, "model": request.Model} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%w: review %s must be a single non-empty line", ErrInvalidDomainValue, name)
		}
	}
	return nil
}

func cloneFingerprintPointer(value *Fingerprint) *Fingerprint {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func reviewRelativePath(stage Stage, runID uint64) string {
	return filepath.Join("reviews", fmt.Sprintf("%s-%03d.md", stage, runID))
}

func formatReviewReport(request ReviewArtifactRequest, runID uint64, body []byte) ([]byte, error) {
	originalUpstream := request.Original.UpstreamHashes()
	acceptedTarget := ""
	acceptedUpstream := []UpstreamHash(nil)
	if request.Accepted != nil {
		acceptedTarget = request.Accepted.TargetHash()
		acceptedUpstream = request.Accepted.UpstreamHashes()
	}
	prefix := strings.ToUpper(string(request.Stage))
	var result strings.Builder
	result.WriteString("---\n")
	fmt.Fprintf(&result, "review_id: %s-REVIEW-%03d\n", prefix, runID)
	fmt.Fprintf(&result, "stage: %s\n", request.Stage)
	fmt.Fprintf(&result, "status: %s\n", request.Status)
	fmt.Fprintf(&result, "created_at: %s\n", request.CreatedAt.Format(time.RFC3339Nano))
	fmt.Fprintf(&result, "updated_at: %s\n", request.UpdatedAt.Format(time.RFC3339Nano))
	fmt.Fprintf(&result, "provider: %s\n", strconv.Quote(request.Provider))
	fmt.Fprintf(&result, "model: %s\n", strconv.Quote(request.Model))
	fmt.Fprintf(&result, "started_revision: %s\n", request.Original.TargetHash())
	fmt.Fprintf(&result, "accepted_for_revision: %s\n", acceptedTarget)
	writeReviewUpstream(&result, "upstream_started", originalUpstream)
	writeReviewUpstream(&result, "upstream_accepted", acceptedUpstream)
	fmt.Fprintf(&result, "attempts: %d\n", request.Attempts)
	result.WriteString("---\n\n")
	result.Write(bytes.TrimSpace(body))
	result.WriteByte('\n')
	return []byte(result.String()), nil
}

func writeReviewUpstream(result *strings.Builder, field string, values []UpstreamHash) {
	if len(values) == 0 {
		fmt.Fprintf(result, "%s: {}\n", field)
		return
	}
	fmt.Fprintf(result, "%s:\n", field)
	for _, upstream := range values {
		fmt.Fprintf(result, "  %s.md: %s\n", upstream.Stage, upstream.Hash)
	}
}

func (r *FSFeatureRepository) RecordDecision(featureID string, stage Stage, role Role, decision Decision) (FeatureSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateDecision(decision); err != nil {
		return FeatureSnapshot{}, err
	}
	feature, err := r.ensureMutableLocked(featureID)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	next := 1
	for _, entry := range feature.Journal {
		if entry.Kind == MemLogDecision {
			next++
		}
	}
	for _, superseded := range decision.Supersedes {
		if superseded <= 0 || superseded >= next {
			return FeatureSnapshot{}, fmt.Errorf("decision supersedes unknown or future decision %d", superseded)
		}
	}
	alternatives := "(none)"
	if len(decision.Alternatives) > 0 {
		alternatives = strings.Join(decision.Alternatives, "; ")
	}
	body := fmt.Sprintf("## Decision D-%03d\n\nauthor: %s\n\ndecision: %s\n\nrationale: %s\n\nalternatives: %s\n\nsupersedes: %v",
		next, decision.Author, decision.Decision, decision.Rationale, alternatives, decision.Supersedes)
	entry, err := NewMemLogEntry(stage, role, MemLogDecision, r.now(), body)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	return r.recordActivityLocked(feature, entry)
}

func (r *FSFeatureRepository) RecordActivity(featureID string, entry MemLogEntry) (FeatureSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	feature, err := r.ensureMutableLocked(featureID)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if entry.At.IsZero() {
		entry.At = r.now()
	}
	return r.recordActivityLocked(feature, entry)
}

func (r *FSFeatureRepository) recordActivityLocked(feature FeatureSnapshot, entry MemLogEntry) (FeatureSnapshot, error) {
	stateBytes, err := encodeState(feature.State)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	journalBytes, err := appendFeatureJournal(feature, entry, hash(stateBytes))
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if err := r.applyMutation(feature.Target, "journal", map[string][]byte{"mem-log.md": journalBytes}); err != nil {
		return FeatureSnapshot{}, err
	}
	return r.loadLocked(feature.Target.ID)
}

func (r *FSFeatureRepository) DiscardPending(featureID string, stage Stage, artifactRoot string) (FeatureSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !stage.Valid() {
		return FeatureSnapshot{}, domainError("stage", stage)
	}
	feature, err := r.ensureMutableLocked(featureID)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if err := r.validateArtifactRoot(artifactRoot); err != nil {
		return FeatureSnapshot{}, err
	}
	if err := removeArtifact(artifactRoot); err != nil {
		return FeatureSnapshot{}, fmt.Errorf("discard pending artifact: %w", err)
	}
	delete(r.pending, pendingDraftKey(featureID, stage))
	entry, err := NewMemLogEntry(stage, mustAuthorRole(stage), MemLogSession, r.now(), "pending draft discarded; published revision preserved")
	if err != nil {
		return FeatureSnapshot{}, err
	}
	return r.recordActivityLocked(feature, entry)
}

func (r *FSFeatureRepository) InspectChanges(featureID string) (ChangeInspection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inspectChangesLocked(featureID)
}

func (r *FSFeatureRepository) inspectChangesLocked(featureID string) (ChangeInspection, error) {
	baseline, exists := r.baselines[featureID]
	if !exists {
		feature, err := r.loadLocked(featureID)
		if err != nil {
			return ChangeInspection{}, err
		}
		return feature.Changes, nil
	}
	target, err := FeatureTargetForID(r.root, featureID)
	if err != nil {
		return ChangeInspection{}, err
	}
	inspection := ChangeInspection{}
	for relative, expected := range baseline {
		actual, missing, readErr := managedFileHash(filepath.Join(target.Directory, filepath.FromSlash(relative)))
		if readErr != nil {
			return ChangeInspection{}, readErr
		}
		if !missing && actual == expected {
			continue
		}
		stage, document := stageForDocumentRelative(relative)
		if document {
			inspection.DocumentRevisions = append(inspection.DocumentRevisions, documentChange(target, stage, expected, actual, missing))
			continue
		}
		inspection.Blocking = append(inspection.Blocking, protectedChange(target, relative, expected, actual, missing))
	}
	for _, stage := range stages {
		relative := string(stage) + ".md"
		if _, tracked := baseline[relative]; tracked {
			continue
		}
		actual, missing, readErr := managedFileHash(filepath.Join(target.Directory, relative))
		if readErr != nil {
			return ChangeInspection{}, readErr
		}
		if !missing {
			inspection.DocumentRevisions = append(inspection.DocumentRevisions, documentChange(target, stage, "", actual, false))
		}
	}
	reviewEntries, err := os.ReadDir(target.ReviewsDirectory)
	if err != nil && !os.IsNotExist(err) {
		return ChangeInspection{}, err
	}
	for _, entry := range reviewEntries {
		relative := filepath.ToSlash(filepath.Join("reviews", entry.Name()))
		if _, tracked := baseline[relative]; tracked {
			continue
		}
		actual, missing, readErr := managedFileHash(filepath.Join(target.Directory, filepath.FromSlash(relative)))
		if readErr != nil {
			return ChangeInspection{}, readErr
		}
		inspection.Blocking = append(inspection.Blocking, protectedChange(target, relative, "", actual, missing))
	}
	sortChanges(&inspection)
	return inspection, nil
}

func sortChanges(inspection *ChangeInspection) {
	sort.Slice(inspection.DocumentRevisions, func(i, j int) bool {
		return inspection.DocumentRevisions[i].Path < inspection.DocumentRevisions[j].Path
	})
	sort.Slice(inspection.Blocking, func(i, j int) bool { return inspection.Blocking[i].Path < inspection.Blocking[j].Path })
}

func protectedChange(target FeatureTarget, relative, expected, actual string, missing bool) ArtifactChange {
	display := filepath.ToSlash(filepath.Join(featuresDirectoryPath, target.ID, relative))
	verb := "changed"
	if missing {
		verb = "was removed"
	} else if expected == "" {
		verb = "was added"
	}
	return ArtifactChange{
		Class: ChangeProtectedArtifact, Path: display, ExpectedHash: expected, ActualHash: actual, Missing: missing,
		Message: fmt.Sprintf("protected artifact %s %s outside Stepan", display, verb),
	}
}

func stageForDocumentRelative(relative string) (Stage, bool) {
	for _, stage := range stages {
		if relative == string(stage)+".md" {
			return stage, true
		}
	}
	return "", false
}

func (r *FSFeatureRepository) ensureMutableLocked(featureID string) (FeatureSnapshot, error) {
	if _, exists := r.baselines[featureID]; exists {
		inspection, err := r.inspectChangesLocked(featureID)
		if err != nil {
			return FeatureSnapshot{}, err
		}
		if err := inspection.Error(); err != nil {
			return FeatureSnapshot{}, err
		}
	}
	feature, err := r.loadLocked(featureID)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if err := feature.Changes.Error(); err != nil {
		return FeatureSnapshot{}, err
	}
	return feature, nil
}

func managedHashes(target FeatureTarget, state FlowState) map[string]string {
	result := make(map[string]string)
	for _, relative := range []string{"state.json", "mem-log.md"} {
		actual, missing, err := managedFileHash(filepath.Join(target.Directory, filepath.FromSlash(relative)))
		if err == nil && !missing {
			result[relative] = actual
		}
	}
	for _, stage := range stages {
		stageState, _ := state.Stage(stage)
		if stageState.CurrentHash != "" {
			result[string(stage)+".md"] = stageState.CurrentHash
		}
	}
	entries, err := os.ReadDir(target.ReviewsDirectory)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			relative := filepath.ToSlash(filepath.Join("reviews", entry.Name()))
			actual, missing, hashErr := managedFileHash(filepath.Join(target.Directory, filepath.FromSlash(relative)))
			if hashErr == nil && !missing {
				result[relative] = actual
			}
		}
	}
	return result
}

func managedFileHash(path string) (actual string, missing bool, err error) {
	data, err := readOptionalRegularFile(path)
	if err != nil {
		return "", false, err
	}
	if data == nil {
		return "", true, nil
	}
	return hash(data), false, nil
}

func (r *FSFeatureRepository) loadReviews(target FeatureTarget, state FlowState) ([]ReviewArtifact, []ArtifactChange, error) {
	expected := make(map[string]struct{})
	reviews := make([]ReviewArtifact, 0)
	changes := make([]ArtifactChange, 0)
	for _, stage := range []Stage{StageSpec, StagePlan} {
		stageState, _ := state.Stage(stage)
		for _, run := range stageState.Reviews {
			relative := filepath.ToSlash(run.Path)
			want := filepath.ToSlash(reviewRelativePath(stage, run.ID))
			if relative != want {
				return nil, nil, fmt.Errorf("%w: review run %d path %q does not match %q", ErrRepositoryBlocked, run.ID, relative, want)
			}
			expected[relative] = struct{}{}
			absolute := filepath.Join(target.Directory, filepath.FromSlash(relative))
			data, err := readOptionalRegularFile(absolute)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: inspect %s: %v", ErrRepositoryBlocked, relative, err)
			}
			if data == nil || run.ReportHash == "" || hash(data) != run.ReportHash {
				actual := ""
				if data != nil {
					actual = hash(data)
				}
				changes = append(changes, protectedChange(target, relative, run.ReportHash, actual, data == nil))
				continue
			}
			if err := validateReviewFrontMatter(data, stage, run); err != nil {
				return nil, nil, fmt.Errorf("%w: %s: %v", ErrRepositoryBlocked, relative, err)
			}
			reviews = append(reviews, ReviewArtifact{
				Stage: stage, RunID: run.ID, Path: filepath.ToSlash(filepath.Join(featuresDirectoryPath, target.ID, relative)),
				Hash: hash(data), Content: append([]byte(nil), data...),
			})
		}
	}
	intentState, _ := state.Stage(StageIntent)
	if len(intentState.Reviews) > 0 {
		return nil, nil, fmt.Errorf("%w: intent cannot have review runs", ErrRepositoryBlocked)
	}
	entries, err := os.ReadDir(target.ReviewsDirectory)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	for _, entry := range entries {
		relative := filepath.ToSlash(filepath.Join("reviews", entry.Name()))
		if entry.IsDir() {
			changes = append(changes, protectedChange(target, relative, "", "directory", false))
			continue
		}
		if _, ok := expected[relative]; !ok {
			actual, _, _ := managedFileHash(filepath.Join(target.Directory, filepath.FromSlash(relative)))
			changes = append(changes, protectedChange(target, relative, "", actual, false))
		}
	}
	sort.Slice(reviews, func(i, j int) bool {
		if reviews[i].Stage != reviews[j].Stage {
			return reviews[i].Stage.order() < reviews[j].Stage.order()
		}
		return reviews[i].RunID < reviews[j].RunID
	})
	return reviews, changes, nil
}

func validateReviewFrontMatter(data []byte, stage Stage, run ReviewRun) error {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return fmt.Errorf("missing YAML front matter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return fmt.Errorf("unterminated YAML front matter")
	}
	front := text[4 : 4+end]
	required := []string{
		fmt.Sprintf("review_id: %s-REVIEW-%03d", strings.ToUpper(string(stage)), run.ID),
		"stage: " + string(stage), "status: " + string(run.Status),
		"started_revision: " + run.OriginalFingerprint.TargetHash(),
		fmt.Sprintf("attempts: %d", run.Attempts),
	}
	for _, value := range required {
		if !strings.Contains(front, value) {
			return fmt.Errorf("front matter does not match state field %q", value)
		}
	}
	return nil
}

func (r *FSFeatureRepository) readExternalArtifact(root, filename string) ([]byte, error) {
	if err := r.validateArtifactRoot(root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, filename)
	if !withinPath(root, path) {
		return nil, fmt.Errorf("artifact path escapes external root")
	}
	data, err := readRegularFile(path)
	if err != nil {
		return nil, fmt.Errorf("read external artifact %s: %w", filename, err)
	}
	return data, nil
}

func (r *FSFeatureRepository) validateArtifactRoot(root string) error {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("artifact root must be an absolute path")
	}
	canonical, err := canonicalExisting(root)
	if err != nil {
		return fmt.Errorf("canonicalize artifact root: %w", err)
	}
	if filepath.Clean(canonical) != filepath.Clean(root) {
		return fmt.Errorf("artifact root must not resolve through a link")
	}
	if withinPath(r.root, canonical) || filepath.Clean(r.root) == filepath.Clean(canonical) {
		return fmt.Errorf("artifact root must be outside Git workspace")
	}
	return nil
}

type recoveryWrite struct {
	Path          string `json:"path"`
	BeforeHash    string `json:"before_hash,omitempty"`
	BeforeMissing bool   `json:"before_missing"`
	Content       []byte `json:"content"`
}

type recoveryManifest struct {
	Version   int             `json:"version"`
	FeatureID string          `json:"feature_id"`
	Operation string          `json:"operation"`
	Writes    []recoveryWrite `json:"writes"`
}

func (r *FSFeatureRepository) applyMutation(target FeatureTarget, operation string, writes map[string][]byte) error {
	if len(writes) == 0 {
		return nil
	}
	manifest := recoveryManifest{Version: 1, FeatureID: target.ID, Operation: operation}
	normalizedWrites := make(map[string][]byte, len(writes))
	paths := make([]string, 0, len(writes))
	for source, content := range writes {
		relative := source
		relative = filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
		if err := validateManagedRelativePath(relative); err != nil {
			return err
		}
		if _, duplicate := normalizedWrites[relative]; duplicate {
			return fmt.Errorf("%w: duplicate mutation path %q", ErrInvalidDomainValue, relative)
		}
		normalizedWrites[relative] = content
		paths = append(paths, relative)
	}
	sort.Slice(paths, func(i, j int) bool {
		return mutationPathOrder(paths[i]) < mutationPathOrder(paths[j]) ||
			mutationPathOrder(paths[i]) == mutationPathOrder(paths[j]) && paths[i] < paths[j]
	})
	for _, relative := range paths {
		absolute := filepath.Join(target.Directory, filepath.FromSlash(relative))
		actual, missing, err := managedFileHash(absolute)
		if err != nil {
			return fmt.Errorf("inspect mutation target %s: %w", relative, err)
		}
		manifest.Writes = append(manifest.Writes, recoveryWrite{
			Path: relative, BeforeHash: actual, BeforeMissing: missing,
			Content: append([]byte(nil), normalizedWrites[relative]...),
		})
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestBytes = append(manifestBytes, '\n')
	manifestPath := filepath.Join(target.Directory, recoveryManifestName)
	if err := r.faults.Before("manifest", recoveryManifestName); err != nil {
		return err
	}
	if err := atomicWriteFile(manifestPath, manifestBytes); err != nil {
		return fmt.Errorf("write recovery manifest: %w", err)
	}
	for _, write := range manifest.Writes {
		if err := r.faults.Before("write", write.Path); err != nil {
			return err
		}
		absolute := filepath.Join(target.Directory, filepath.FromSlash(write.Path))
		if err := atomicWriteFile(absolute, write.Content); err != nil {
			return fmt.Errorf("write %s: %w", write.Path, err)
		}
	}
	if err := r.faults.Before("complete", recoveryManifestName); err != nil {
		return err
	}
	if err := os.Remove(manifestPath); err != nil {
		return fmt.Errorf("remove recovery manifest: %w", err)
	}
	return nil
}

func mutationPathOrder(relative string) int {
	switch {
	case relative == "intent.md", relative == "spec.md", relative == "plan.md", strings.HasPrefix(relative, "reviews/"):
		return 0
	case relative == "mem-log.md":
		return 1
	case relative == "state.json":
		return 2
	default:
		return 3
	}
}

func validateManagedRelativePath(relative string) error {
	if relative == "state.json" || relative == "mem-log.md" || relative == "intent.md" || relative == "spec.md" || relative == "plan.md" {
		return nil
	}
	if strings.HasPrefix(relative, "reviews/") && filepath.Ext(relative) == ".md" && !strings.Contains(strings.TrimPrefix(relative, "reviews/"), "/") {
		return nil
	}
	return fmt.Errorf("%w: unmanaged mutation path %q", ErrInvalidDomainValue, relative)
}

func (r *FSFeatureRepository) Recover(featureID string) (RecoveryResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	target, err := FeatureTargetForID(r.root, featureID)
	if err != nil {
		return RecoveryResult{}, err
	}
	fileRecovery, err := r.recoverLocked(target)
	if err != nil || len(fileRecovery.Diagnostics) > 0 {
		return fileRecovery, err
	}
	phaseRecovery, err := r.recoverPhaseLocked(featureID)
	if err != nil {
		return RecoveryResult{}, err
	}
	return RecoveryResult{
		Completed:   fileRecovery.Completed || phaseRecovery.Completed,
		Diagnostics: append(fileRecovery.Diagnostics, phaseRecovery.Diagnostics...),
	}, nil
}

func (r *FSFeatureRepository) recoverLocked(target FeatureTarget) (RecoveryResult, error) {
	manifestPath := filepath.Join(target.Directory, recoveryManifestName)
	data, err := readOptionalRegularFile(manifestPath)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("inspect recovery manifest: %w", err)
	}
	if data == nil {
		return RecoveryResult{}, nil
	}
	var manifest recoveryManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return RecoveryResult{Diagnostics: []RecoveryDiagnostic{{Path: recoveryManifestName, Message: "recovery manifest is invalid: " + err.Error()}}}, nil
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return RecoveryResult{Diagnostics: []RecoveryDiagnostic{{Path: recoveryManifestName, Message: "recovery manifest has trailing JSON"}}}, nil
	}
	if manifest.Version != 1 || manifest.FeatureID != target.ID || len(manifest.Writes) == 0 {
		return RecoveryResult{Diagnostics: []RecoveryDiagnostic{{Path: recoveryManifestName, Message: "recovery manifest identity is inconsistent"}}}, nil
	}
	diagnostics := make([]RecoveryDiagnostic, 0)
	seen := make(map[string]struct{}, len(manifest.Writes))
	for _, write := range manifest.Writes {
		if err := validateManagedRelativePath(write.Path); err != nil {
			diagnostics = append(diagnostics, RecoveryDiagnostic{Path: write.Path, Message: err.Error()})
			continue
		}
		if _, duplicate := seen[write.Path]; duplicate {
			diagnostics = append(diagnostics, RecoveryDiagnostic{Path: write.Path, Message: "recovery manifest contains duplicate target"})
			continue
		}
		seen[write.Path] = struct{}{}
		actual, missing, hashErr := managedFileHash(filepath.Join(target.Directory, filepath.FromSlash(write.Path)))
		if hashErr != nil {
			diagnostics = append(diagnostics, RecoveryDiagnostic{Path: write.Path, Message: hashErr.Error()})
			continue
		}
		after := hash(write.Content)
		if !missing && actual == after {
			continue
		}
		if write.BeforeMissing && missing {
			continue
		}
		if !write.BeforeMissing && !missing && actual == write.BeforeHash {
			continue
		}
		diagnostics = append(diagnostics, RecoveryDiagnostic{
			Path:    write.Path,
			Message: fmt.Sprintf("cannot recover %s: current bytes match neither pre-operation nor intended content", write.Path),
		})
	}
	if len(diagnostics) > 0 {
		return RecoveryResult{Diagnostics: diagnostics}, nil
	}
	for _, write := range manifest.Writes {
		actual, missing, _ := managedFileHash(filepath.Join(target.Directory, filepath.FromSlash(write.Path)))
		if !missing && actual == hash(write.Content) {
			continue
		}
		if err := r.faults.Before("recover", write.Path); err != nil {
			return RecoveryResult{}, err
		}
		if err := atomicWriteFile(filepath.Join(target.Directory, filepath.FromSlash(write.Path)), write.Content); err != nil {
			return RecoveryResult{}, fmt.Errorf("recover %s: %w", write.Path, err)
		}
	}
	if err := os.Remove(manifestPath); err != nil {
		return RecoveryResult{}, fmt.Errorf("complete recovery: %w", err)
	}
	delete(r.baselines, target.ID)
	return RecoveryResult{Completed: true}, nil
}

func atomicWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".stepan-write-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a regular non-link file", path)
	}
	return os.ReadFile(path)
}

func readOptionalRegularFile(path string) ([]byte, error) {
	data, err := readRegularFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}
