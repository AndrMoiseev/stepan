package specflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type SessionArtifact struct {
	Stage        Stage
	Role         Role
	ArtifactRoot string
}

type SessionCloseRequest struct {
	FeatureID string
	Artifacts []SessionArtifact
	At        time.Time
}

type SessionRecoveryRequest struct {
	FeatureID string
	At        time.Time
}

type SessionRecoveryResult struct {
	Feature     FeatureSnapshot
	Interrupted bool
	Diagnostics []RecoveryDiagnostic
}

type ResumableFlow struct {
	FeatureID    string
	FlowStatus   FlowStatus
	CurrentStage Stage
	StageStatus  StageStatus
	ReviewStatus ReviewStatus
	Updated      time.Time
}

// SessionLifecycleRepository keeps session exit and recovery as atomic durable
// operations instead of spreading state/journal ordering across callers.
type SessionLifecycleRepository interface {
	FeatureRepository
	CloseFeatureSession(SessionCloseRequest) (FeatureSnapshot, error)
	RecoverFeatureSession(SessionRecoveryRequest) (SessionRecoveryResult, error)
	DiscoverResumable() ([]ResumableFlow, error)
	ActivationBlockers(featureID string) ([]ApprovalBlocker, error)
}

// ResumeManager deliberately separates discovery from activation. Listing a
// flow never creates an agent thread or mutates its journal.
type ResumeManager struct {
	repository SessionLifecycleRepository
	registry   *SessionRegistry
	controller *FeatureController
	now        func() time.Time

	mu       sync.Mutex
	activeID string
	closed   bool
}

func NewResumeManager(repository SessionLifecycleRepository, registry *SessionRegistry, controller *FeatureController) (*ResumeManager, error) {
	if repository == nil || registry == nil || controller == nil {
		return nil, fmt.Errorf("create resume manager: repository, registry, and controller are required")
	}
	return &ResumeManager{repository: repository, registry: registry, controller: controller, now: time.Now}, nil
}

func (m *ResumeManager) Discover() ([]ResumableFlow, error) {
	return m.repository.DiscoverResumable()
}

func (m *ResumeManager) Begin(request CreateFeatureRequest, runtimeContext string) (Progress, error) {
	progress, err := m.controller.Begin(request, runtimeContext)
	if progress.FeatureID != "" {
		m.mu.Lock()
		m.activeID, m.closed = progress.FeatureID, false
		m.mu.Unlock()
	}
	return progress, err
}

func (m *ResumeManager) Activate(featureID, runtimeContext string) (Progress, error) {
	flows, err := m.repository.DiscoverResumable()
	if err != nil {
		return Progress{}, fmt.Errorf("discover resumable flows: %w", err)
	}
	found := false
	for _, flow := range flows {
		if flow.FeatureID == featureID {
			found = true
			break
		}
	}
	if !found {
		return Progress{}, fmt.Errorf("%w: feature %s is not resumable", ErrControllerCommandInvalid, featureID)
	}
	blockers, err := m.repository.ActivationBlockers(featureID)
	if err != nil {
		return Progress{}, fmt.Errorf("check resume activation: %w", err)
	}
	if len(blockers) > 0 {
		return Progress{FeatureID: featureID, Blocking: append([]ApprovalBlocker(nil), blockers...)}, fmt.Errorf("%w: another feature has uncommitted changes", ErrRepositoryBlocked)
	}
	recovery, err := m.repository.RecoverFeatureSession(SessionRecoveryRequest{FeatureID: featureID, At: m.now()})
	if err != nil {
		return Progress{}, fmt.Errorf("recover feature session: %w", err)
	}
	if len(recovery.Diagnostics) > 0 {
		return Progress{FeatureID: featureID, RecoveryDiagnostics: append([]RecoveryDiagnostic(nil), recovery.Diagnostics...)}, fmt.Errorf("%w: feature recovery requires attention", ErrRepositoryBlocked)
	}
	if _, err := m.controller.Open(featureID); err != nil {
		return Progress{}, err
	}
	progress, err := m.controller.StartCurrentStage(runtimeContext)
	progress.RecoveryDiagnostics = append([]RecoveryDiagnostic(nil), recovery.Diagnostics...)
	if err != nil {
		return progress, err
	}
	m.mu.Lock()
	m.activeID, m.closed = featureID, false
	m.mu.Unlock()
	return progress, nil
}

func (m *ResumeManager) Close() (Progress, error) {
	m.mu.Lock()
	if m.closed || m.activeID == "" {
		m.mu.Unlock()
		return Progress{Event: ControllerSessionClosed}, nil
	}
	featureID := m.activeID
	m.closed = true
	m.mu.Unlock()
	progress, controllerErr := m.controller.Close()
	registryErr := m.registry.CloseFeature(featureID)
	return progress, errors.Join(controllerErr, registryErr)
}

func (r *FSFeatureRepository) DiscoverResumable() ([]ResumableFlow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	directory := filepath.Join(r.root, filepath.FromSlash(featuresDirectoryPath))
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return []ResumableFlow{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("discover feature directories: %w", err)
	}
	result := make([]ResumableFlow, 0)
	for _, entry := range entries {
		if !entry.IsDir() || ValidateFeatureID(entry.Name()) != nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(directory, entry.Name(), "state.json")); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		feature, err := r.loadLocked(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("discover feature %s: %w", entry.Name(), err)
		}
		if feature.State.Status() != FlowActive {
			continue
		}
		plan, _ := feature.State.Stage(StagePlan)
		if plan.Status == StageCommitted {
			continue
		}
		stage := feature.State.CurrentStage()
		state, _ := feature.State.Stage(stage)
		updated := time.Time{}
		if len(feature.Journal) > 0 {
			updated = feature.Journal[len(feature.Journal)-1].At
		}
		result = append(result, ResumableFlow{
			FeatureID: feature.Target.ID, FlowStatus: feature.State.Status(), CurrentStage: stage, StageStatus: state.Status,
			ReviewStatus: state.ReviewStatus, Updated: updated,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].Updated.Equal(result[j].Updated) {
			return result[i].Updated.After(result[j].Updated)
		}
		return result[i].FeatureID < result[j].FeatureID
	})
	return result, nil
}

func (r *FSFeatureRepository) ActivationBlockers(featureID string) ([]ApprovalBlocker, error) {
	if err := ValidateFeatureID(featureID); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	paths, err := r.changedGitPaths()
	if err != nil {
		return nil, err
	}
	blockers := make([]ApprovalBlocker, 0)
	active := make(map[string]bool)
	checked := make(map[string]bool)
	for _, changed := range paths {
		if pathOwnedByFeature(changed, featureID) {
			continue
		}
		prefix := filepath.ToSlash(featuresDirectoryPath) + "/"
		if !strings.HasPrefix(changed, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(changed, prefix)
		other := strings.SplitN(remainder, "/", 2)[0]
		if other == "" || other == featureID {
			continue
		}
		if !checked[other] {
			checked[other] = true
			target, targetErr := FeatureTargetForID(r.root, other)
			if targetErr == nil {
				if _, stateErr := os.Lstat(target.StatePath); stateErr == nil {
					feature, loadErr := r.loadLocked(other)
					if loadErr != nil {
						return nil, fmt.Errorf("inspect dirty feature %s: %w", other, loadErr)
					}
					active[other] = feature.State.Status() == FlowActive
				} else if !os.IsNotExist(stateErr) {
					return nil, stateErr
				}
			}
		}
		if !active[other] {
			continue
		}
		blockers = append(blockers, ApprovalBlocker{
			Code: "other-feature-uncommitted", Path: changed,
			Message: fmt.Sprintf("feature %s has uncommitted changes", other),
		})
	}
	sortApprovalBlockers(blockers)
	return blockers, nil
}

func (r *FSFeatureRepository) CloseFeatureSession(request SessionCloseRequest) (FeatureSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ValidateFeatureID(request.FeatureID); err != nil {
		return FeatureSnapshot{}, err
	}
	if request.At.IsZero() {
		request.At = r.now()
	}
	// A user-edited primary document remains recoverable on resume, so session
	// cleanup must not reject that supported external-revision state. Protected
	// state/journal/review changes are still rejected by Load.
	feature, err := r.loadLocked(request.FeatureID)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	for _, artifact := range request.Artifacts {
		if !artifact.Stage.Valid() || !artifact.Role.Valid() {
			return FeatureSnapshot{}, fmt.Errorf("%w: invalid session artifact identity", ErrInvalidDomainValue)
		}
		if err := r.validateArtifactRoot(artifact.ArtifactRoot); err != nil {
			return FeatureSnapshot{}, err
		}
	}
	for _, artifact := range request.Artifacts {
		if err := removeArtifact(artifact.ArtifactRoot); err != nil {
			return FeatureSnapshot{}, fmt.Errorf("close %s session artifact: %w", artifact.Role, err)
		}
		if artifact.Role == mustAuthorRole(artifact.Stage) {
			delete(r.pending, pendingDraftKey(request.FeatureID, artifact.Stage))
		}
	}
	stage := feature.State.CurrentStage()
	role := mustAuthorRole(stage)
	entry, err := NewMemLogEntry(stage, role, MemLogSession, request.At, "feature sessions closed; pending draft discarded; published revision preserved")
	if err != nil {
		return FeatureSnapshot{}, err
	}
	return r.recordActivityLocked(feature, entry)
}

func (r *FSFeatureRepository) RecoverFeatureSession(request SessionRecoveryRequest) (SessionRecoveryResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ValidateFeatureID(request.FeatureID); err != nil {
		return SessionRecoveryResult{}, err
	}
	if request.At.IsZero() {
		request.At = r.now()
	}
	target, err := FeatureTargetForID(r.root, request.FeatureID)
	if err != nil {
		return SessionRecoveryResult{}, err
	}
	recovery, err := r.recoverLocked(target)
	if err != nil || len(recovery.Diagnostics) > 0 {
		return SessionRecoveryResult{Diagnostics: recovery.Diagnostics}, err
	}
	phase, err := r.recoverPhaseLocked(request.FeatureID)
	if err != nil || len(phase.Diagnostics) > 0 {
		return SessionRecoveryResult{Diagnostics: phase.Diagnostics}, err
	}
	feature, err := r.loadLockedCore(request.FeatureID, false)
	if err != nil {
		return SessionRecoveryResult{}, err
	}
	stage := feature.State.CurrentStage()
	stageState, _ := feature.State.Stage(stage)
	interrupted := stageState.ReviewStatus == ReviewRunning || stageState.ReviewStatus == ReviewAutomaticRework
	state := feature.State
	if interrupted {
		snapshot := state.Snapshot()
		stable := snapshot.Stages[stage]
		// Escalated is the stable user-controlled state: it advertises a fresh
		// /review, blocks approval, and never implies that an interrupted turn
		// may continue automatically.
		stable.ReviewStatus = ReviewEscalated
		stable.RetryCounters.ReviewRework = 0
		snapshot.Stages[stage] = stable
		state, err = NewFlowStateFromSnapshot(snapshot)
		if err != nil {
			return SessionRecoveryResult{}, err
		}
	}
	stateBytes, err := encodeState(state)
	if err != nil {
		return SessionRecoveryResult{}, err
	}
	body := "session recovered from authoritative documents, review reports, and mem-log"
	if interrupted {
		body += "; interrupted review returned to stable state and requires a new explicit /review"
	}
	entry, err := NewMemLogEntry(stage, mustAuthorRole(stage), MemLogRecovery, request.At, body)
	if err != nil {
		return SessionRecoveryResult{}, err
	}
	journalBytes, err := appendFeatureJournal(feature, entry, hash(stateBytes))
	if err != nil {
		return SessionRecoveryResult{}, err
	}
	if err := r.applyMutation(feature.Target, "session-recovery", map[string][]byte{"state.json": stateBytes, "mem-log.md": journalBytes}); err != nil {
		return SessionRecoveryResult{}, err
	}
	loaded, err := r.loadLocked(request.FeatureID)
	if err != nil {
		return SessionRecoveryResult{}, err
	}
	return SessionRecoveryResult{Feature: loaded, Interrupted: interrupted}, nil
}
