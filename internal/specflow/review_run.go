package specflow

import "time"

// ReviewOutcome describes the externally visible boundary of one review
// engine operation. A fingerprint decision never implies that the report was
// accepted for document bytes that the reviewer did not inspect.
type ReviewOutcome string

const (
	ReviewExisting           ReviewOutcome = "existing_review"
	ReviewTargetDiagnostics  ReviewOutcome = "target_diagnostics"
	ReviewReviewerMessage    ReviewOutcome = "reviewer_message"
	ReviewReportDiagnostics  ReviewOutcome = "report_diagnostics"
	ReviewReportCompleted    ReviewOutcome = "review_completed"
	ReviewMaterialDecisions  ReviewOutcome = "material_decisions"
	ReviewReworkRequired     ReviewOutcome = "rework_required"
	ReviewFingerprintChanged ReviewOutcome = "fingerprint_changed"
)

type ReviewFingerprintAction string

const (
	ReviewFingerprintRerun  ReviewFingerprintAction = "rerun"
	ReviewFingerprintAccept ReviewFingerprintAction = "accept_for_current_revision"
)

func (a ReviewFingerprintAction) Valid() bool {
	return a == ReviewFingerprintRerun || a == ReviewFingerprintAccept
}

type StartReviewRequest struct {
	FeatureID      string
	Stage          Stage
	Provider       string
	Model          string
	RuntimeContext string
}

// ReviewResult is intentionally independent from terminal presentation. The
// controller can turn Findings and FingerprintActions into its own ordered
// prompts without parsing reviewer prose.
type ReviewResult struct {
	Stage               Stage
	Outcome             ReviewOutcome
	Status              ReviewStatus
	RunID               uint64
	Path                string
	Message             string
	Diagnostics         []DocumentDiagnostic
	Findings            []FindingSnapshot
	MaterialFindings    []FindingSnapshot
	OriginalFingerprint Fingerprint
	CurrentFingerprint  Fingerprint
	FingerprintActions  []ReviewFingerprintAction
	Feature             FeatureSnapshot
}

type liveReviewRun struct {
	request           StartReviewRequest
	role              Role
	runID             uint64
	createdAt         time.Time
	original          Fingerprint
	turnFingerprint   Fingerprint
	previousFindings  []FindingSnapshot
	candidateFindings []FindingSnapshot
	attempts          int
}
