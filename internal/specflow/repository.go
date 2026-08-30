package specflow

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrRepositoryBlocked = errors.New("feature repository is blocked")
	ErrExternalChanges   = errors.New("feature artifacts changed outside Stepan")
	ErrPhaseCommit       = errors.New("feature phase commit failed")
)

// FeatureRepository is the durable seam used by flow orchestration. Each
// mutation owns the complete state/document/journal write ordering; callers do
// not write individual project files.
type FeatureRepository interface {
	Create(CreateFeatureRequest) (FeatureSnapshot, error)
	Load(featureID string) (FeatureSnapshot, error)
	InspectAuthorDraft(DraftArtifactRequest) (DraftInspection, error)
	PublishAuthorDraft(DraftArtifactRequest) (DraftPublication, error)
	PublishReview(ReviewArtifactRequest) (ReviewPublication, error)
	RecordDecision(featureID string, stage Stage, role Role, decision Decision) (FeatureSnapshot, error)
	RecordActivity(featureID string, entry MemLogEntry) (FeatureSnapshot, error)
	DiscardPending(featureID string, stage Stage, artifactRoot string) (FeatureSnapshot, error)
	Approve(ApproveStageRequest) (PhaseCommitResult, error)
	ReviseIntent(ReviseIntentRequest) (PhaseCommitResult, error)
	SupersedeIntent(SupersedeIntentRequest) (SupersessionResult, error)
	InspectChanges(featureID string) (ChangeInspection, error)
	Recover(featureID string) (RecoveryResult, error)
}

type CreateFeatureRequest struct {
	FeatureID string
	Brief     string
	At        time.Time
}

type DocumentArtifact struct {
	Stage   Stage
	Path    string
	Hash    string
	Content []byte
}

type ReviewArtifact struct {
	Stage   Stage
	RunID   uint64
	Path    string
	Hash    string
	Content []byte
}

type FeatureSnapshot struct {
	Target    FeatureTarget
	State     FlowState
	Documents map[Stage]DocumentArtifact
	Reviews   []ReviewArtifact
	Journal   []MemLogEntry
	Changes   ChangeInspection
}

type DraftArtifactRequest struct {
	FeatureID      string
	Stage          Stage
	ArtifactRoot   string
	Mode           DocumentValidationMode
	ActiveIDs      []StableID
	RetainedIDs    []StableID
	UpstreamHashes []UpstreamHash
	ExpectedHash   string
	At             time.Time
}

type DraftInspection struct {
	Hash       string
	Validation DocumentResult
	State      FlowState
}

type DraftPublication struct {
	Published  bool
	Hash       string
	Validation DocumentResult
	Feature    FeatureSnapshot
}

type ReviewArtifactRequest struct {
	FeatureID        string
	Stage            Stage
	ArtifactRoot     string
	RunID            uint64
	Status           ReviewStatus
	Original         Fingerprint
	Accepted         *Fingerprint
	Attempts         int
	ActiveIDs        []StableID
	PreviousFindings []FindingSnapshot
	Provider         string
	Model            string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ReviewPublication struct {
	Published  bool
	RunID      uint64
	Path       string
	Validation DocumentResult
	Feature    FeatureSnapshot
}

type ApproveStageRequest struct {
	FeatureID string
	Stage     Stage
	At        time.Time
}

type ReviseIntentRequest struct {
	FeatureID string
	At        time.Time
}

type SupersedeIntentRequest struct {
	OldFeatureID string
	NewFeatureID string
	At           time.Time
}

// ApprovalBlocker is a complete, user-actionable preflight reason. Approval
// operations return all blockers without mutating durable feature state.
type ApprovalBlocker struct {
	Code    string
	Path    string
	Message string
}

type PhaseCommitResult struct {
	Committed bool
	Blocking  []ApprovalBlocker
	Feature   FeatureSnapshot
}

type SupersessionResult struct {
	Committed bool
	Blocking  []ApprovalBlocker
	Old       FeatureSnapshot
	New       FeatureSnapshot
}

type ChangeClass string

const (
	ChangeDocumentRevision  ChangeClass = "document_revision"
	ChangeProtectedArtifact ChangeClass = "protected_artifact"
)

type ArtifactChange struct {
	Class        ChangeClass
	Path         string
	Stage        Stage
	ExpectedHash string
	ActualHash   string
	Missing      bool
	Message      string
}

type ChangeInspection struct {
	DocumentRevisions []ArtifactChange
	Blocking          []ArtifactChange
}

func (c ChangeInspection) Empty() bool {
	return len(c.DocumentRevisions) == 0 && len(c.Blocking) == 0
}

func (c ChangeInspection) Error() error {
	if c.Empty() {
		return nil
	}
	if len(c.Blocking) > 0 {
		return fmt.Errorf("%w: %s", ErrRepositoryBlocked, c.Blocking[0].Message)
	}
	return fmt.Errorf("%w: %s", ErrExternalChanges, c.DocumentRevisions[0].Message)
}

type RecoveryDiagnostic struct {
	Path    string
	Message string
}

type RecoveryResult struct {
	Completed   bool
	Diagnostics []RecoveryDiagnostic
}
