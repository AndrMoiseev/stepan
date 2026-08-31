package specflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

var (
	ErrReviewNotStarted          = errors.New("review is not started")
	ErrReviewFingerprintDecision = errors.New("review fingerprint decision is pending")
)

// ReviewEngine owns one explicit /review run. It never starts an author turn:
// material decisions and automatic rework belong to the controller and the
// next review lifecycle layer.
type ReviewEngine struct {
	workspace  string
	runner     dialogueRunner
	repository FeatureRepository
	catalog    PromptCatalog
	now        func() time.Time
	newRoot    func(string) (string, error)

	thread       agentruntime.Thread
	artifactRoot string
	feature      FeatureSnapshot
	run          *liveReviewRun
	pendingDrift bool
	terminal     bool
}

func NewReviewEngine(workspace string, runner dialogueRunner, repository FeatureRepository, catalog PromptCatalog) (*ReviewEngine, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, fmt.Errorf("create review engine: workspace is required")
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("create review engine: %w", err)
	}
	if runner == nil || repository == nil || catalog == nil {
		return nil, fmt.Errorf("create review engine: runner, repository, and prompt catalog are required")
	}
	return &ReviewEngine{
		workspace: absolute, runner: runner, repository: repository, catalog: catalog,
		now: time.Now, newRoot: CreateArtifactRoot,
	}, nil
}

// Start is the effect boundary for an explicit /review command. Duplicate
// completed fingerprints return before an artifact root or agent thread is
// created.
func (e *ReviewEngine) Start(request StartReviewRequest) (ReviewResult, error) {
	if e.run != nil && !e.terminal {
		return ReviewResult{}, fmt.Errorf("start review: a review run is already active")
	}
	if strings.TrimSpace(request.FeatureID) == "" || !validRuntimeMetadata(request.Provider) || !validRuntimeMetadata(request.Model) {
		return ReviewResult{}, fmt.Errorf("start review: feature ID, provider, and model are required")
	}
	policy, err := PolicyForStage(request.Stage)
	if err != nil {
		return ReviewResult{}, err
	}
	if !policy.ReviewAvailable {
		return ReviewResult{}, fmt.Errorf("start review: %s has no agent review", request.Stage)
	}
	feature, err := e.repository.Load(request.FeatureID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("start %s review: %w", request.Stage, err)
	}
	stageState, _ := feature.State.Stage(request.Stage)
	if stageState.Status != StagePublished {
		return ReviewResult{}, fmt.Errorf("start %s review: stage must be published", request.Stage)
	}
	fingerprint, err := reviewFingerprint(feature, policy)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("start %s review: %w", request.Stage, err)
	}
	if existing, ok := completedReviewForFingerprint(stageState, fingerprint); ok {
		return ReviewResult{
			Stage: request.Stage, Outcome: ReviewExisting, Status: ReviewCompleted,
			RunID: existing.ID, Path: existing.Path, OriginalFingerprint: existing.OriginalFingerprint,
			CurrentFingerprint: fingerprint, Feature: feature,
		}, nil
	}

	targetValidation, targetIDs := validateReviewTarget(feature, policy)
	if !targetValidation.Valid() {
		return ReviewResult{
			Stage: request.Stage, Outcome: ReviewTargetDiagnostics, Status: stageState.ReviewStatus,
			Diagnostics:         append([]DocumentDiagnostic(nil), targetValidation.Diagnostics...),
			OriginalFingerprint: fingerprint, CurrentFingerprint: fingerprint, Feature: feature,
		}, nil
	}

	role, err := ReviewerRole(request.Stage)
	if err != nil {
		return ReviewResult{}, err
	}
	previous := latestReviewFindings(feature, request.Stage, targetIDs)
	runID := nextReviewRunID(stageState)
	reuse := e.terminal && e.run != nil && e.thread != nil && e.run.request.FeatureID == request.FeatureID && e.run.request.Stage == request.Stage
	if !reuse && e.run != nil {
		if err := e.Close(); err != nil {
			return ReviewResult{}, fmt.Errorf("close previous reviewer session: %w", err)
		}
	}
	if !reuse {
		artifactRoot, err := e.newRoot(e.workspace)
		if err != nil {
			return ReviewResult{}, fmt.Errorf("create %s review artifact root: %w", request.Stage, err)
		}
		context := reviewerRuntimeContext(feature, policy, artifactRoot, runID, request.RuntimeContext)
		prompt, err := e.catalog.Compose(role, context)
		if err != nil {
			_ = removeArtifact(artifactRoot)
			return ReviewResult{}, fmt.Errorf("compose %s reviewer prompt: %w", request.Stage, err)
		}
		thread, err := e.runner.StartThread(agentruntime.ThreadConfig{
			BootstrapInstructions: prompt,
			OutputSchema:          DialogueSchema(),
			Workspace:             e.workspace,
			ArtifactRoot:          artifactRoot,
		})
		if err != nil {
			_ = removeArtifact(artifactRoot)
			return ReviewResult{}, fmt.Errorf("start %s reviewer thread: %w", request.Stage, err)
		}
		e.thread = thread
		e.artifactRoot = artifactRoot
	}

	e.feature = feature
	e.run = &liveReviewRun{
		request: request, role: role, runID: runID, createdAt: e.now(),
		original: fingerprint, turnFingerprint: fingerprint, previousFindings: previous,
	}
	e.pendingDrift = false
	e.terminal = false
	turnPrompt := "Review the current published document now. Write the complete report to review.md and return an artifact envelope."
	if reuse {
		turnPrompt += "\n\nCurrent run context:\n" + reviewerRuntimeContext(feature, policy, e.artifactRoot, runID, request.RuntimeContext)
	}
	return e.runTurn(turnPrompt, targetIDs, true)
}

// Submit continues reviewer dialogue without creating another numbered run.
// TASK-008 can layer material-decision semantics on this primitive.
func (e *ReviewEngine) Submit(message string) (ReviewResult, error) {
	if e.run == nil || e.thread == nil {
		return ReviewResult{}, ErrReviewNotStarted
	}
	if e.pendingDrift {
		return ReviewResult{}, ErrReviewFingerprintDecision
	}
	if strings.TrimSpace(message) == "" {
		return ReviewResult{}, fmt.Errorf("reviewer message must not be empty")
	}
	_, targetIDs := validateReviewTarget(e.feature, mustPolicy(e.run.request.Stage))
	return e.runTurn(strings.TrimSpace(message), targetIDs, false)
}

func (e *ReviewEngine) DecideFingerprint(action ReviewFingerprintAction) (ReviewResult, error) {
	if e.run == nil || e.thread == nil {
		return ReviewResult{}, ErrReviewNotStarted
	}
	if !e.pendingDrift {
		return ReviewResult{}, fmt.Errorf("no review fingerprint decision is pending")
	}
	if !action.Valid() {
		return ReviewResult{}, fmt.Errorf("unknown review fingerprint action %q", action)
	}
	feature, current, err := e.reloadFingerprint()
	if err != nil {
		return ReviewResult{}, err
	}
	e.feature = feature
	switch action {
	case ReviewFingerprintAccept:
		e.pendingDrift = false
		return e.publishCandidate(current)
	case ReviewFingerprintRerun:
		e.pendingDrift = false
		e.run.turnFingerprint = current
		e.run.previousFindings = cloneFindingSnapshots(e.run.candidateFindings)
		validation, targetIDs := validateReviewTarget(feature, mustPolicy(e.run.request.Stage))
		if !validation.Valid() {
			result := e.baseResult(ReviewTargetDiagnostics, ReviewRunning, conciseDocumentDiagnostics(validation.Diagnostics))
			result.Diagnostics = append([]DocumentDiagnostic(nil), validation.Diagnostics...)
			return result, nil
		}
		return e.runTurn("The reviewed target or an upstream document changed during review. Re-read the current revisions and overwrite review.md with the complete report for them. Preserve finding identity where the problem is unchanged; supersede a materially changed problem.", targetIDs, true)
	default:
		panic("unreachable")
	}
}

func (e *ReviewEngine) runTurn(prompt string, targetIDs []StableID, artifactRequired bool) (ReviewResult, error) {
	for parserAttempt := 0; parserAttempt < DefaultRetryLimit; parserAttempt++ {
		e.run.attempts++
		stage := e.run.request.Stage
		raw, err := e.runner.RunTurn(e.thread, prompt)
		if err != nil {
			e.abort()
			return ReviewResult{}, fmt.Errorf("run %s reviewer turn: %w", stage, err)
		}
		envelope, err := DecodeEnvelope(raw)
		if err != nil {
			e.abort()
			return ReviewResult{}, fmt.Errorf("decode %s reviewer response: %w", stage, err)
		}
		if envelope.Kind == KindMessage {
			if artifactRequired {
				e.abort()
				return ReviewResult{}, fmt.Errorf("run %s reviewer turn: reviewer must publish review.md before dialogue", stage)
			}
			return e.baseResult(ReviewReviewerMessage, ReviewRunning, envelope.Message), nil
		}

		validation, err := e.inspectReport(targetIDs)
		if err != nil {
			e.abort()
			return ReviewResult{}, err
		}
		if validation.Valid() {
			e.run.candidateFindings = findingsFromDocument(validation)
			if err := e.publishProvisional(); err != nil {
				return ReviewResult{}, err
			}
			e.run.previousFindings = cloneFindingSnapshots(e.run.candidateFindings)
			feature, current, err := e.reloadFingerprint()
			if err != nil {
				return ReviewResult{}, err
			}
			e.feature = feature
			if !current.Equal(e.run.turnFingerprint) {
				e.pendingDrift = true
				result := e.baseResult(ReviewFingerprintChanged, ReviewRunning, "reviewed document revisions changed during the reviewer turn")
				result.Findings = cloneFindingSnapshots(e.run.candidateFindings)
				result.MaterialFindings = pendingMaterialFindings(e.run.candidateFindings)
				result.CurrentFingerprint = current
				result.FingerprintActions = []ReviewFingerprintAction{ReviewFingerprintRerun, ReviewFingerprintAccept}
				return result, nil
			}
			return e.publishCandidate(current)
		}
		if parserAttempt+1 == DefaultRetryLimit {
			result := e.baseResult(ReviewReportDiagnostics, ReviewRunning, conciseDocumentDiagnostics(validation.Diagnostics))
			result.Diagnostics = append([]DocumentDiagnostic(nil), validation.Diagnostics...)
			return result, nil
		}
		prompt = parserRepairPrompt("review.md", validation.Diagnostics)
	}
	panic("unreachable")
}

func (e *ReviewEngine) inspectReport(targetIDs []StableID) (DocumentResult, error) {
	data, err := os.ReadFile(filepath.Join(e.artifactRoot, "review.md"))
	if err != nil {
		return DocumentResult{}, fmt.Errorf("read %s review artifact: %w", e.run.request.Stage, err)
	}
	return ParseDocument(DocumentRequest{
		Kind: reviewKindForStage(e.run.request.Stage), Mode: ValidateDraft, Markdown: string(data),
		ActiveIDs: targetIDs, IssuedIDs: e.feature.State.IssuedIDs(), PreviousFindings: e.run.previousFindings,
	}), nil
}

func (e *ReviewEngine) publishProvisional() error {
	publication, err := e.repository.PublishReview(ReviewArtifactRequest{
		FeatureID: e.run.request.FeatureID, Stage: e.run.request.Stage, ArtifactRoot: e.artifactRoot,
		RunID: e.run.runID, Status: ReviewRunning, Original: e.run.original,
		Attempts: min(e.run.attempts, DefaultRetryLimit), ActiveIDs: reviewTargetIDs(e.feature, e.run.request.Stage),
		PreviousFindings: e.run.previousFindings, Provider: e.run.request.Provider, Model: e.run.request.Model,
		CreatedAt: e.run.createdAt, UpdatedAt: e.now(),
	})
	if err != nil {
		return fmt.Errorf("publish live %s review: %w", e.run.request.Stage, err)
	}
	if !publication.Published {
		return fmt.Errorf("publish live %s review: report became structurally invalid: %s", e.run.request.Stage, conciseDocumentDiagnostics(publication.Validation.Diagnostics))
	}
	e.feature = publication.Feature
	e.run.runID = publication.RunID
	return nil
}

func (e *ReviewEngine) publishCandidate(accepted Fingerprint) (ReviewResult, error) {
	status, outcome := reviewDisposition(e.run.candidateFindings)
	acceptedCopy := accepted
	publication, err := e.repository.PublishReview(ReviewArtifactRequest{
		FeatureID: e.run.request.FeatureID, Stage: e.run.request.Stage, ArtifactRoot: e.artifactRoot,
		RunID: e.run.runID, Status: status, Original: e.run.original, Accepted: &acceptedCopy,
		Attempts: min(e.run.attempts, DefaultRetryLimit), ActiveIDs: reviewTargetIDs(e.feature, e.run.request.Stage),
		PreviousFindings: e.run.previousFindings, Provider: e.run.request.Provider, Model: e.run.request.Model,
		CreatedAt: e.run.createdAt, UpdatedAt: e.now(),
	})
	if err != nil {
		return ReviewResult{}, fmt.Errorf("publish %s review: %w", e.run.request.Stage, err)
	}
	if !publication.Published {
		result := e.baseResult(ReviewReportDiagnostics, ReviewRunning, conciseDocumentDiagnostics(publication.Validation.Diagnostics))
		result.Diagnostics = append([]DocumentDiagnostic(nil), publication.Validation.Diagnostics...)
		return result, nil
	}
	e.feature = publication.Feature
	e.run.runID = publication.RunID
	e.run.previousFindings = cloneFindingSnapshots(e.run.candidateFindings)
	if recorded, err := e.recordAcceptedFingerprint(accepted); err != nil {
		return ReviewResult{}, err
	} else {
		e.feature = recorded
	}
	result := e.baseResult(outcome, status, "")
	result.Path = publication.Path
	result.Findings = cloneFindingSnapshots(e.run.candidateFindings)
	result.MaterialFindings = pendingMaterialFindings(e.run.candidateFindings)
	result.CurrentFingerprint = accepted
	e.terminal = status == ReviewCompleted
	return result, nil
}

func (e *ReviewEngine) recordAcceptedFingerprint(accepted Fingerprint) (FeatureSnapshot, error) {
	body := fmt.Sprintf("review run %d accepted: started_revision=%s accepted_for_revision=%s upstream_started=%s upstream_accepted=%s",
		e.run.runID, e.run.original.TargetHash(), accepted.TargetHash(), formatUpstreamFingerprint(e.run.original), formatUpstreamFingerprint(accepted))
	entry, err := NewMemLogEntry(e.run.request.Stage, e.run.role, MemLogReview, e.now(), body)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	feature, err := e.repository.RecordActivity(e.run.request.FeatureID, entry)
	if err != nil {
		return FeatureSnapshot{}, fmt.Errorf("record %s review fingerprint: %w", e.run.request.Stage, err)
	}
	return feature, nil
}

func (e *ReviewEngine) reloadFingerprint() (FeatureSnapshot, Fingerprint, error) {
	feature, err := e.repository.Load(e.run.request.FeatureID)
	if err != nil {
		return FeatureSnapshot{}, Fingerprint{}, fmt.Errorf("reload %s review documents: %w", e.run.request.Stage, err)
	}
	policy := mustPolicy(e.run.request.Stage)
	fingerprint, err := reviewFingerprint(feature, policy)
	if err != nil {
		return FeatureSnapshot{}, Fingerprint{}, err
	}
	return feature, fingerprint, nil
}

func (e *ReviewEngine) baseResult(outcome ReviewOutcome, status ReviewStatus, message string) ReviewResult {
	result := ReviewResult{Outcome: outcome, Status: status, Message: message, Feature: e.feature}
	if e.run != nil {
		result.Stage = e.run.request.Stage
		result.RunID = e.run.runID
		result.Path = filepath.ToSlash(reviewRelativePath(e.run.request.Stage, e.run.runID))
		result.OriginalFingerprint = e.run.original
		result.CurrentFingerprint = e.run.turnFingerprint
	}
	return result
}

func (e *ReviewEngine) Close() error {
	if e.thread == nil && e.artifactRoot == "" {
		e.reset()
		return nil
	}
	var closeErr error
	if e.thread != nil {
		closeErr = e.runner.CloseThread(e.thread)
	}
	removeErr := removeArtifact(e.artifactRoot)
	e.reset()
	return errors.Join(closeErr, removeErr)
}

func (e *ReviewEngine) abort() {
	if e.thread != nil {
		_ = e.runner.CloseThread(e.thread)
	}
	_ = removeArtifact(e.artifactRoot)
	e.reset()
}

func (e *ReviewEngine) reset() {
	e.thread = nil
	e.artifactRoot = ""
	e.feature = FeatureSnapshot{}
	e.run = nil
	e.pendingDrift = false
	e.terminal = false
}

func validateReviewTarget(feature FeatureSnapshot, policy StagePolicy) (DocumentResult, []StableID) {
	target, ok := feature.Documents[policy.Stage]
	if !ok {
		result := ParseDocument(DocumentRequest{Kind: documentKindForStage(policy.Stage), Mode: ValidateDraft})
		return result, nil
	}
	first := ParseDocument(DocumentRequest{Kind: documentKindForStage(policy.Stage), Mode: ValidateDraft, Markdown: string(target.Content)})
	targetIDs := observedStableIDs(first.ObservedIDs)
	result := ParseDocument(DocumentRequest{
		Kind: documentKindForStage(policy.Stage), Mode: ValidateDraft, Markdown: string(target.Content),
		ActiveIDs: activeDocumentIDs(feature, policy.UpstreamStages), IssuedIDs: feature.State.IssuedIDs(), RetainedIDs: targetIDs,
	})
	return result, targetIDs
}

func reviewFingerprint(feature FeatureSnapshot, policy StagePolicy) (Fingerprint, error) {
	target, ok := feature.Documents[policy.Stage]
	if !ok {
		return Fingerprint{}, fmt.Errorf("published %s document is missing", policy.Stage)
	}
	upstream := make([]UpstreamHash, 0, len(policy.UpstreamStages))
	for _, stage := range policy.UpstreamStages {
		document, ok := feature.Documents[stage]
		if !ok {
			return Fingerprint{}, fmt.Errorf("required upstream %s document is missing", stage)
		}
		upstream = append(upstream, UpstreamHash{Stage: stage, Hash: document.Hash})
	}
	return NewFingerprint(target.Hash, upstream)
}

func completedReviewForFingerprint(state StageState, fingerprint Fingerprint) (ReviewRun, bool) {
	for i := len(state.Reviews) - 1; i >= 0; i-- {
		run := state.Reviews[i]
		if run.Status == ReviewCompleted && run.AcceptedFingerprint != nil && run.AcceptedFingerprint.Equal(fingerprint) {
			return run, true
		}
	}
	return ReviewRun{}, false
}

func nextReviewRunID(state StageState) uint64 {
	var next uint64 = 1
	for _, run := range state.Reviews {
		if run.ID >= next {
			next = run.ID + 1
		}
	}
	return next
}

func reviewerRuntimeContext(feature FeatureSnapshot, policy StagePolicy, artifactRoot string, runID uint64, extra string) string {
	var result strings.Builder
	fmt.Fprintf(&result, "Feature: %s\nStage: %s\nReview run: %d\n", feature.Target.ID, policy.Stage, runID)
	fmt.Fprintf(&result, "Writable artifact root: %s\nFixed artifact filename: review.md\n", artifactRoot)
	if target, ok := feature.Documents[policy.Stage]; ok {
		fmt.Fprintf(&result, "Reviewed document (read-only): %s (sha256 %s)\n", target.Path, target.Hash)
	}
	result.WriteString("Upstream documents (read-only):")
	if len(policy.UpstreamStages) == 0 {
		result.WriteString(" none\n")
	} else {
		result.WriteByte('\n')
		for _, stage := range policy.UpstreamStages {
			if document, ok := feature.Documents[stage]; ok {
				fmt.Fprintf(&result, "- %s: %s (sha256 %s)\n", stage, document.Path, document.Hash)
			}
		}
	}
	fmt.Fprintf(&result, "Memory log (read-only): %s\n", feature.Target.JournalPath)
	result.WriteString("Previous review reports (read-only):")
	count := 0
	for _, review := range feature.Reviews {
		if review.Stage == policy.Stage {
			fmt.Fprintf(&result, "\n- %s", review.Path)
			count++
		}
	}
	if count == 0 {
		result.WriteString(" none")
	}
	result.WriteByte('\n')
	if strings.TrimSpace(extra) != "" {
		result.WriteString("\nSession context:\n")
		result.WriteString(strings.TrimSpace(extra))
		result.WriteByte('\n')
	}
	return strings.TrimSpace(result.String())
}

func latestReviewFindings(feature FeatureSnapshot, stage Stage, activeIDs []StableID) []FindingSnapshot {
	reviews := append([]ReviewArtifact(nil), feature.Reviews...)
	sort.Slice(reviews, func(i, j int) bool { return reviews[i].RunID > reviews[j].RunID })
	for _, review := range reviews {
		if review.Stage != stage {
			continue
		}
		parsed := ParseDocument(DocumentRequest{Kind: reviewKindForStage(stage), Mode: ValidateDraft, Markdown: string(review.Content), ActiveIDs: activeIDs})
		return findingsFromDocument(parsed)
	}
	return nil
}

func findingsFromDocument(result DocumentResult) []FindingSnapshot {
	findings := make([]FindingSnapshot, 0, len(result.Document.Findings))
	for _, finding := range result.Document.Findings {
		findings = append(findings, finding.Finding.Snapshot())
	}
	return findings
}

func reviewTargetIDs(feature FeatureSnapshot, stage Stage) []StableID {
	policy := mustPolicy(stage)
	_, ids := validateReviewTarget(feature, policy)
	return ids
}

func reviewDisposition(findings []FindingSnapshot) (ReviewStatus, ReviewOutcome) {
	if len(pendingMaterialFindings(findings)) > 0 {
		return ReviewAwaitingDecisions, ReviewMaterialDecisions
	}
	for _, finding := range findings {
		if finding.Status == FindingOpen {
			return ReviewAutomaticRework, ReviewReworkRequired
		}
	}
	return ReviewCompleted, ReviewReportCompleted
}

func pendingMaterialFindings(findings []FindingSnapshot) []FindingSnapshot {
	result := make([]FindingSnapshot, 0)
	for _, finding := range findings {
		if finding.Kind == FindingMaterial && finding.Status == FindingOpen && finding.Decision.Decision == DecisionPending {
			result = append(result, cloneFindingSnapshot(finding))
		}
	}
	return result
}

func cloneFindingSnapshots(values []FindingSnapshot) []FindingSnapshot {
	result := make([]FindingSnapshot, len(values))
	for i, value := range values {
		result[i] = cloneFindingSnapshot(value)
	}
	return result
}

func cloneFindingSnapshot(value FindingSnapshot) FindingSnapshot {
	value.Traces = append([]StableID(nil), value.Traces...)
	return value
}

func formatUpstreamFingerprint(fingerprint Fingerprint) string {
	values := fingerprint.UpstreamHashes()
	if len(values) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, string(value.Stage)+"="+value.Hash)
	}
	return strings.Join(parts, ",")
}

func mustPolicy(stage Stage) StagePolicy {
	policy, err := PolicyForStage(stage)
	if err != nil {
		panic(err)
	}
	return policy
}

func validRuntimeMetadata(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n")
}
