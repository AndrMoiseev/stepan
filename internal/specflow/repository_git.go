package specflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const phaseRecoveryManifestName = ".stepan-phase-recovery.json"

type phaseRecoveryFile struct {
	FeatureID     string `json:"feature_id"`
	Path          string `json:"path"`
	BeforeMissing bool   `json:"before_missing"`
	BeforeContent []byte `json:"before_content,omitempty"`
	Desired       []byte `json:"desired"`
}

type phaseRecoveryManifest struct {
	Version      int                 `json:"version"`
	Operation    string              `json:"operation"`
	Coordinator  string              `json:"coordinator"`
	Participants []string            `json:"participants"`
	Stage        Stage               `json:"stage"`
	Message      string              `json:"message"`
	Files        []phaseRecoveryFile `json:"files"`
	IndexPaths   []string            `json:"index_paths"`
}

func (r *FSFeatureRepository) Approve(request ApproveStageRequest) (PhaseCommitResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateApprovalRequest(request); err != nil {
		return PhaseCommitResult{}, err
	}
	if request.At.IsZero() {
		request.At = r.now()
	}
	feature, err := r.loadLocked(request.FeatureID)
	if err != nil {
		return PhaseCommitResult{}, err
	}
	blocking := r.approvalBlockers(feature, request.Stage)
	if len(blocking) > 0 {
		return PhaseCommitResult{Feature: feature, Blocking: blocking}, nil
	}

	snapshot := feature.State.Snapshot()
	stageState := snapshot.Stages[request.Stage]
	stageState.Status = StageCommitted
	stageState.ApprovedHash = stageState.CurrentHash
	stageState.Outdated = false
	snapshot.Stages[request.Stage] = stageState
	committedState, err := NewFlowStateFromSnapshot(snapshot)
	if err != nil {
		return PhaseCommitResult{}, err
	}
	message := approvalCommitMessage(request.FeatureID, request.Stage)
	writes, err := approvalWrites(feature, committedState, request.Stage, request.At, message)
	if err != nil {
		return PhaseCommitResult{}, err
	}

	transaction, err := r.beginPhaseTransaction("approve", request.FeatureID, []string{request.FeatureID}, request.Stage, message, map[string]map[string][]byte{request.FeatureID: writes})
	if err != nil {
		return PhaseCommitResult{}, err
	}
	if err := r.applyMutation(feature.Target, "approve-"+string(request.Stage), writes); err != nil {
		return PhaseCommitResult{}, err
	}
	return r.finishSingleFeatureCommit(transaction, feature, request.At)
}

func validateApprovalRequest(request ApproveStageRequest) error {
	if strings.TrimSpace(request.FeatureID) == "" || !request.Stage.Valid() {
		return fmt.Errorf("%w: invalid approval request", ErrInvalidDomainValue)
	}
	return nil
}

func approvalCommitMessage(featureID string, stage Stage) string {
	return fmt.Sprintf("feature(%s): approve %s", featureID, stage)
}

func (r *FSFeatureRepository) approvalBlockers(feature FeatureSnapshot, stage Stage) []ApprovalBlocker {
	blocking := make([]ApprovalBlocker, 0)
	if feature.State.Status() != FlowActive {
		blocking = append(blocking, ApprovalBlocker{Code: "flow-status", Path: "state.json", Message: "superseded feature cannot be approved"})
	}
	if feature.State.CurrentStage() != stage {
		blocking = append(blocking, ApprovalBlocker{Code: "current-stage", Path: "state.json", Message: fmt.Sprintf("current stage is %s, not %s", feature.State.CurrentStage(), stage)})
	}
	stageState, _ := feature.State.Stage(stage)
	if stageState.Status != StagePublished {
		blocking = append(blocking, ApprovalBlocker{Code: "stage-status", Path: "state.json", Message: fmt.Sprintf("%s must be published before approval", stage)})
	}
	for _, upstream := range stageState.UpstreamHashes {
		upstreamState, ok := feature.State.Stage(upstream.Stage)
		if !ok || upstreamState.Status != StageCommitted || upstreamState.ApprovedHash != upstream.Hash {
			blocking = append(blocking, ApprovalBlocker{
				Code: "upstream-revision", Path: "state.json",
				Message: fmt.Sprintf("%s upstream hash is not the approved %s revision", stage, upstream.Stage),
			})
		}
	}
	document, exists := feature.Documents[stage]
	if !exists || document.Hash != stageState.CurrentHash {
		path, _ := feature.Target.DisplayDocumentPath(stage)
		blocking = append(blocking, ApprovalBlocker{Code: "document-revision", Path: path, Message: fmt.Sprintf("%s has no authoritative published revision", stage)})
	} else {
		result := r.validateDocumentForApproval(feature, stage, document.Content)
		for _, diagnostic := range result.Diagnostics {
			blocking = append(blocking, ApprovalBlocker{
				Code: string(diagnostic.Code), Path: document.Path,
				Message: fmt.Sprintf("line %d, %s: %s", diagnostic.Line, diagnostic.Subject, diagnostic.Message),
			})
		}
	}
	blocking = append(blocking, reviewApprovalBlockers(feature, stage, stageState)...)
	paths, err := r.changedGitPaths()
	if err != nil {
		blocking = append(blocking, ApprovalBlocker{Code: "git-status", Message: err.Error()})
	} else {
		for _, path := range paths {
			if !pathOwnedByFeature(path, feature.Target.ID) {
				blocking = append(blocking, ApprovalBlocker{Code: "outside-feature-change", Path: path, Message: "path outside the current feature is dirty"})
			}
		}
	}
	sortApprovalBlockers(blocking)
	return blocking
}

func (r *FSFeatureRepository) validateDocumentForApproval(feature FeatureSnapshot, stage Stage, content []byte) DocumentResult {
	active := make([]StableID, 0)
	if stage == StagePlan {
		if specification, ok := feature.Documents[StageSpec]; ok {
			parsed := ParseDocument(DocumentRequest{Kind: DocumentSpec, Mode: ValidateDraft, Markdown: string(specification.Content)})
			for _, element := range parsed.Document.Elements {
				active = append(active, element.ID)
			}
		}
	}
	initial := ParseDocument(DocumentRequest{Kind: documentKindForStage(stage), Mode: ValidateDraft, Markdown: string(content), ActiveIDs: active})
	retained := observedStableIDs(initial.ObservedIDs)
	return ParseDocument(DocumentRequest{
		Kind: documentKindForStage(stage), Mode: ValidateApproval, Markdown: string(content),
		ActiveIDs: active, IssuedIDs: feature.State.IssuedIDs(), RetainedIDs: retained,
	})
}

func reviewApprovalBlockers(feature FeatureSnapshot, stage Stage, state StageState) []ApprovalBlocker {
	if stage == StageIntent || state.ReviewStatus == ReviewNotStarted {
		return nil
	}
	if state.ReviewStatus != ReviewCompleted || len(state.Reviews) == 0 {
		return []ApprovalBlocker{{Code: "review-incomplete", Path: "state.json", Message: fmt.Sprintf("%s review must complete before approval", stage)}}
	}
	latest := state.Reviews[len(state.Reviews)-1]
	if latest.Status != ReviewCompleted || latest.AcceptedFingerprint == nil {
		return []ApprovalBlocker{{Code: "review-incomplete", Path: latest.Path, Message: "latest review is not accepted for a document revision"}}
	}
	expected, err := NewFingerprint(state.CurrentHash, state.UpstreamHashes)
	if err != nil || !latest.AcceptedFingerprint.Equal(expected) {
		return []ApprovalBlocker{{Code: "review-revision", Path: latest.Path, Message: "latest review does not apply to the current document and upstream revisions"}}
	}
	for _, artifact := range feature.Reviews {
		if artifact.Stage != stage || artifact.RunID != latest.ID {
			continue
		}
		result := ParseDocument(DocumentRequest{Kind: reviewKindForStage(stage), Mode: ValidateApproval, Markdown: string(artifact.Content)})
		for _, diagnostic := range result.Diagnostics {
			return []ApprovalBlocker{{Code: string(diagnostic.Code), Path: artifact.Path, Message: diagnostic.Message}}
		}
		for _, finding := range result.Document.Findings {
			if finding.Finding.Snapshot().Status == FindingOpen {
				return []ApprovalBlocker{{Code: "open-finding", Path: artifact.Path, Message: fmt.Sprintf("finding %s remains open", finding.Finding.Snapshot().ID)}}
			}
		}
		return nil
	}
	return []ApprovalBlocker{{Code: "review-missing", Path: latest.Path, Message: "latest review report is missing"}}
}

func sortApprovalBlockers(values []ApprovalBlocker) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Path != values[j].Path {
			return values[i].Path < values[j].Path
		}
		return values[i].Code < values[j].Code
	})
}

func approvalWrites(feature FeatureSnapshot, state FlowState, stage Stage, at time.Time, message string) (map[string][]byte, error) {
	stateBytes, err := encodeState(state)
	if err != nil {
		return nil, err
	}
	approval, err := NewMemLogEntry(stage, mustAuthorRole(stage), MemLogApproval, at, fmt.Sprintf("%s approved: hash=%s", stage, feature.Documents[stage].Hash))
	if err != nil {
		return nil, err
	}
	commit, err := NewMemLogEntry(stage, mustAuthorRole(stage), MemLogCommit, at, "phase commit: "+message)
	if err != nil {
		return nil, err
	}
	journal, err := appendFeatureJournalEntries(feature, []MemLogEntry{approval, commit}, hash(stateBytes))
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"state.json": stateBytes, "mem-log.md": journal}, nil
}

func appendFeatureJournalEntries(feature FeatureSnapshot, entries []MemLogEntry, stateHash string) ([]byte, error) {
	data, err := readRegularFile(feature.Target.JournalPath)
	if err != nil {
		return nil, err
	}
	if len(feature.Journal) == 0 {
		return nil, fmt.Errorf("mem-log has no entries")
	}
	previous := feature.Journal[len(feature.Journal)-1].Checksum
	sequence := uint64(len(feature.Journal))
	for _, entry := range entries {
		entry.Sequence = sequence
		entry.StateHash = stateHash
		data, entry, err = appendMemLogBytes(data, entry, previous)
		if err != nil {
			return nil, err
		}
		sequence = entry.Sequence
		previous = entry.Checksum
	}
	return data, nil
}

func (r *FSFeatureRepository) ReviseIntent(request ReviseIntentRequest) (PhaseCommitResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.TrimSpace(request.FeatureID) == "" {
		return PhaseCommitResult{}, fmt.Errorf("%w: feature ID is required", ErrInvalidDomainValue)
	}
	if request.At.IsZero() {
		request.At = r.now()
	}
	feature, err := r.loadLocked(request.FeatureID)
	if err != nil {
		return PhaseCommitResult{}, err
	}
	blocking := r.intentRevisionBlockers(feature)
	if len(blocking) > 0 {
		return PhaseCommitResult{Feature: feature, Blocking: blocking}, nil
	}
	changed, err := readRegularFile(feature.Target.IntentPath)
	if err != nil {
		return PhaseCommitResult{}, err
	}
	snapshot := feature.State.Snapshot()
	intent := snapshot.Stages[StageIntent]
	intent.CurrentHash = hash(changed)
	intent.ApprovedHash = intent.CurrentHash
	intent.Status = StageCommitted
	snapshot.Stages[StageIntent] = intent
	revisedState, err := NewFlowStateFromSnapshot(snapshot)
	if err != nil {
		return PhaseCommitResult{}, err
	}
	stateBytes, err := encodeState(revisedState)
	if err != nil {
		return PhaseCommitResult{}, err
	}
	entry, err := NewMemLogEntry(StageIntent, RoleIntentAuthor, MemLogCommit, request.At, fmt.Sprintf("intent revision committed: old=%s new=%s", feature.State.Snapshot().Stages[StageIntent].ApprovedHash, intent.ApprovedHash))
	if err != nil {
		return PhaseCommitResult{}, err
	}
	journal, err := appendFeatureJournalEntries(feature, []MemLogEntry{entry}, hash(stateBytes))
	if err != nil {
		return PhaseCommitResult{}, err
	}
	writes := map[string][]byte{"state.json": stateBytes, "mem-log.md": journal}
	message := fmt.Sprintf("feature(%s): revise intent", request.FeatureID)
	transaction, err := r.beginPhaseTransaction("revise-intent", request.FeatureID, []string{request.FeatureID}, StageIntent, message, map[string]map[string][]byte{request.FeatureID: writes})
	if err != nil {
		return PhaseCommitResult{}, err
	}
	if err := r.applyMutation(feature.Target, "revise-intent", writes); err != nil {
		return PhaseCommitResult{}, err
	}
	return r.finishSingleFeatureCommit(transaction, feature, request.At)
}

func (r *FSFeatureRepository) intentRevisionBlockers(feature FeatureSnapshot) []ApprovalBlocker {
	blocking := make([]ApprovalBlocker, 0)
	intentState, _ := feature.State.Stage(StageIntent)
	if feature.State.Status() != FlowActive || intentState.Status != StageCommitted || intentState.ApprovedHash == "" {
		blocking = append(blocking, ApprovalBlocker{Code: "intent-status", Path: "state.json", Message: "intent must be committed before revision"})
	}
	if len(feature.Changes.Blocking) > 0 {
		for _, change := range feature.Changes.Blocking {
			blocking = append(blocking, ApprovalBlocker{Code: "protected-artifact", Path: change.Path, Message: change.Message})
		}
	}
	if len(feature.Changes.DocumentRevisions) != 1 || feature.Changes.DocumentRevisions[0].Stage != StageIntent {
		blocking = append(blocking, ApprovalBlocker{Code: "intent-revision", Path: feature.Target.DisplayIntentPath, Message: "exactly one manual intent revision is required"})
	} else if data, err := readRegularFile(feature.Target.IntentPath); err != nil {
		blocking = append(blocking, ApprovalBlocker{Code: "intent-revision", Path: feature.Target.DisplayIntentPath, Message: err.Error()})
	} else {
		result := r.validateDocumentForApproval(feature, StageIntent, data)
		for _, diagnostic := range result.Diagnostics {
			blocking = append(blocking, ApprovalBlocker{Code: string(diagnostic.Code), Path: feature.Target.DisplayIntentPath, Message: diagnostic.Message})
		}
	}
	paths, err := r.changedGitPaths()
	if err != nil {
		blocking = append(blocking, ApprovalBlocker{Code: "git-status", Message: err.Error()})
	} else {
		for _, path := range paths {
			if !pathOwnedByFeature(path, feature.Target.ID) {
				blocking = append(blocking, ApprovalBlocker{Code: "outside-feature-change", Path: path, Message: "path outside the current feature is dirty"})
			}
		}
	}
	sortApprovalBlockers(blocking)
	return blocking
}

func (r *FSFeatureRepository) SupersedeIntent(request SupersedeIntentRequest) (SupersessionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ValidateFeatureID(request.OldFeatureID); err != nil {
		return SupersessionResult{}, err
	}
	if err := ValidateFeatureID(request.NewFeatureID); err != nil {
		return SupersessionResult{}, err
	}
	if request.OldFeatureID == request.NewFeatureID {
		return SupersessionResult{}, fmt.Errorf("%w: supersession features must differ", ErrInvalidDomainValue)
	}
	if request.At.IsZero() {
		request.At = r.now()
	}
	old, err := r.loadLocked(request.OldFeatureID)
	if err != nil {
		return SupersessionResult{}, err
	}
	blocking := r.intentRevisionBlockers(old)
	if len(blocking) > 0 {
		return SupersessionResult{Old: old, Blocking: blocking}, nil
	}
	newTarget, err := FeatureTargetForID(r.root, request.NewFeatureID)
	if err != nil {
		return SupersessionResult{}, err
	}
	if _, err := os.Lstat(newTarget.Directory); err == nil {
		return SupersessionResult{}, fmt.Errorf("feature %s already exists", request.NewFeatureID)
	} else if !os.IsNotExist(err) {
		return SupersessionResult{}, err
	}
	changed, err := readRegularFile(old.Target.IntentPath)
	if err != nil {
		return SupersessionResult{}, err
	}
	original, err := r.gitFileAtHEAD(filepath.ToSlash(filepath.Join(featuresDirectoryPath, request.OldFeatureID, "intent.md")))
	if err != nil {
		return SupersessionResult{}, fmt.Errorf("read approved intent from HEAD: %w", err)
	}
	oldIntent, _ := old.State.Stage(StageIntent)
	if hash(original) != oldIntent.ApprovedHash {
		return SupersessionResult{}, fmt.Errorf("%w: HEAD intent does not match approved hash", ErrRepositoryBlocked)
	}

	oldSnapshot := old.State.Snapshot()
	oldSnapshot.FlowStatus = FlowSuperseded
	oldSnapshot.Supersession.SupersededBy = request.NewFeatureID
	oldState, err := NewFlowStateFromSnapshot(oldSnapshot)
	if err != nil {
		return SupersessionResult{}, err
	}
	newSnapshot := NewFlowState().Snapshot()
	newSnapshot.Supersession.Supersedes = request.OldFeatureID
	newIntent := newSnapshot.Stages[StageIntent]
	newIntent.Status = StagePublished
	newIntent.CurrentHash = hash(changed)
	newSnapshot.Stages[StageIntent] = newIntent
	newState, err := NewFlowStateFromSnapshot(newSnapshot)
	if err != nil {
		return SupersessionResult{}, err
	}
	oldStateBytes, _ := encodeState(oldState)
	newStateBytes, _ := encodeState(newState)
	oldEntry, _ := NewMemLogEntry(StageIntent, RoleIntentAuthor, MemLogCommit, request.At, fmt.Sprintf("feature superseded by %s", request.NewFeatureID))
	oldJournal, err := appendFeatureJournalEntries(old, []MemLogEntry{oldEntry}, hash(oldStateBytes))
	if err != nil {
		return SupersessionResult{}, err
	}
	brief := old.Journal[0].Body
	newJournal, newEntries, err := newMemLogBoundToState(request.NewFeatureID, brief, request.At, hash(newStateBytes))
	if err != nil {
		return SupersessionResult{}, err
	}
	newEntry, _ := NewMemLogEntry(StageIntent, RoleIntentAuthor, MemLogRevisionDecision, request.At, fmt.Sprintf("created from material intent revision of %s; approval required", request.OldFeatureID))
	newEntry.Sequence = uint64(len(newEntries))
	newEntry.StateHash = hash(newStateBytes)
	newJournal, _, err = appendMemLogBytes(newJournal, newEntry, newEntries[len(newEntries)-1].Checksum)
	if err != nil {
		return SupersessionResult{}, err
	}
	oldWrites := map[string][]byte{"intent.md": original, "state.json": oldStateBytes, "mem-log.md": oldJournal}
	newWrites := map[string][]byte{"intent.md": changed, "state.json": newStateBytes, "mem-log.md": newJournal}
	message := fmt.Sprintf("feature(%s): supersede with %s", request.OldFeatureID, request.NewFeatureID)
	transaction, err := r.beginPhaseTransaction("supersede-intent", request.OldFeatureID, []string{request.OldFeatureID, request.NewFeatureID}, StageIntent, message, map[string]map[string][]byte{
		request.OldFeatureID: oldWrites, request.NewFeatureID: newWrites,
	})
	if err != nil {
		return SupersessionResult{}, err
	}
	if err := os.MkdirAll(newTarget.Directory, 0o755); err != nil {
		return SupersessionResult{}, err
	}
	if err := r.applyMutation(old.Target, "supersede-old", oldWrites); err != nil {
		return SupersessionResult{}, err
	}
	if err := r.applyMutation(newTarget, "supersede-new", newWrites); err != nil {
		return SupersessionResult{}, err
	}
	if err := r.preparePhaseIndex(&transaction); err != nil {
		return r.rollbackSupersession(transaction, old, request.At, err)
	}
	if err := r.persistPhaseManifest(transaction); err != nil {
		return SupersessionResult{}, err
	}
	if err := r.faults.Before("phase-commit", request.OldFeatureID); err != nil {
		return SupersessionResult{}, err
	}
	if err := r.commitPhase(transaction); err != nil {
		return r.rollbackSupersession(transaction, old, request.At, err)
	}
	if err := r.faults.Before("phase-complete", request.OldFeatureID); err != nil {
		return SupersessionResult{}, err
	}
	if err := r.removePhaseManifest(transaction); err != nil {
		return SupersessionResult{}, err
	}
	delete(r.baselines, request.OldFeatureID)
	delete(r.baselines, request.NewFeatureID)
	loadedOld, err := r.loadLocked(request.OldFeatureID)
	if err != nil {
		return SupersessionResult{}, err
	}
	loadedNew, err := r.loadLocked(request.NewFeatureID)
	if err != nil {
		return SupersessionResult{}, err
	}
	return SupersessionResult{Committed: true, Old: loadedOld, New: loadedNew}, nil
}

func (r *FSFeatureRepository) finishSingleFeatureCommit(transaction phaseRecoveryManifest, before FeatureSnapshot, at time.Time) (PhaseCommitResult, error) {
	if err := r.preparePhaseIndex(&transaction); err != nil {
		return r.rollbackSingleFeature(transaction, before, at, err)
	}
	if err := r.persistPhaseManifest(transaction); err != nil {
		return PhaseCommitResult{}, err
	}
	if err := r.faults.Before("phase-commit", before.Target.ID); err != nil {
		return PhaseCommitResult{}, err
	}
	if err := r.commitPhase(transaction); err != nil {
		return r.rollbackSingleFeature(transaction, before, at, err)
	}
	if err := r.faults.Before("phase-complete", before.Target.ID); err != nil {
		return PhaseCommitResult{}, err
	}
	if err := r.removePhaseManifest(transaction); err != nil {
		return PhaseCommitResult{}, err
	}
	delete(r.baselines, before.Target.ID)
	loaded, err := r.loadLocked(before.Target.ID)
	if err != nil {
		return PhaseCommitResult{}, err
	}
	return PhaseCommitResult{Committed: true, Feature: loaded}, nil
}

func (r *FSFeatureRepository) rollbackSingleFeature(transaction phaseRecoveryManifest, before FeatureSnapshot, at time.Time, commitErr error) (PhaseCommitResult, error) {
	cleanupErr := r.rollbackPhase(transaction)
	delete(r.baselines, before.Target.ID)
	loaded, loadErr := r.loadLocked(before.Target.ID)
	if loadErr == nil {
		entry, _ := NewMemLogEntry(transaction.Stage, mustAuthorRole(transaction.Stage), MemLogError, at, "phase commit failed: "+commitErr.Error())
		loaded, loadErr = r.recordActivityLocked(loaded, entry)
	}
	if cleanupErr != nil {
		commitErr = errors.Join(commitErr, cleanupErr)
	}
	if loadErr != nil {
		commitErr = errors.Join(commitErr, loadErr)
	}
	return PhaseCommitResult{Feature: loaded}, fmt.Errorf("%w: %v", ErrPhaseCommit, commitErr)
}

func (r *FSFeatureRepository) rollbackSupersession(transaction phaseRecoveryManifest, old FeatureSnapshot, at time.Time, commitErr error) (SupersessionResult, error) {
	cleanupErr := r.rollbackPhase(transaction)
	delete(r.baselines, old.Target.ID)
	loaded, loadErr := r.loadLocked(old.Target.ID)
	if loadErr == nil {
		entry, _ := NewMemLogEntry(StageIntent, RoleIntentAuthor, MemLogError, at, "supersession commit failed: "+commitErr.Error())
		loaded, loadErr = r.recordActivityLocked(loaded, entry)
	}
	if cleanupErr != nil {
		commitErr = errors.Join(commitErr, cleanupErr)
	}
	if loadErr != nil {
		commitErr = errors.Join(commitErr, loadErr)
	}
	return SupersessionResult{Old: loaded}, fmt.Errorf("%w: %v", ErrPhaseCommit, commitErr)
}

func (r *FSFeatureRepository) beginPhaseTransaction(operation, coordinator string, participants []string, stage Stage, message string, overlays map[string]map[string][]byte) (phaseRecoveryManifest, error) {
	manifest := phaseRecoveryManifest{Version: 1, Operation: operation, Coordinator: coordinator, Participants: append([]string(nil), participants...), Stage: stage, Message: message}
	for _, featureID := range participants {
		target, err := FeatureTargetForID(r.root, featureID)
		if err != nil {
			return phaseRecoveryManifest{}, err
		}
		desired := make(map[string][]byte)
		if info, statErr := os.Lstat(target.Directory); statErr == nil && info.IsDir() {
			files, err := managedFeatureFiles(target)
			if err != nil {
				return phaseRecoveryManifest{}, err
			}
			for relative, content := range files {
				desired[relative] = content
			}
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return phaseRecoveryManifest{}, statErr
		}
		for relative, content := range overlays[featureID] {
			desired[filepath.ToSlash(relative)] = append([]byte(nil), content...)
		}
		paths := make([]string, 0, len(desired))
		for relative := range desired {
			paths = append(paths, relative)
		}
		sort.Strings(paths)
		for _, relative := range paths {
			if err := validateManagedRelativePath(relative); err != nil {
				return phaseRecoveryManifest{}, err
			}
			before, err := readOptionalRegularFile(filepath.Join(target.Directory, filepath.FromSlash(relative)))
			if err != nil {
				return phaseRecoveryManifest{}, err
			}
			manifest.Files = append(manifest.Files, phaseRecoveryFile{
				FeatureID: featureID, Path: relative, BeforeMissing: before == nil,
				BeforeContent: append([]byte(nil), before...), Desired: append([]byte(nil), desired[relative]...),
			})
		}
	}
	if err := r.persistPhaseManifest(manifest); err != nil {
		return phaseRecoveryManifest{}, err
	}
	return manifest, nil
}

func managedFeatureFiles(target FeatureTarget) (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := filepath.WalkDir(target.Directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(target.Directory, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == recoveryManifestName || relative == phaseRecoveryManifestName {
			return nil
		}
		if err := validateManagedRelativePath(relative); err != nil {
			return err
		}
		data, err := readRegularFile(path)
		if err != nil {
			return err
		}
		result[relative] = data
		return nil
	})
	return result, err
}

func (r *FSFeatureRepository) persistPhaseManifest(manifest phaseRecoveryManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	target, err := FeatureTargetForID(r.root, manifest.Coordinator)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target.Directory, 0o755); err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(target.Directory, phaseRecoveryManifestName), data)
}

func (r *FSFeatureRepository) preparePhaseIndex(manifest *phaseRecoveryManifest) error {
	paths := manifestRepoPaths(*manifest)
	newPaths := make([]string, 0)
	for _, path := range paths {
		if _, err := r.gitOutput("ls-files", "--error-unmatch", "--", path); err != nil {
			newPaths = append(newPaths, path)
		}
	}
	manifest.IndexPaths = newPaths
	// Persist the complete cleanup set before touching the index. A process
	// crash during Git's intent-to-add operation can then remove only Stepan's
	// managed entries and preserve every unrelated staged entry.
	if err := r.persistPhaseManifest(*manifest); err != nil {
		return err
	}
	if len(newPaths) > 0 {
		args := append([]string{"add", "--intent-to-add", "--"}, newPaths...)
		if _, err := r.gitOutput(args...); err != nil {
			return fmt.Errorf("prepare new feature paths: %w", err)
		}
	}
	return nil
}

func (r *FSFeatureRepository) commitPhase(manifest phaseRecoveryManifest) error {
	paths := manifestRepoPaths(manifest)
	args := []string{"commit", "--only", "-m", manifest.Message, "--"}
	args = append(args, paths...)
	if _, err := r.gitOutput(args...); err != nil {
		return err
	}
	return nil
}

func manifestRepoPaths(manifest phaseRecoveryManifest) []string {
	paths := make([]string, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		paths = append(paths, filepath.ToSlash(filepath.Join(featuresDirectoryPath, file.FeatureID, file.Path)))
	}
	sort.Strings(paths)
	return paths
}

func (r *FSFeatureRepository) rollbackPhase(manifest phaseRecoveryManifest) error {
	var result error
	if err := r.cleanPhaseIndex(manifest); err != nil {
		result = errors.Join(result, fmt.Errorf("clean transient index entries: %w", err))
	}
	for _, file := range manifest.Files {
		target, err := FeatureTargetForID(r.root, file.FeatureID)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		absolute := filepath.Join(target.Directory, filepath.FromSlash(file.Path))
		if file.BeforeMissing {
			if err := os.Remove(absolute); err != nil && !os.IsNotExist(err) {
				result = errors.Join(result, err)
			}
			continue
		}
		if err := atomicWriteFile(absolute, file.BeforeContent); err != nil {
			result = errors.Join(result, err)
		}
	}
	if err := r.removePhaseManifest(manifest); err != nil {
		result = errors.Join(result, err)
	}
	for i := len(manifest.Participants) - 1; i >= 0; i-- {
		featureID := manifest.Participants[i]
		if featureID == manifest.Coordinator || !participantWasMissing(manifest, featureID) {
			continue
		}
		target, _ := FeatureTargetForID(r.root, featureID)
		_ = os.Remove(target.ReviewsDirectory)
		_ = os.Remove(target.Directory)
	}
	return result
}

func participantWasMissing(manifest phaseRecoveryManifest, featureID string) bool {
	found := false
	for _, file := range manifest.Files {
		if file.FeatureID == featureID {
			found = true
			if !file.BeforeMissing {
				return false
			}
		}
	}
	return found
}

func (r *FSFeatureRepository) removePhaseManifest(manifest phaseRecoveryManifest) error {
	target, err := FeatureTargetForID(r.root, manifest.Coordinator)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(target.Directory, phaseRecoveryManifestName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (r *FSFeatureRepository) changedGitPaths() ([]string, error) {
	output, err := r.gitOutput("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	return parsePorcelainPaths([]byte(output)), nil
}

func parsePorcelainPaths(output []byte) []string {
	records := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(records))
	seen := make(map[string]struct{})
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) < 4 {
			continue
		}
		status := string(record[:2])
		path := filepath.ToSlash(string(record[3:]))
		if _, exists := seen[path]; !exists {
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
		if strings.ContainsAny(status, "RC") && index+1 < len(records) {
			index++
			old := filepath.ToSlash(string(records[index]))
			if _, exists := seen[old]; !exists {
				seen[old] = struct{}{}
				paths = append(paths, old)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

func pathOwnedByFeature(path, featureID string) bool {
	prefix := filepath.ToSlash(filepath.Join(featuresDirectoryPath, featureID))
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func (r *FSFeatureRepository) gitFileAtHEAD(path string) ([]byte, error) {
	output, err := r.gitOutputBytes("show", "HEAD:"+path)
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (r *FSFeatureRepository) gitOutput(args ...string) (string, error) {
	output, err := r.gitOutputBytes(args...)
	return string(output), err
}

func (r *FSFeatureRepository) gitOutputBytes(args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (r *FSFeatureRepository) ensureCleanRepository() error {
	paths, err := r.changedGitPaths()
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	return fmt.Errorf("%w: Git working tree and index must be clean: %s", ErrRepositoryBlocked, strings.Join(paths, ", "))
}

func (r *FSFeatureRepository) recoverPhaseLocked(featureID string) (RecoveryResult, error) {
	manifest, found, err := r.findPhaseManifest(featureID)
	if err != nil || !found {
		return RecoveryResult{}, err
	}
	for _, participant := range manifest.Participants {
		target, targetErr := FeatureTargetForID(r.root, participant)
		if targetErr != nil {
			return RecoveryResult{}, targetErr
		}
		if _, statErr := os.Lstat(target.Directory); os.IsNotExist(statErr) {
			continue
		} else if statErr != nil {
			return RecoveryResult{}, statErr
		}
		fileRecovery, recoveryErr := r.recoverLocked(target)
		if recoveryErr != nil {
			return RecoveryResult{}, recoveryErr
		}
		if len(fileRecovery.Diagnostics) > 0 {
			return fileRecovery, nil
		}
	}
	if diagnostics := r.validatePhaseRecovery(manifest); len(diagnostics) > 0 {
		return RecoveryResult{Diagnostics: diagnostics}, nil
	}
	committed := true
	for _, file := range manifest.Files {
		path := filepath.ToSlash(filepath.Join(featuresDirectoryPath, file.FeatureID, file.Path))
		data, err := r.gitFileAtHEAD(path)
		if err != nil || !bytes.Equal(data, file.Desired) {
			committed = false
			break
		}
	}
	if committed {
		if err := r.cleanPhaseIndex(manifest); err != nil {
			return RecoveryResult{}, err
		}
		if err := r.removePhaseManifest(manifest); err != nil {
			return RecoveryResult{}, err
		}
		return RecoveryResult{Completed: true}, nil
	}
	if err := r.rollbackPhase(manifest); err != nil {
		return RecoveryResult{}, err
	}
	coordinator, err := r.loadLockedWithoutPhase(manifest.Coordinator)
	if err != nil {
		return RecoveryResult{}, err
	}
	entry, _ := NewMemLogEntry(manifest.Stage, mustAuthorRole(manifest.Stage), MemLogRecovery, r.now(), fmt.Sprintf("rolled back interrupted %s before Git commit", manifest.Operation))
	if _, err := r.recordActivityLocked(coordinator, entry); err != nil {
		return RecoveryResult{}, err
	}
	return RecoveryResult{Completed: true}, nil
}

func (r *FSFeatureRepository) findPhaseManifest(featureID string) (phaseRecoveryManifest, bool, error) {
	base := filepath.Join(r.root, filepath.FromSlash(featuresDirectoryPath))
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return phaseRecoveryManifest{}, false, nil
	}
	if err != nil {
		return phaseRecoveryManifest{}, false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(base, entry.Name(), phaseRecoveryManifestName)
		data, err := readOptionalRegularFile(path)
		if err != nil {
			return phaseRecoveryManifest{}, false, err
		}
		if data == nil {
			continue
		}
		manifest, err := decodePhaseManifest(data)
		if err != nil {
			return phaseRecoveryManifest{}, false, fmt.Errorf("%w: invalid phase recovery manifest: %v", ErrRepositoryBlocked, err)
		}
		for _, participant := range manifest.Participants {
			if participant == featureID {
				return manifest, true, nil
			}
		}
	}
	return phaseRecoveryManifest{}, false, nil
}

func (r *FSFeatureRepository) cleanPhaseIndex(manifest phaseRecoveryManifest) error {
	if len(manifest.IndexPaths) == 0 {
		return nil
	}
	args := append([]string{"reset", "-q", "HEAD", "--"}, manifest.IndexPaths...)
	_, err := r.gitOutput(args...)
	return err
}

func decodePhaseManifest(data []byte) (phaseRecoveryManifest, error) {
	var manifest phaseRecoveryManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return phaseRecoveryManifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return phaseRecoveryManifest{}, fmt.Errorf("trailing JSON")
	}
	if manifest.Version != 1 || manifest.Coordinator == "" || len(manifest.Participants) == 0 || !manifest.Stage.Valid() || len(manifest.Files) == 0 {
		return phaseRecoveryManifest{}, fmt.Errorf("invalid identity")
	}
	return manifest, nil
}

func (r *FSFeatureRepository) validatePhaseRecovery(manifest phaseRecoveryManifest) []RecoveryDiagnostic {
	diagnostics := make([]RecoveryDiagnostic, 0)
	for _, file := range manifest.Files {
		if err := ValidateFeatureID(file.FeatureID); err != nil {
			diagnostics = append(diagnostics, RecoveryDiagnostic{Path: file.FeatureID, Message: err.Error()})
			continue
		}
		if err := validateManagedRelativePath(file.Path); err != nil {
			diagnostics = append(diagnostics, RecoveryDiagnostic{Path: file.Path, Message: err.Error()})
			continue
		}
		target, _ := FeatureTargetForID(r.root, file.FeatureID)
		current, err := readOptionalRegularFile(filepath.Join(target.Directory, filepath.FromSlash(file.Path)))
		if err != nil {
			diagnostics = append(diagnostics, RecoveryDiagnostic{Path: file.Path, Message: err.Error()})
			continue
		}
		matchesDesired := bytes.Equal(current, file.Desired)
		matchesBefore := file.BeforeMissing && current == nil || !file.BeforeMissing && bytes.Equal(current, file.BeforeContent)
		if !matchesDesired && !matchesBefore {
			diagnostics = append(diagnostics, RecoveryDiagnostic{Path: file.Path, Message: "current bytes match neither pre-operation nor intended content"})
		}
	}
	return diagnostics
}

// loadLockedWithoutPhase is used only after a phase manifest has been removed
// by recovery. Calling the regular load path there would recursively scan the
// same transaction while its recovery event is being appended.
func (r *FSFeatureRepository) loadLockedWithoutPhase(featureID string) (FeatureSnapshot, error) {
	return r.loadLockedCore(featureID, false)
}
