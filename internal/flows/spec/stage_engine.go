package specflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

var (
	ErrAuthorSessionNotStarted = errors.New("author session is not started")
	ErrRevisionDecisionPending = errors.New("author revision decision is pending")
)

type RevisionAction string

const (
	RevisionApply  RevisionAction = "apply"
	RevisionReject RevisionAction = "reject"
	RevisionRework RevisionAction = "rework"
)

func (a RevisionAction) Valid() bool {
	return a == RevisionApply || a == RevisionReject || a == RevisionRework
}

type StageOutcome string

const (
	StageAuthorMessage          StageOutcome = "author_message"
	StageDraftPublished         StageOutcome = "draft_published"
	StageRevisionPending        StageOutcome = "revision_pending"
	StageRevisionApplied        StageOutcome = "revision_applied"
	StageRevisionRejected       StageOutcome = "revision_rejected"
	StageAuthorDiagnostics      StageOutcome = "author_diagnostics"
	StageExternalChangeRead     StageOutcome = "external_change_read"
	StageReviewReworkPublished  StageOutcome = "review_rework_published"
	StageReviewDecisionRequired StageOutcome = "review_decision_required"
)

// StageResult is the author engine's event/effect boundary. FeatureController
// may use Feature as its new durable snapshot; StageEngine never mutates a
// FlowState value itself.
type StageResult struct {
	Stage           Stage
	Outcome         StageOutcome
	Message         string
	Diff            string
	Diagnostics     []DocumentDiagnostic
	Decisions       []Decision
	RevisionActions []RevisionAction
	Feature         FeatureSnapshot
}

type StartStageRequest struct {
	FeatureID      string
	Stage          Stage
	RuntimeContext string
}

// ReviewReworkRequest is deliberately narrower than a normal author message.
// The author receives the current report path and the already-agreed fix scope;
// reviewer conversation and dismissed findings are not part of this contract.
type ReviewReworkRequest struct {
	ReportPath string
	Findings   []FindingSnapshot
}

type pendingAuthorRevision struct {
	hash string
	diff string
}

// StageEngine runs one live author thread at a time. The same implementation
// serves intent, spec, and plan; all stage differences come from StagePolicy.
type StageEngine struct {
	workspace  string
	runner     dialogueRunner
	repository FeatureRepository
	catalog    PromptCatalog
	now        func() time.Time
	newRoot    func(string) (string, error)

	featureID     string
	policy        StagePolicy
	thread        agentruntime.Thread
	artifactRoot  string
	pending       *pendingAuthorRevision
	parserAttempt int
	workingIDs    []StableID
	feature       FeatureSnapshot
	reviewRework  *ReviewReworkRequest
	registry      *SessionRegistry
	resumed       bool
	resumeContext string
}

func NewStageEngine(workspace string, runner dialogueRunner, repository FeatureRepository, catalog PromptCatalog) (*StageEngine, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, fmt.Errorf("create stage engine: workspace is required")
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("create stage engine: %w", err)
	}
	if runner == nil || repository == nil || catalog == nil {
		return nil, fmt.Errorf("create stage engine: runner, repository, and prompt catalog are required")
	}
	engine := &StageEngine{
		workspace: absolute, runner: runner, repository: repository, catalog: catalog,
		now: time.Now, newRoot: CreateArtifactRoot,
	}
	engine.registry, _ = runner.(*SessionRegistry)
	return engine, nil
}

func (e *StageEngine) Policy() (StagePolicy, bool) {
	if e.thread == nil {
		return StagePolicy{}, false
	}
	return e.policy.clone(), true
}

func (e *StageEngine) Start(request StartStageRequest) (StagePolicy, error) {
	if e.thread != nil {
		return StagePolicy{}, fmt.Errorf("start author session: a session is already active")
	}
	if strings.TrimSpace(request.FeatureID) == "" {
		return StagePolicy{}, fmt.Errorf("start author session: feature ID is required")
	}
	policy, err := PolicyForStage(request.Stage)
	if err != nil {
		return StagePolicy{}, err
	}
	feature, err := e.repository.Load(request.FeatureID)
	if err != nil {
		return StagePolicy{}, fmt.Errorf("start %s author: %w", request.Stage, err)
	}
	if err := validateAuthorStageGate(feature.State, policy); err != nil {
		return StagePolicy{}, err
	}
	artifactRoot, err := e.newRoot(e.workspace)
	if err != nil {
		return StagePolicy{}, fmt.Errorf("create %s artifact root: %w", request.Stage, err)
	}
	registry, managed := e.registry, e.registry != nil
	actualRoot := artifactRoot
	var thread agentruntime.Thread
	var resumed bool
	if managed {
		runtimeContext := authorRuntimeContext(feature, policy, artifactRoot, request.RuntimeContext)
		prompt, composeErr := e.catalog.Compose(policy.AuthorRole, runtimeContext)
		if composeErr != nil {
			_ = removeArtifact(artifactRoot)
			return StagePolicy{}, fmt.Errorf("compose %s author prompt: %w", request.Stage, composeErr)
		}
		registered, acquireErr := registry.Acquire(SessionRequest{FeatureID: request.FeatureID, Role: policy.AuthorRole, Config: agentruntime.ThreadConfig{
			BootstrapInstructions: prompt, OutputSchema: DialogueSchema(), Workspace: e.workspace, ArtifactRoot: artifactRoot,
		}})
		if acquireErr != nil {
			_ = removeArtifact(artifactRoot)
			return StagePolicy{}, fmt.Errorf("start %s author thread: %w", request.Stage, acquireErr)
		}
		thread, actualRoot, resumed = registered.Thread, registered.ArtifactRoot, registered.Reused
	} else {
		runtimeContext := authorRuntimeContext(feature, policy, artifactRoot, request.RuntimeContext)
		prompt, composeErr := e.catalog.Compose(policy.AuthorRole, runtimeContext)
		if composeErr != nil {
			_ = removeArtifact(artifactRoot)
			return StagePolicy{}, fmt.Errorf("compose %s author prompt: %w", request.Stage, composeErr)
		}
		thread, err = e.runner.StartThread(agentruntime.ThreadConfig{
			BootstrapInstructions: prompt, OutputSchema: DialogueSchema(), Workspace: e.workspace, ArtifactRoot: artifactRoot,
		})
	}
	if err != nil {
		_ = removeArtifact(artifactRoot)
		return StagePolicy{}, fmt.Errorf("start %s author thread: %w", request.Stage, err)
	}
	e.featureID = request.FeatureID
	e.policy = policy
	e.thread = thread
	e.artifactRoot = actualRoot
	e.feature = feature
	e.registry = registry
	e.resumed = resumed
	if resumed {
		e.resumeContext = authorRuntimeContext(feature, policy, actualRoot, request.RuntimeContext)
	}
	e.pending = nil
	e.parserAttempt = 0
	e.workingIDs = nil
	e.reviewRework = nil
	return policy.clone(), nil
}

func validateAuthorStageGate(state FlowState, policy StagePolicy) error {
	for _, upstream := range policy.UpstreamStages {
		upstreamState, ok := state.Stage(upstream)
		if !ok || upstreamState.Status != StageCommitted {
			return fmt.Errorf("start %s author: %s must be committed", policy.Stage, upstream)
		}
	}
	return nil
}

func authorRuntimeContext(feature FeatureSnapshot, policy StagePolicy, artifactRoot, extra string) string {
	var context strings.Builder
	fmt.Fprintf(&context, "Feature: %s\nStage: %s\n", feature.Target.ID, policy.Stage)
	fmt.Fprintf(&context, "Writable artifact root: %s\nFixed artifact filename: %s\n", artifactRoot, policy.ArtifactFilename)
	fmt.Fprintf(&context, "Memory log (read-only): %s\n", feature.Target.JournalPath)
	if current, ok := feature.Documents[policy.Stage]; ok {
		fmt.Fprintf(&context, "Current %s document (read-only): %s\n", policy.Stage, current.Path)
	} else {
		fmt.Fprintf(&context, "Current %s document: not published\n", policy.Stage)
	}
	context.WriteString("Required upstream documents (read-only):")
	if len(policy.UpstreamStages) == 0 {
		context.WriteString(" none\n")
	} else {
		context.WriteByte('\n')
		for _, stage := range policy.UpstreamStages {
			document, ok := feature.Documents[stage]
			if ok {
				fmt.Fprintf(&context, "- %s: %s (sha256 %s)\n", stage, document.Path, document.Hash)
			} else {
				fmt.Fprintf(&context, "- %s: missing\n", stage)
			}
		}
	}
	context.WriteString("Current and previous review reports (read-only):")
	reviewCount := 0
	for _, review := range feature.Reviews {
		if review.Stage != policy.Stage {
			continue
		}
		fmt.Fprintf(&context, "\n- %s", review.Path)
		reviewCount++
	}
	if reviewCount == 0 {
		context.WriteString(" none")
	}
	context.WriteByte('\n')
	if strings.TrimSpace(extra) != "" {
		context.WriteString("\nSession context:\n")
		context.WriteString(strings.TrimSpace(extra))
		context.WriteByte('\n')
	}
	return strings.TrimSpace(context.String())
}

// Submit continues the live author dialogue. A user turn starts a fresh parser
// repair budget; automatic repair turns do not reset it.
func (e *StageEngine) Submit(message string) (StageResult, error) {
	if err := e.ready(); err != nil {
		return StageResult{}, err
	}
	if e.pending != nil {
		return StageResult{}, ErrRevisionDecisionPending
	}
	if strings.TrimSpace(message) == "" {
		return StageResult{}, fmt.Errorf("author message must not be empty")
	}
	e.parserAttempt = 0
	if err := e.record(MemLogUserMessage, message); err != nil {
		return StageResult{}, err
	}
	if e.reviewRework != nil {
		result, err := e.runMode(message, false, true)
		if err != nil {
			return StageResult{}, err
		}
		if result.Outcome == StageReviewDecisionRequired {
			if err := e.checkpoint(CheckpointReviewRework); err != nil {
				return StageResult{}, err
			}
			result.Feature = e.feature
		}
		return result, nil
	}
	return e.run(message, false)
}

// SubmitBrief starts the intent dialogue with the feature brief already stored
// as the first mem-log entry by repository creation. Unlike Submit, it must not
// append the same user text to the journal a second time.
func (e *StageEngine) SubmitBrief(brief string) (StageResult, error) {
	if err := e.ready(); err != nil {
		return StageResult{}, err
	}
	if e.policy.Stage != StageIntent {
		return StageResult{}, fmt.Errorf("feature brief is only valid for the intent author")
	}
	if e.pending != nil {
		return StageResult{}, ErrRevisionDecisionPending
	}
	if strings.TrimSpace(brief) == "" {
		return StageResult{}, fmt.Errorf("feature brief must not be empty")
	}
	e.parserAttempt = 0
	return e.run(brief, false)
}

// BeginStageDialogue gives a newly opened downstream author its first turn.
// The prompt is control-plane input rather than a user message, so it is not
// recorded in the feature memory log.
func (e *StageEngine) BeginStageDialogue() (StageResult, error) {
	if err := e.ready(); err != nil {
		return StageResult{}, err
	}
	if e.policy.Stage == StageIntent {
		return StageResult{}, fmt.Errorf("intent dialogue must begin with the feature brief")
	}
	if e.pending != nil {
		return StageResult{}, ErrRevisionDecisionPending
	}
	e.parserAttempt = 0
	prompt := fmt.Sprintf("Begin the %s stage now. Read the approved upstream documents and identify material ambiguities before drafting. Ask the user the next material clarification question or a small related set. If no material ambiguity remains, write the complete %s artifact.", e.policy.Stage, e.policy.ArtifactFilename)
	return e.run(prompt, false)
}

// ResumeStageDialogue gives a restored drafting stage an active first turn.
// Durable documents and the memory log are authoritative; the control-plane
// continuation prompt is not recorded as a user message.
func (e *StageEngine) ResumeStageDialogue() (StageResult, error) {
	if err := e.ready(); err != nil {
		return StageResult{}, err
	}
	if e.pending != nil {
		return StageResult{}, ErrRevisionDecisionPending
	}
	e.parserAttempt = 0
	prompt := fmt.Sprintf("Resume the current %s dialogue from the authoritative documents and memory log. Ask the user the next unresolved material clarification question or a small related set. If no material ambiguity remains, write the complete %s artifact.", e.policy.Stage, e.policy.ArtifactFilename)
	return e.run(prompt, false)
}

func (e *StageEngine) run(prompt string, external bool) (StageResult, error) {
	return e.runMode(prompt, external, false)
}

func (e *StageEngine) runMode(prompt string, external, reviewRework bool) (StageResult, error) {
	if e.resumed && e.resumeContext != "" {
		prompt = "Current durable session context (authoritative documents and mem-log override conversation memory):\n" + e.resumeContext + "\n\nContinue with this turn:\n" + prompt
		e.resumed = false
		e.resumeContext = ""
	}
	for {
		raw, err := e.runner.RunTurn(e.thread, prompt)
		if err != nil {
			return StageResult{}, fmt.Errorf("run %s author turn: %w", e.policy.Stage, err)
		}
		envelope, err := DecodeEnvelope(raw)
		if err != nil {
			return StageResult{}, fmt.Errorf("decode %s author response: %w", e.policy.Stage, err)
		}
		if !external && !reviewRework {
			if err := e.recordDecisions(envelope.Decisions); err != nil {
				return StageResult{}, err
			}
		}
		if envelope.Kind == KindMessage {
			if !external && !reviewRework {
				if err := e.record(MemLogAgentMessage, envelope.Message); err != nil {
					return StageResult{}, err
				}
			}
			outcome := StageAuthorMessage
			if external {
				outcome = StageExternalChangeRead
			} else if reviewRework {
				outcome = StageReviewDecisionRequired
			}
			return e.result(outcome, envelope.Message, "", nil, envelope.Decisions), nil
		}

		inspection, err := e.inspectDraft()
		if err != nil {
			return StageResult{}, err
		}
		e.workingIDs = observedStableIDs(inspection.Validation.ObservedIDs)
		if inspection.Validation.Valid() {
			if reviewRework {
				if len(envelope.Decisions) > 0 {
					return e.result(StageReviewDecisionRequired, "The author reported a material decision during automatic review rework.", "", nil, envelope.Decisions), nil
				}
				return e.publishReviewRework(inspection)
			}
			return e.acceptValidDraft(inspection, envelope.Decisions)
		}
		e.parserAttempt++
		if e.parserAttempt >= DefaultRetryLimit {
			message := conciseDocumentDiagnostics(inspection.Validation.Diagnostics)
			if err := e.record(MemLogError, "parser repair exhausted: "+message); err != nil {
				return StageResult{}, err
			}
			return e.result(StageAuthorDiagnostics, message, "", inspection.Validation.Diagnostics, envelope.Decisions), nil
		}
		prompt = parserRepairPrompt(e.policy.ArtifactFilename, inspection.Validation.Diagnostics)
		external = false
	}
}

// ReworkFromReview runs the current author session in review-scoped mode. A
// valid artifact is published immediately and its diff is informational: no
// pending revision decision is created.
func (e *StageEngine) ReworkFromReview(request ReviewReworkRequest) (StageResult, error) {
	if err := e.ready(); err != nil {
		return StageResult{}, err
	}
	if e.pending != nil {
		return StageResult{}, ErrRevisionDecisionPending
	}
	reportPath := strings.TrimSpace(request.ReportPath)
	if reportPath == "" {
		return StageResult{}, fmt.Errorf("review rework report path must not be empty")
	}
	feature, err := e.repository.Load(e.featureID)
	if err != nil {
		return StageResult{}, fmt.Errorf("reload feature for review rework: %w", err)
	}
	e.feature = feature
	latestRun, latestPath := uint64(0), ""
	stageState, _ := feature.State.Stage(e.policy.Stage)
	for _, report := range stageState.Reviews {
		if report.ID > latestRun {
			latestRun = report.ID
			latestPath = filepath.Join(feature.Target.Directory, filepath.FromSlash(report.Path))
		}
	}
	requestedPath, err := filepath.Abs(reportPath)
	if err != nil {
		return StageResult{}, fmt.Errorf("resolve review rework report path: %w", err)
	}
	if latestPath == "" || !strings.EqualFold(filepath.Clean(requestedPath), filepath.Clean(latestPath)) {
		return StageResult{}, fmt.Errorf("review rework requires the current %s review report", e.policy.Stage)
	}
	if len(request.Findings) == 0 {
		return StageResult{}, fmt.Errorf("review rework scope must contain at least one finding")
	}
	ids := make([]string, 0, len(request.Findings))
	seen := make(map[StableID]struct{}, len(request.Findings))
	for _, finding := range request.Findings {
		if !finding.ID.Valid() || finding.Status != FindingOpen || finding.Decision.Decision != DecisionFix ||
			(finding.Kind == FindingMaterial && finding.Decision.DecidedBy != DecidedByUser) ||
			(finding.Kind == FindingContractViolation && finding.Decision.DecidedBy != DecidedByReviewer) {
			return StageResult{}, fmt.Errorf("review rework scope contains finding %q without an agreed open fix", finding.ID)
		}
		if _, duplicate := seen[finding.ID]; duplicate {
			return StageResult{}, fmt.Errorf("review rework scope contains duplicate finding %s", finding.ID)
		}
		seen[finding.ID] = struct{}{}
		ids = append(ids, finding.ID.String())
	}
	current, ok := e.feature.Documents[e.policy.Stage]
	if !ok {
		return StageResult{}, fmt.Errorf("review rework requires a published %s document", e.policy.Stage)
	}
	e.parserAttempt = 0
	prompt := fmt.Sprintf("Perform review-scoped rework of the current %s document. Read the current review report at %s. Apply only the agreed open fix findings listed below; do not apply dismissed findings or use reviewer conversation as input. The listed fixes are already user decisions: do not repeat them in the decisions field. Use decisions only for a genuinely new material choice. If such a choice is required, ask the user directly and return a message instead of an artifact. Otherwise overwrite %s with the complete corrected document and return an artifact envelope.\n\nAgreed finding IDs:\n- %s\n\nCurrent revision: %s",
		e.policy.Stage, reportPath, e.policy.ArtifactFilename, strings.Join(ids, "\n- "), current.Hash)
	scope := request
	scope.Findings = cloneFindingSnapshots(request.Findings)
	e.reviewRework = &scope
	result, err := e.runMode(prompt, false, true)
	if err != nil {
		return StageResult{}, err
	}
	return result, nil
}

func (e *StageEngine) publishReviewRework(inspection DraftInspection) (StageResult, error) {
	current, ok := e.feature.Documents[e.policy.Stage]
	if !ok {
		return StageResult{}, fmt.Errorf("publish review rework: no current %s document", e.policy.Stage)
	}
	pendingBytes, err := os.ReadFile(filepath.Join(e.artifactRoot, e.policy.ArtifactFilename))
	if err != nil {
		return StageResult{}, fmt.Errorf("read review rework %s artifact: %w", e.policy.Stage, err)
	}
	diff := unifiedArtifactDiff(e.policy.ArtifactFilename, current.Content, pendingBytes)
	request := e.draftRequest()
	request.ExpectedHash = inspection.Hash
	publication, err := e.repository.PublishAuthorDraft(request)
	if err != nil {
		return StageResult{}, fmt.Errorf("publish automatic %s review rework: %w", e.policy.Stage, err)
	}
	if !publication.Published {
		return StageResult{}, fmt.Errorf("publish automatic %s review rework: draft became invalid", e.policy.Stage)
	}
	e.feature = publication.Feature
	e.reviewRework = nil
	if err := e.record(MemLogDiff, "informational automatic review rework diff:\n"+diff); err != nil {
		return StageResult{}, err
	}
	if err := e.checkpoint(CheckpointReviewRework); err != nil {
		return StageResult{}, err
	}
	e.parserAttempt = 0
	e.workingIDs = nil
	return e.result(StageReviewReworkPublished, "", diff, nil, nil), nil
}

func (e *StageEngine) inspectDraft() (DraftInspection, error) {
	request := e.draftRequest()
	inspection, err := e.repository.InspectAuthorDraft(request)
	if err != nil {
		return DraftInspection{}, fmt.Errorf("inspect %s author draft: %w", e.policy.Stage, err)
	}
	return inspection, nil
}

func (e *StageEngine) draftRequest() DraftArtifactRequest {
	return DraftArtifactRequest{
		FeatureID: e.featureID, Stage: e.policy.Stage, ArtifactRoot: e.artifactRoot,
		Mode: e.policy.ParserMode, ActiveIDs: activeDocumentIDs(e.feature, e.policy.UpstreamStages),
		RetainedIDs:    append(retainedDocumentIDs(e.feature, e.policy.Stage), e.workingIDs...),
		UpstreamHashes: upstreamDocumentHashes(e.feature, e.policy.UpstreamStages),
		At:             e.now(),
	}
}

func activeDocumentIDs(feature FeatureSnapshot, stages []Stage) []StableID {
	var ids []StableID
	for _, stage := range stages {
		document, ok := feature.Documents[stage]
		if !ok {
			continue
		}
		parsed := ParseDocument(DocumentRequest{Kind: documentKindForStage(stage), Mode: ValidateDraft, Markdown: string(document.Content)})
		ids = append(ids, observedStableIDs(parsed.ObservedIDs)...)
	}
	return ids
}

func retainedDocumentIDs(feature FeatureSnapshot, stage Stage) []StableID {
	document, ok := feature.Documents[stage]
	if !ok {
		return nil
	}
	parsed := ParseDocument(DocumentRequest{Kind: documentKindForStage(stage), Mode: ValidateDraft, Markdown: string(document.Content)})
	return observedStableIDs(parsed.ObservedIDs)
}

func upstreamDocumentHashes(feature FeatureSnapshot, stages []Stage) []UpstreamHash {
	result := make([]UpstreamHash, 0, len(stages))
	for _, stage := range stages {
		if document, ok := feature.Documents[stage]; ok {
			result = append(result, UpstreamHash{Stage: stage, Hash: document.Hash})
		}
	}
	return result
}

func (e *StageEngine) acceptValidDraft(inspection DraftInspection, decisions []Decision) (StageResult, error) {
	current, published := e.feature.Documents[e.policy.Stage]
	if !published {
		request := e.draftRequest()
		request.ExpectedHash = inspection.Hash
		publication, err := e.repository.PublishAuthorDraft(request)
		if err != nil {
			return StageResult{}, fmt.Errorf("publish first %s draft: %w", e.policy.Stage, err)
		}
		if !publication.Published {
			return StageResult{}, fmt.Errorf("publish first %s draft: draft became invalid", e.policy.Stage)
		}
		e.feature = publication.Feature
		if err := e.checkpoint(CheckpointAuthorDraft); err != nil {
			return StageResult{}, err
		}
		e.parserAttempt = 0
		e.workingIDs = nil
		return e.result(StageDraftPublished, "", "", nil, decisions), nil
	}
	pendingBytes, err := os.ReadFile(filepath.Join(e.artifactRoot, e.policy.ArtifactFilename))
	if err != nil {
		return StageResult{}, fmt.Errorf("read pending %s revision: %w", e.policy.Stage, err)
	}
	diff := unifiedArtifactDiff(e.policy.ArtifactFilename, current.Content, pendingBytes)
	e.pending = &pendingAuthorRevision{hash: inspection.Hash, diff: diff}
	if err := e.record(MemLogDiff, diff); err != nil {
		e.pending = nil
		return StageResult{}, err
	}
	result := e.result(StageRevisionPending, "", diff, nil, decisions)
	result.RevisionActions = []RevisionAction{RevisionApply, RevisionReject, RevisionRework}
	return result, nil
}

func (e *StageEngine) Decide(action RevisionAction, reworkScope string) (StageResult, error) {
	if err := e.ready(); err != nil {
		return StageResult{}, err
	}
	if e.pending == nil {
		return StageResult{}, fmt.Errorf("no author revision is awaiting a decision")
	}
	if !action.Valid() {
		return StageResult{}, fmt.Errorf("unknown revision action %q", action)
	}
	switch action {
	case RevisionApply:
		request := e.draftRequest()
		request.ExpectedHash = e.pending.hash
		publication, err := e.repository.PublishAuthorDraft(request)
		if err != nil {
			return StageResult{}, fmt.Errorf("apply %s revision: %w", e.policy.Stage, err)
		}
		if !publication.Published {
			return StageResult{}, fmt.Errorf("apply %s revision: draft became invalid", e.policy.Stage)
		}
		e.feature = publication.Feature
		if err := e.record(MemLogRevisionDecision, "revision applied: "+e.pending.hash); err != nil {
			return StageResult{}, err
		}
		if err := e.checkpoint(CheckpointAuthorDraft); err != nil {
			return StageResult{}, err
		}
		e.pending = nil
		e.parserAttempt = 0
		e.workingIDs = nil
		return e.result(StageRevisionApplied, "", "", nil, nil), nil
	case RevisionReject:
		if err := e.record(MemLogRevisionDecision, "revision rejected: "+e.pending.hash); err != nil {
			return StageResult{}, err
		}
		e.pending = nil
		e.workingIDs = nil
		return e.result(StageRevisionRejected, "", "", nil, nil), nil
	case RevisionRework:
		if strings.TrimSpace(reworkScope) == "" {
			return StageResult{}, fmt.Errorf("rework scope must not be empty")
		}
		if err := e.record(MemLogRevisionDecision, "revision rework requested: "+reworkScope); err != nil {
			return StageResult{}, err
		}
		e.pending = nil
		e.parserAttempt = 0
		return e.run("Rework the pending revision using only this user-provided scope. Do not make material choices beyond it:\n\n"+strings.TrimSpace(reworkScope), false)
	}
	panic("unreachable")
}

func (e *StageEngine) checkpoint(kind CheckpointKind) error {
	feature, err := e.repository.Checkpoint(CheckpointRequest{
		FeatureID: e.featureID,
		Stage:     e.policy.Stage,
		Role:      e.policy.AuthorRole,
		Kind:      kind,
		At:        e.now(),
	})
	if err != nil {
		return fmt.Errorf("checkpoint %s %s: %w", e.policy.Stage, kind, err)
	}
	e.feature = feature
	return nil
}

// ReReadCurrentDocument makes the existing author thread acknowledge a manual
// project-document change. Its response is returned to FeatureController,
// which owns the stage-specific transition that follows.
func (e *StageEngine) ReReadCurrentDocument() (StageResult, error) {
	if err := e.ready(); err != nil {
		return StageResult{}, err
	}
	if e.pending != nil {
		return StageResult{}, ErrRevisionDecisionPending
	}
	feature, err := e.repository.Load(e.featureID)
	if err != nil {
		return StageResult{}, fmt.Errorf("reload externally changed feature: %w", err)
	}
	e.feature = feature
	document, ok := feature.Documents[e.policy.Stage]
	if !ok {
		return StageResult{}, fmt.Errorf("re-read %s document: no published document", e.policy.Stage)
	}
	e.parserAttempt = 0
	prompt := fmt.Sprintf("Re-read the externally changed current %s document at %s. Report the stage-specific impact without making material decisions for the user.", e.policy.Stage, document.Path)
	return e.run(prompt, true)
}

func (e *StageEngine) Close() error {
	if e.thread == nil {
		return nil
	}
	if e.registry != nil {
		releaseErr := e.registry.Release(e.featureID, e.policy.AuthorRole)
		e.reset()
		return releaseErr
	}
	closeErr := e.runner.CloseThread(e.thread)
	_, discardErr := e.repository.DiscardPending(e.featureID, e.policy.Stage, e.artifactRoot)
	var cleanupErr error
	if discardErr != nil {
		// External document changes can deliberately block repository mutations.
		// The artifact root is still session-owned and must not leak when the
		// controller switches to the author responsible for that revision.
		cleanupErr = removeArtifact(e.artifactRoot)
	}
	e.reset()
	return errors.Join(closeErr, discardErr, cleanupErr)
}

func (e *StageEngine) CloseFeatureSessions(featureID string) error {
	if e.registry == nil {
		return nil
	}
	return e.registry.CloseFeature(featureID)
}

func (e *StageEngine) ready() error {
	if e.thread == nil {
		return ErrAuthorSessionNotStarted
	}
	return nil
}

func (e *StageEngine) reset() {
	e.featureID = ""
	e.policy = StagePolicy{}
	e.thread = nil
	e.artifactRoot = ""
	e.pending = nil
	e.parserAttempt = 0
	e.workingIDs = nil
	e.feature = FeatureSnapshot{}
	e.resumed = false
	e.resumeContext = ""
}

func (e *StageEngine) record(kind MemLogEventKind, body string) error {
	entry, err := NewMemLogEntry(e.policy.Stage, e.policy.AuthorRole, kind, e.now(), body)
	if err != nil {
		return err
	}
	feature, err := e.repository.RecordActivity(e.featureID, entry)
	if err != nil {
		return fmt.Errorf("record %s author activity: %w", e.policy.Stage, err)
	}
	e.feature = feature
	return nil
}

func (e *StageEngine) recordDecisions(decisions []Decision) error {
	for _, decision := range decisions {
		feature, err := e.repository.RecordDecision(e.featureID, e.policy.Stage, e.policy.AuthorRole, decision)
		if err != nil {
			return fmt.Errorf("record %s author decision: %w", e.policy.Stage, err)
		}
		e.feature = feature
	}
	return nil
}

func (e *StageEngine) result(outcome StageOutcome, message, diff string, diagnostics []DocumentDiagnostic, decisions []Decision) StageResult {
	return StageResult{
		Stage: e.policy.Stage, Outcome: outcome, Message: message, Diff: diff,
		Diagnostics: append([]DocumentDiagnostic(nil), diagnostics...),
		Decisions:   append([]Decision(nil), decisions...), Feature: e.feature,
	}
}

func parserRepairPrompt(filename string, diagnostics []DocumentDiagnostic) string {
	return fmt.Sprintf("The %s artifact failed deterministic structural validation. Correct only the listed contract errors, overwrite the same fixed artifact, and return an artifact envelope. Do not invent material decisions.\n\n%s", filename, conciseDocumentDiagnostics(diagnostics))
}

func conciseDocumentDiagnostics(diagnostics []DocumentDiagnostic) string {
	if len(diagnostics) == 0 {
		return "No document diagnostics were reported."
	}
	const limit = 6
	var result strings.Builder
	result.WriteString("Document diagnostics:")
	for i, diagnostic := range diagnostics {
		if i == limit {
			fmt.Fprintf(&result, "\n- and %d more diagnostic(s)", len(diagnostics)-limit)
			break
		}
		location := ""
		if diagnostic.Line > 0 {
			location = fmt.Sprintf(" line %d", diagnostic.Line)
		}
		fmt.Fprintf(&result, "\n- [%s]%s %s", diagnostic.Code, location, diagnostic.Message)
	}
	return result.String()
}

func unifiedArtifactDiff(filename string, old, new []byte) string {
	diff := unifiedDiff(old, new)
	diff = strings.Replace(diff, "--- intent.md\n+++ intent.md\n", "--- "+filename+"\n+++ "+filename+"\n", 1)
	return diff
}
