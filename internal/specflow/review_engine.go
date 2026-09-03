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

// ReviewEngine owns one explicit /review run and its material decision queue.
// Applying a report may invoke the author once; every later review round is
// started explicitly by the user and receives a new numbered report.
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
	registry     *SessionRegistry
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

// Start is the effect boundary for an explicit /review command. Every command
// creates a new numbered review, even when the document fingerprint is unchanged.
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
		if registry, managed := e.runner.(*SessionRegistry); managed {
			registered, acquireErr := registry.Acquire(SessionRequest{FeatureID: request.FeatureID, Role: role, Config: agentruntime.ThreadConfig{
				BootstrapInstructions: prompt, OutputSchema: DialogueSchema(), Workspace: e.workspace, ArtifactRoot: artifactRoot,
			}})
			if acquireErr != nil {
				_ = removeArtifact(artifactRoot)
				return ReviewResult{}, fmt.Errorf("start %s reviewer thread: %w", request.Stage, acquireErr)
			}
			e.thread = registered.Thread
			e.artifactRoot = registered.ArtifactRoot
			e.registry = registry
			reuse = registered.Reused
		} else {
			thread, startErr := e.runner.StartThread(agentruntime.ThreadConfig{
				BootstrapInstructions: prompt, OutputSchema: DialogueSchema(), Workspace: e.workspace, ArtifactRoot: artifactRoot,
			})
			if startErr != nil {
				_ = removeArtifact(artifactRoot)
				return ReviewResult{}, fmt.Errorf("start %s reviewer thread: %w", request.Stage, startErr)
			}
			e.thread = thread
			e.artifactRoot = artifactRoot
		}
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
	result, err := e.runTurn(turnPrompt, targetIDs, true)
	if err != nil {
		return ReviewResult{}, err
	}
	return e.checkpointPublishedReview(result)
}

// StartWithAuthor is the lifecycle entry used when the controller has a live
// author session. Review never invokes the author until the user runs /apply.
func (e *ReviewEngine) StartWithAuthor(request StartReviewRequest, _ *StageEngine) (ReviewResult, error) {
	return e.Start(request)
}

// Submit continues reviewer dialogue without creating another numbered run.
func (e *ReviewEngine) Submit(message string) (ReviewResult, error) {
	return e.SubmitWithAuthor(message, nil)
}

// SubmitWithAuthor continues free-form reviewer dialogue. The author parameter
// is retained for API compatibility; only an explicit /apply may invoke it.
func (e *ReviewEngine) SubmitWithAuthor(message string, _ *StageEngine) (ReviewResult, error) {
	if e.run == nil || e.thread == nil {
		return ReviewResult{}, ErrReviewNotStarted
	}
	if e.pendingDrift {
		return ReviewResult{}, ErrReviewFingerprintDecision
	}
	if strings.TrimSpace(message) == "" {
		return ReviewResult{}, fmt.Errorf("reviewer message must not be empty")
	}
	entry, err := NewMemLogEntry(e.run.request.Stage, e.run.role, MemLogUserMessage, e.now(), message)
	if err != nil {
		return ReviewResult{}, err
	}
	feature, err := e.repository.RecordActivity(e.run.request.FeatureID, entry)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("record reviewer dialogue input: %w", err)
	}
	e.feature = feature
	_, targetIDs := validateReviewTarget(e.feature, mustPolicy(e.run.request.Stage))
	result, err := e.runTurn(strings.TrimSpace(message), targetIDs, false)
	if err != nil {
		return ReviewResult{}, err
	}
	return e.checkpointPublishedReview(result)
}

// ApplyPendingMaterial implements /apply: every currently pending material
// finding is accepted in report order and the author is invoked exactly once.
// A later review is never started automatically.
func (e *ReviewEngine) ApplyPendingMaterial(author *StageEngine) (ReviewResult, error) {
	if e.run == nil || e.thread == nil {
		return ReviewResult{}, ErrReviewNotStarted
	}
	pending := pendingMaterialFindings(e.run.candidateFindings)
	if len(pending) == 0 {
		if len(openFixFindings(e.run.candidateFindings)) == 0 {
			return ReviewResult{}, fmt.Errorf("review has no agreed fixes to apply")
		}
		result := e.baseResult(ReviewReworkRequired, ReviewAutomaticRework, "")
		result.Findings = cloneFindingSnapshots(e.run.candidateFindings)
		result.MaterialFindings = nil
		if _, err := e.checkpointResult(result, CheckpointReviewApply); err != nil {
			return ReviewResult{}, err
		}
		return e.AutomaticRework(author)
	}
	decisions := make([]MaterialFindingDecision, 0, len(pending))
	for _, finding := range pending {
		decisions = append(decisions, MaterialFindingDecision{
			FindingID: finding.ID, Decision: DecisionFix,
			Rationale: "User accepted the pending recommendation with /apply.",
		})
	}
	result, err := e.decideMaterial(decisions, nil)
	if err != nil || result.Outcome != ReviewReworkRequired {
		return result, err
	}
	if _, err := e.checkpointResult(result, CheckpointReviewApply); err != nil {
		return ReviewResult{}, err
	}
	return e.AutomaticRework(author)
}

// DecideMaterial records one explicit fix/dismiss decision. Dismissal requires
// user rationale and can never target a contract violation.
func (e *ReviewEngine) DecideMaterial(decision MaterialFindingDecision, author *StageEngine) (ReviewResult, error) {
	return e.decideMaterial([]MaterialFindingDecision{decision}, author)
}

func (e *ReviewEngine) decideMaterial(decisions []MaterialFindingDecision, author *StageEngine) (ReviewResult, error) {
	if e.run == nil || e.thread == nil {
		return ReviewResult{}, ErrReviewNotStarted
	}
	if e.pendingDrift {
		return ReviewResult{}, ErrReviewFingerprintDecision
	}
	if len(decisions) == 0 {
		return ReviewResult{}, fmt.Errorf("material review decision is required")
	}
	updated := cloneFindingSnapshots(e.run.candidateFindings)
	seen := make(map[StableID]struct{}, len(decisions))
	for _, requested := range decisions {
		if !requested.FindingID.Valid() || (requested.Decision != DecisionFix && requested.Decision != DecisionDismiss) {
			return ReviewResult{}, fmt.Errorf("invalid material decision for finding %q", requested.FindingID)
		}
		rationale := strings.TrimSpace(requested.Rationale)
		if rationale == "" || strings.ContainsAny(rationale, "\r\n") {
			return ReviewResult{}, fmt.Errorf("material finding decision requires a single-line user rationale")
		}
		if _, duplicate := seen[requested.FindingID]; duplicate {
			return ReviewResult{}, fmt.Errorf("duplicate material decision for finding %s", requested.FindingID)
		}
		seen[requested.FindingID] = struct{}{}
		index := findingIndex(updated, requested.FindingID)
		if index < 0 {
			return ReviewResult{}, fmt.Errorf("review finding %s is not present", requested.FindingID)
		}
		finding := updated[index]
		if finding.Kind == FindingContractViolation {
			if requested.Decision == DecisionDismiss {
				return ReviewResult{}, fmt.Errorf("contract violation %s cannot be dismissed", requested.FindingID)
			}
			return ReviewResult{}, fmt.Errorf("contract violation %s is already fixed by reviewer decision", requested.FindingID)
		}
		if finding.Status != FindingOpen || finding.Decision.Decision != DecisionPending {
			return ReviewResult{}, fmt.Errorf("material finding %s is not pending", requested.FindingID)
		}
		finding.Decision = FindingDecisionRecord{Decision: requested.Decision, DecidedBy: DecidedByUser, Rationale: rationale}
		if requested.Decision == DecisionDismiss {
			finding.Status = FindingDismissed
		}
		updated[index] = finding
	}

	data, err := os.ReadFile(filepath.Join(e.artifactRoot, "review.md"))
	if err != nil {
		return ReviewResult{}, fmt.Errorf("read live review report for material decision: %w", err)
	}
	for _, requested := range decisions {
		data, err = rewriteFindingDecision(data, requested)
		if err != nil {
			return ReviewResult{}, err
		}
	}
	for _, requested := range decisions {
		label := "fix"
		if requested.Decision == DecisionDismiss {
			label = "dismiss"
		}
		decision := Decision{Author: DecisionUser, Decision: label + " review finding " + requested.FindingID.String(),
			Rationale: strings.TrimSpace(requested.Rationale), Alternatives: []string{}, Supersedes: []int{}}
		feature, err := e.repository.RecordDecision(e.run.request.FeatureID, e.run.request.Stage, e.run.role, decision)
		if err != nil {
			return ReviewResult{}, fmt.Errorf("record material review decision: %w", err)
		}
		e.feature = feature
	}
	if err := os.WriteFile(filepath.Join(e.artifactRoot, "review.md"), data, 0o600); err != nil {
		return ReviewResult{}, fmt.Errorf("write live review report decision: %w", err)
	}
	_, targetIDs := validateReviewTarget(e.feature, mustPolicy(e.run.request.Stage))
	validation, err := e.inspectReport(targetIDs)
	if err != nil {
		return ReviewResult{}, err
	}
	if !validation.Valid() {
		return ReviewResult{}, fmt.Errorf("material decision made review report invalid: %s", conciseDocumentDiagnostics(validation.Diagnostics))
	}
	e.run.candidateFindings = findingsFromDocument(validation)
	result, err := e.publishCandidate(e.run.turnFingerprint)
	if err != nil || result.Outcome != ReviewReworkRequired || author == nil {
		return result, err
	}
	return e.AutomaticRework(author)
}

// AutomaticRework applies the current review through one author turn. Despite
// the legacy name retained for compatibility with callers, it never invokes
// the reviewer; another review requires a new explicit /review command.
func (e *ReviewEngine) AutomaticRework(author *StageEngine) (ReviewResult, error) {
	if e.run == nil || e.thread == nil {
		return ReviewResult{}, ErrReviewNotStarted
	}
	if author == nil {
		return ReviewResult{}, fmt.Errorf("automatic review rework requires the current author session")
	}
	if e.pendingDrift {
		return ReviewResult{}, ErrReviewFingerprintDecision
	}
	if policy, ok := author.Policy(); !ok || policy.Stage != e.run.request.Stage || author.featureID != e.run.request.FeatureID {
		return ReviewResult{}, fmt.Errorf("automatic review rework requires the current %s author session for feature %s", e.run.request.Stage, e.run.request.FeatureID)
	}
	if len(pendingMaterialFindings(e.run.candidateFindings)) > 0 {
		return ReviewResult{}, fmt.Errorf("material review decisions are still pending")
	}

	scope := openFixFindings(e.run.candidateFindings)
	if len(scope) == 0 {
		return ReviewResult{}, fmt.Errorf("review has no agreed fixes to apply")
	}
	e.run.reworkAttempts++
	started := ReviewProgressEvent{Kind: ReviewProgressReworkStarted, Message: "Review findings were passed to the author."}
	progress := []ReviewProgressEvent{started}
	if err := e.recordAttempt(started.Message); err != nil {
		return ReviewResult{}, err
	}
	if err := e.publishStatus(ReviewAutomaticRework); err != nil {
		return ReviewResult{}, err
	}
	rework, err := author.ReworkFromReview(ReviewReworkRequest{ReportPath: e.currentReportPath(), Findings: scope})
	if err != nil {
		return ReviewResult{}, fmt.Errorf("apply %s review through author: %w", e.run.request.Stage, err)
	}
	if rework.Outcome == StageReviewDecisionRequired {
		if err := e.publishStatus(ReviewAuthorDialogue); err != nil {
			return ReviewResult{}, err
		}
		result := e.baseResult(ReviewAuthorDecision, ReviewAuthorDialogue, rework.Message)
		result.Progress = progress
		result.Findings = cloneFindingSnapshots(e.run.candidateFindings)
		result.ReworkAttempts = e.run.reworkAttempts
		return e.checkpointResultAs(result, CheckpointReviewRework, mustPolicy(e.run.request.Stage).AuthorRole)
	}
	if rework.Outcome != StageReviewReworkPublished {
		return ReviewResult{}, fmt.Errorf("review author returned unexpected outcome %q", rework.Outcome)
	}
	e.feature = rework.Feature
	progress = append(progress, ReviewProgressEvent{Kind: ReviewProgressDiff, Message: "Author published the review rework.", Diff: rework.Diff})
	result := e.baseResult(ReviewReportCompleted, ReviewNotStarted, "The author published the agreed review changes. Start /review explicitly to check the new revision.")
	result.Feature = e.feature
	result.Findings = cloneFindingSnapshots(e.run.candidateFindings)
	result.Progress = progress
	result.ReworkAttempts = e.run.reworkAttempts
	e.terminal = true
	return result, nil
}

func (e *ReviewEngine) publishStatus(status ReviewStatus) error {
	accepted := e.run.turnFingerprint
	publication, err := e.repository.PublishReview(ReviewArtifactRequest{
		FeatureID: e.run.request.FeatureID, Stage: e.run.request.Stage, ArtifactRoot: e.artifactRoot,
		RunID: e.run.runID, Status: status, Original: e.run.original, Accepted: &accepted,
		Attempts: e.run.reworkAttempts, ActiveIDs: reviewTargetIDs(e.feature, e.run.request.Stage),
		PreviousFindings: e.run.previousFindings, Provider: e.run.request.Provider, Model: e.run.request.Model,
		CreatedAt: e.run.createdAt, UpdatedAt: e.now(),
	})
	if err != nil {
		return fmt.Errorf("publish %s review status %s: %w", e.run.request.Stage, status, err)
	}
	if !publication.Published {
		return fmt.Errorf("publish %s review status %s: report became invalid: %s", e.run.request.Stage, status, conciseDocumentDiagnostics(publication.Validation.Diagnostics))
	}
	e.feature = publication.Feature
	e.run.runID = publication.RunID
	return nil
}

func (e *ReviewEngine) recordAttempt(message string) error {
	entry, err := NewMemLogEntry(e.run.request.Stage, e.run.role, MemLogAttempt, e.now(), message)
	if err != nil {
		return err
	}
	feature, err := e.repository.RecordActivity(e.run.request.FeatureID, entry)
	if err != nil {
		return fmt.Errorf("record automatic review attempt: %w", err)
	}
	e.feature = feature
	return nil
}

func (e *ReviewEngine) currentReportPath() string {
	return filepath.Join(e.feature.Target.Directory, reviewRelativePath(e.run.request.Stage, e.run.runID))
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
		e.run.reviewerTurns++
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
		Attempts: e.run.reworkAttempts, ActiveIDs: reviewTargetIDs(e.feature, e.run.request.Stage),
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
		Attempts: e.run.reworkAttempts, ActiveIDs: reviewTargetIDs(e.feature, e.run.request.Stage),
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
	result.ReworkAttempts = e.run.reworkAttempts
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
		result.ReworkAttempts = e.run.reworkAttempts
	}
	return result
}

func (e *ReviewEngine) checkpointPublishedReview(result ReviewResult) (ReviewResult, error) {
	switch result.Outcome {
	case ReviewReportCompleted, ReviewMaterialDecisions, ReviewReworkRequired:
		return e.checkpointResult(result, CheckpointReview)
	default:
		return result, nil
	}
}

func (e *ReviewEngine) checkpointResult(result ReviewResult, kind CheckpointKind) (ReviewResult, error) {
	return e.checkpointResultAs(result, kind, e.run.role)
}

func (e *ReviewEngine) checkpointResultAs(result ReviewResult, kind CheckpointKind, role Role) (ReviewResult, error) {
	if e.run == nil {
		return ReviewResult{}, ErrReviewNotStarted
	}
	feature, err := e.repository.Checkpoint(CheckpointRequest{
		FeatureID: e.run.request.FeatureID,
		Stage:     e.run.request.Stage,
		Role:      role,
		Kind:      kind,
		At:        e.now(),
	})
	if err != nil {
		return ReviewResult{}, fmt.Errorf("checkpoint %s %s: %w", e.run.request.Stage, kind, err)
	}
	e.feature = feature
	result.Feature = feature
	return result, nil
}

func (e *ReviewEngine) Close() error {
	if e.thread == nil && e.artifactRoot == "" {
		e.reset()
		return nil
	}
	if e.registry != nil && e.run != nil {
		releaseErr := e.registry.Release(e.run.request.FeatureID, e.run.role)
		e.reset()
		return releaseErr
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
	e.registry = nil
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

func openFixFindings(findings []FindingSnapshot) []FindingSnapshot {
	result := make([]FindingSnapshot, 0)
	for _, finding := range findings {
		if finding.Status == FindingOpen && finding.Decision.Decision == DecisionFix {
			result = append(result, cloneFindingSnapshot(finding))
		}
	}
	return result
}

func findingIndex(findings []FindingSnapshot, id StableID) int {
	for i := range findings {
		if findings[i].ID == id {
			return i
		}
	}
	return -1
}

func rewriteFindingDecision(data []byte, requested MaterialFindingDecision) ([]byte, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		fields := strings.Fields(heading)
		if len(fields) == 0 {
			continue
		}
		id, err := ParseStableID(fields[0])
		if err != nil || (id.Family() != IDSpecFinding && id.Family() != IDPlanFinding) {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		if id == requested.FindingID {
			start = i
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("review finding %s is not present in review.md", requested.FindingID)
	}
	values := map[string]string{
		"Decision":   string(requested.Decision),
		"Decided-by": string(DecidedByUser),
		"Rationale":  strings.TrimSpace(requested.Rationale),
	}
	if requested.Decision == DecisionDismiss {
		values["Status"] = string(FindingDismissed)
	}
	for name, value := range values {
		found := false
		prefix := strings.ToLower(name) + ":"
		for i := start + 1; i < end; i++ {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(lines[i])), prefix) {
				lines[i] = name + ": " + value
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("review finding %s has no %s field", requested.FindingID, name)
		}
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func conciseOpenFindings(findings []FindingSnapshot) string {
	open := openFixFindings(findings)
	if len(open) == 0 {
		return "Automatic review rework exhausted without a verified resolution."
	}
	var result strings.Builder
	result.WriteString("Automatic review rework exhausted; unresolved findings:")
	for _, finding := range open {
		fmt.Fprintf(&result, "\n- %s: %s", finding.ID, finding.Problem)
	}
	return result.String()
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
