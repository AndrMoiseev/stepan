package specflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

type dialogueRunner interface {
	StartThread(agentruntime.ThreadConfig) (agentruntime.Thread, error)
	RunTurn(agentruntime.Thread, string) (json.RawMessage, error)
	CloseThread(agentruntime.Thread) error
}

func withinPath(root, value string) bool {
	r, err := filepath.Rel(root, value)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
func removeArtifact(root string) error {
	if root == "" {
		return nil
	}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if filepath.Clean(canonical) != filepath.Clean(root) {
		return fmt.Errorf("refuse cleanup through link")
	}
	return os.RemoveAll(root)
}
func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func unifiedDiff(old, new []byte) string {
	before, after := diffLines(string(old)), diffLines(string(new))
	type operation struct {
		kind byte
		line string
	}
	// A compact LCS implementation is sufficient here: intent files are small,
	// and it gives users actual hunks with unchanged context rather than a full
	// delete/add replacement.
	table := make([][]int, len(before)+1)
	for i := range table {
		table[i] = make([]int, len(after)+1)
	}
	for i := len(before) - 1; i >= 0; i-- {
		for j := len(after) - 1; j >= 0; j-- {
			if before[i] == after[j] {
				table[i][j] = 1 + table[i+1][j+1]
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	var ops []operation
	for i, j := 0, 0; i < len(before) || j < len(after); {
		switch {
		case i < len(before) && j < len(after) && before[i] == after[j]:
			ops = append(ops, operation{' ', before[i]})
			i++
			j++
		case j < len(after) && (i == len(before) || table[i][j+1] >= table[i+1][j]):
			ops = append(ops, operation{'+', after[j]})
			j++
		default:
			ops = append(ops, operation{'-', before[i]})
			i++
		}
	}
	var output strings.Builder
	output.WriteString("--- intent.md\n+++ intent.md\n")
	for start := 0; start < len(ops); {
		for start < len(ops) && ops[start].kind == ' ' {
			start++
		}
		if start == len(ops) {
			break
		}
		hunkStart := start - 3
		if hunkStart < 0 {
			hunkStart = 0
		}
		end := start + 1
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			contextEnd := end
			for contextEnd < len(ops) && ops[contextEnd].kind == ' ' {
				contextEnd++
			}
			if contextEnd-end > 6 {
				end += 3
				break
			}
			end = contextEnd
		}
		oldStart, newStart := 1, 1
		for _, op := range ops[:hunkStart] {
			if op.kind != '+' {
				oldStart++
			}
			if op.kind != '-' {
				newStart++
			}
		}
		oldCount, newCount := 0, 0
		for _, op := range ops[hunkStart:end] {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&output, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, op := range ops[hunkStart:end] {
			output.WriteByte(op.kind)
			output.WriteString(op.line)
			output.WriteByte('\n')
		}
		start = end
	}
	return output.String()
}

func diffLines(value string) []string {
	if value == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

// ControllerCommandKind is the provider-neutral command surface of the
// intent -> spec -> plan control plane. Terminal parsing is deliberately kept
// outside this package boundary.
type ControllerCommandKind string

const (
	ControllerAuthorMessage     ControllerCommandKind = "author_message"
	ControllerRevisionDecision  ControllerCommandKind = "revision_decision"
	ControllerStartReview       ControllerCommandKind = "review"
	ControllerReviewMessage     ControllerCommandKind = "review_message"
	ControllerReviewDecision    ControllerCommandKind = "review_decision"
	ControllerApplyReview       ControllerCommandKind = "apply_review"
	ControllerFingerprintChoice ControllerCommandKind = "review_fingerprint_decision"
	ControllerApprove           ControllerCommandKind = "approve"
	ControllerReviseSpec        ControllerCommandKind = "revise_spec"
	ControllerClassifyIntent    ControllerCommandKind = "classify_intent_revision"
	ControllerStatus            ControllerCommandKind = "status"
	ControllerClose             ControllerCommandKind = "close"
)

type ControllerCommand struct {
	Kind              ControllerCommandKind
	Message           string
	RevisionAction    RevisionAction
	ReworkScope       string
	ReviewDecision    MaterialFindingDecision
	FingerprintAction ReviewFingerprintAction
	Provider          string
	Model             string
	RuntimeContext    string
	IntentRevision    IntentRevisionClassification
	NewFeatureID      string
}

type IntentRevisionClassification string

const (
	IntentRevisionNonMaterial IntentRevisionClassification = "non_material"
	IntentRevisionMaterial    IntentRevisionClassification = "material"
)

type ControllerEvent string

const (
	ControllerStateShown      ControllerEvent = "state_shown"
	ControllerAuthorStarted   ControllerEvent = "author_started"
	ControllerAuthorUpdated   ControllerEvent = "author_updated"
	ControllerReviewUpdated   ControllerEvent = "review_updated"
	ControllerApprovalBlocked ControllerEvent = "approval_blocked"
	ControllerStageCommitted  ControllerEvent = "stage_committed"
	ControllerSessionClosed   ControllerEvent = "session_closed"
	ControllerExternalRead    ControllerEvent = "external_revision_read"
	ControllerExternalApplied ControllerEvent = "external_revision_applied"
	ControllerSuperseded      ControllerEvent = "feature_superseded"
)

var (
	ErrControllerNotOpen        = errors.New("feature controller is not open")
	ErrControllerCommandInvalid = errors.New("controller command is unavailable")
)

type featureAuthorEngine interface {
	Policy() (StagePolicy, bool)
	Start(StartStageRequest) (StagePolicy, error)
	SubmitBrief(string) (StageResult, error)
	Submit(string) (StageResult, error)
	Decide(RevisionAction, string) (StageResult, error)
	ReReadCurrentDocument() (StageResult, error)
	Close() error
}

type featureReviewEngine interface {
	Start(StartReviewRequest) (ReviewResult, error)
	Submit(string) (ReviewResult, error)
	Apply() (ReviewResult, error)
	Decide(MaterialFindingDecision) (ReviewResult, error)
	DecideFingerprint(ReviewFingerprintAction) (ReviewResult, error)
	Close() error
}

type reviewEngineBinding struct {
	reviewer *ReviewEngine
	author   *StageEngine
}

func (b reviewEngineBinding) Start(request StartReviewRequest) (ReviewResult, error) {
	return b.reviewer.StartWithAuthor(request, b.author)
}
func (b reviewEngineBinding) Submit(message string) (ReviewResult, error) {
	return b.reviewer.SubmitWithAuthor(message, b.author)
}
func (b reviewEngineBinding) Apply() (ReviewResult, error) {
	return b.reviewer.ApplyPendingMaterial(b.author)
}
func (b reviewEngineBinding) Decide(decision MaterialFindingDecision) (ReviewResult, error) {
	return b.reviewer.DecideMaterial(decision, b.author)
}
func (b reviewEngineBinding) DecideFingerprint(action ReviewFingerprintAction) (ReviewResult, error) {
	return b.reviewer.DecideFingerprint(action)
}
func (b reviewEngineBinding) Close() error { return b.reviewer.Close() }

// FeatureController is the single in-process owner of canonical planning-flow
// state. It accepts a new snapshot only after a repository or engine operation
// has durably succeeded, and reloads that snapshot before every user command.
type FeatureController struct {
	repository FeatureRepository
	author     featureAuthorEngine
	reviewer   featureReviewEngine
	now        func() time.Time

	featureID          string
	feature            FeatureSnapshot
	revisionPending    bool
	lastStage          StageResult
	lastReview         ReviewResult
	externalIntentHash string
	revisingSpec       bool
}

func NewFeatureController(repository FeatureRepository, author *StageEngine, reviewer *ReviewEngine) (*FeatureController, error) {
	if repository == nil || author == nil || reviewer == nil {
		return nil, fmt.Errorf("create feature controller: repository, author engine, and review engine are required")
	}
	return newFeatureController(repository, author, reviewEngineBinding{reviewer: reviewer, author: author}), nil
}

func newFeatureController(repository FeatureRepository, author featureAuthorEngine, reviewer featureReviewEngine) *FeatureController {
	return &FeatureController{repository: repository, author: author, reviewer: reviewer, now: time.Now}
}

// Begin creates a durable feature and opens only its intent author. If opening
// the runtime session fails, the returned progress still describes the created
// durable flow and can be resumed later.
func (c *FeatureController) Begin(request CreateFeatureRequest, runtimeContext string) (Progress, error) {
	if c.repository == nil || c.author == nil || c.reviewer == nil {
		return Progress{}, fmt.Errorf("begin feature: controller dependencies are required")
	}
	feature, err := c.repository.Create(request)
	if err != nil {
		return Progress{}, fmt.Errorf("begin feature: %w", err)
	}
	c.accept(feature)
	if _, err := c.startStage(StageIntent, runtimeContext); err != nil {
		return c.progress(ControllerStateShown), err
	}
	result, err := c.author.SubmitBrief(request.Brief)
	if err != nil {
		return c.fail("submit feature brief", err)
	}
	c.accept(result.Feature)
	c.lastStage = result
	c.revisionPending = result.Outcome == StageRevisionPending
	return c.progress(ControllerAuthorUpdated), nil
}

// Open selects an existing flow without implicitly creating a provider
// session. Resume/session policy can therefore decide when to start the role.
func (c *FeatureController) Open(featureID string) (Progress, error) {
	if strings.TrimSpace(featureID) == "" {
		return Progress{}, fmt.Errorf("open feature: feature ID is required")
	}
	c.featureID = featureID
	if err := c.reload(); err != nil {
		return c.progress(ControllerStateShown), fmt.Errorf("open feature: %w", err)
	}
	return c.progress(ControllerStateShown), nil
}

func (c *FeatureController) StartStage(stage Stage, runtimeContext string) (Progress, error) {
	if strings.TrimSpace(c.featureID) == "" {
		return c.progress(ControllerStateShown), ErrControllerNotOpen
	}
	if err := c.reload(); err != nil {
		return c.progress(ControllerStateShown), err
	}
	if !c.feature.Changes.Empty() {
		return c.handleExternalRevision(runtimeContext)
	}
	return c.startStage(stage, runtimeContext)
}

func (c *FeatureController) StartCurrentStage(runtimeContext string) (Progress, error) {
	if strings.TrimSpace(c.featureID) == "" {
		return c.progress(ControllerStateShown), ErrControllerNotOpen
	}
	if err := c.reload(); err != nil {
		return c.progress(ControllerStateShown), err
	}
	if !c.feature.Changes.Empty() {
		return c.handleExternalRevision(runtimeContext)
	}
	return c.startStage(c.feature.State.CurrentStage(), runtimeContext)
}

func (c *FeatureController) Execute(command ControllerCommand) (Progress, error) {
	if strings.TrimSpace(c.featureID) == "" {
		return c.progress(ControllerStateShown), ErrControllerNotOpen
	}
	if err := c.reload(); err != nil {
		return c.progress(ControllerStateShown), err
	}
	if c.externalIntentHash != "" {
		if !c.externalIntentStillPending() {
			c.externalIntentHash = ""
		} else {
			switch command.Kind {
			case ControllerClassifyIntent:
				return c.classifyIntentRevision(command)
			case ControllerStatus:
				return c.progress(ControllerStateShown), nil
			case ControllerClose:
				return c.close()
			default:
				return c.unavailable("the committed intent revision must be classified as material or non-material")
			}
		}
	}
	if !c.feature.Changes.Empty() {
		return c.handleExternalRevision(command.RuntimeContext)
	}
	switch command.Kind {
	case ControllerAuthorMessage:
		return c.authorMessage(command.Message)
	case ControllerRevisionDecision:
		return c.revisionDecision(command.RevisionAction, command.ReworkScope)
	case ControllerStartReview:
		return c.startReview(command)
	case ControllerReviewMessage:
		return c.reviewMessage(command.Message)
	case ControllerReviewDecision:
		return c.reviewDecision(command.ReviewDecision)
	case ControllerApplyReview:
		return c.applyReview()
	case ControllerFingerprintChoice:
		return c.fingerprintDecision(command.FingerprintAction)
	case ControllerApprove:
		return c.approve(command.RuntimeContext)
	case ControllerReviseSpec:
		return c.reviseSpec(command.RuntimeContext)
	case ControllerClassifyIntent:
		return c.unavailable("no committed intent revision is awaiting classification")
	case ControllerStatus:
		return c.progress(ControllerStateShown), nil
	case ControllerClose:
		return c.close()
	default:
		return c.unavailable(fmt.Sprintf("unknown controller command %q", command.Kind))
	}
}

func (c *FeatureController) AuthorMessage(message string) (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerAuthorMessage, Message: message})
}
func (c *FeatureController) RevisionDecision(action RevisionAction, scope string) (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerRevisionDecision, RevisionAction: action, ReworkScope: scope})
}
func (c *FeatureController) StartReview(provider, model, runtimeContext string) (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerStartReview, Provider: provider, Model: model, RuntimeContext: runtimeContext})
}
func (c *FeatureController) ReviewMessage(message string) (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerReviewMessage, Message: message})
}
func (c *FeatureController) ApplyReview() (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerApplyReview})
}
func (c *FeatureController) ReviewDecision(decision MaterialFindingDecision) (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerReviewDecision, ReviewDecision: decision})
}
func (c *FeatureController) ReviewFingerprintDecision(action ReviewFingerprintAction) (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerFingerprintChoice, FingerprintAction: action})
}
func (c *FeatureController) Approve(runtimeContext string) (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerApprove, RuntimeContext: runtimeContext})
}
func (c *FeatureController) ReviseSpec(runtimeContext string) (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerReviseSpec, RuntimeContext: runtimeContext})
}
func (c *FeatureController) ClassifyIntentRevision(classification IntentRevisionClassification, newFeatureID, runtimeContext string) (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerClassifyIntent, IntentRevision: classification, NewFeatureID: newFeatureID, RuntimeContext: runtimeContext})
}
func (c *FeatureController) Status() (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerStatus})
}
func (c *FeatureController) Close() (Progress, error) {
	return c.Execute(ControllerCommand{Kind: ControllerClose})
}

func (c *FeatureController) startStage(stage Stage, runtimeContext string) (Progress, error) {
	current := c.feature.State.CurrentStage()
	if stage != current {
		return c.unavailable(fmt.Sprintf("cannot start %s author while current stage is %s", stage, current))
	}
	state, _ := c.feature.State.Stage(stage)
	if state.Status != StageDrafting && state.Status != StagePublished {
		return c.unavailable(fmt.Sprintf("cannot start %s author while stage is %s", stage, state.Status))
	}
	if policy, active := c.author.Policy(); active {
		if policy.Stage == stage {
			return c.progress(ControllerAuthorStarted), nil
		}
		return c.unavailable(fmt.Sprintf("%s author session is already active", policy.Stage))
	}
	if _, err := c.author.Start(StartStageRequest{FeatureID: c.featureID, Stage: stage, RuntimeContext: runtimeContext}); err != nil {
		return c.fail("start author", err)
	}
	return c.progress(ControllerAuthorStarted), nil
}

func (c *FeatureController) authorMessage(message string) (Progress, error) {
	stage := c.currentAuthorStage()
	state, _ := c.feature.State.Stage(stage)
	if c.revisionPending || reviewBlocksAuthorInput(state.ReviewStatus) {
		return c.unavailable("author message is not available while a decision or review is pending")
	}
	if state.Status != StageDrafting && state.Status != StagePublished && !(c.revisingSpec && stage == StageSpec && state.Status == StageCommitted) {
		return c.unavailable(fmt.Sprintf("author message is unavailable while %s is %s", stage, state.Status))
	}
	if policy, active := c.author.Policy(); !active || policy.Stage != stage {
		return c.unavailable(fmt.Sprintf("%s author session is not active", stage))
	}
	result, err := c.author.Submit(message)
	if err != nil {
		return c.fail("submit author message", err)
	}
	c.accept(result.Feature)
	c.lastStage = result
	c.revisionPending = result.Outcome == StageRevisionPending
	return c.progress(ControllerAuthorUpdated), nil
}

func (c *FeatureController) revisionDecision(action RevisionAction, scope string) (Progress, error) {
	if !c.revisionPending {
		return c.unavailable("no author revision is awaiting a decision")
	}
	result, err := c.author.Decide(action, scope)
	if err != nil {
		return c.fail("decide author revision", err)
	}
	c.accept(result.Feature)
	c.lastStage = result
	c.revisionPending = result.Outcome == StageRevisionPending
	if c.revisingSpec && result.Outcome == StageRevisionApplied {
		return c.activateRevisedSpec()
	}
	return c.progress(ControllerAuthorUpdated), nil
}

func (c *FeatureController) startReview(command ControllerCommand) (Progress, error) {
	stage := c.feature.State.CurrentStage()
	state, _ := c.feature.State.Stage(stage)
	policy, _ := PolicyForStage(stage)
	if !policy.ReviewAvailable || state.Status != StagePublished || c.revisionPending {
		return c.unavailable(fmt.Sprintf("/review is unavailable for %s in %s", stage, state.Status))
	}
	if authorPolicy, active := c.author.Policy(); !active || authorPolicy.Stage != stage {
		return c.unavailable(fmt.Sprintf("%s author session is required before review", stage))
	}
	result, err := c.reviewer.Start(StartReviewRequest{FeatureID: c.featureID, Stage: stage, Provider: command.Provider, Model: command.Model, RuntimeContext: command.RuntimeContext})
	if err != nil {
		return c.fail("start review", err)
	}
	c.accept(result.Feature)
	c.lastReview = result
	return c.progress(ControllerReviewUpdated), nil
}

func (c *FeatureController) reviewMessage(message string) (Progress, error) {
	if !reviewAcceptsInput(c.currentReviewStatus()) {
		return c.unavailable("review dialogue is not active")
	}
	result, err := c.reviewer.Submit(message)
	if err != nil {
		return c.fail("submit review message", err)
	}
	c.accept(result.Feature)
	c.lastReview = result
	return c.progress(ControllerReviewUpdated), nil
}

func (c *FeatureController) applyReview() (Progress, error) {
	if c.currentReviewStatus() != ReviewAwaitingDecisions {
		return c.unavailable("/apply requires pending material review decisions")
	}
	result, err := c.reviewer.Apply()
	if err != nil {
		return c.fail("apply review findings", err)
	}
	c.accept(result.Feature)
	c.lastReview = result
	return c.progress(ControllerReviewUpdated), nil
}

func (c *FeatureController) reviewDecision(decision MaterialFindingDecision) (Progress, error) {
	if c.currentReviewStatus() != ReviewAwaitingDecisions {
		return c.unavailable("no material review decision is pending")
	}
	result, err := c.reviewer.Decide(decision)
	if err != nil {
		return c.fail("record review decision", err)
	}
	c.accept(result.Feature)
	c.lastReview = result
	return c.progress(ControllerReviewUpdated), nil
}

func (c *FeatureController) fingerprintDecision(action ReviewFingerprintAction) (Progress, error) {
	if len(c.lastReview.FingerprintActions) == 0 {
		return c.unavailable("no review fingerprint decision is pending")
	}
	result, err := c.reviewer.DecideFingerprint(action)
	if err != nil {
		return c.fail("decide review fingerprint", err)
	}
	c.accept(result.Feature)
	c.lastReview = result
	return c.progress(ControllerReviewUpdated), nil
}

func (c *FeatureController) approve(runtimeContext string) (Progress, error) {
	if c.revisingSpec {
		spec, _ := c.feature.State.Stage(StageSpec)
		if spec.CurrentHash == spec.ApprovedHash {
			closeErr := c.author.Close()
			c.revisingSpec = false
			c.revisionPending = false
			c.lastStage = StageResult{}
			if reloadErr := c.reload(); reloadErr != nil {
				return c.progress(ControllerStateShown), errors.Join(closeErr, reloadErr)
			}
			return c.progress(ControllerStateShown), closeErr
		}
	}
	stage := c.feature.State.CurrentStage()
	state, _ := c.feature.State.Stage(stage)
	if state.Status != StagePublished || c.revisionPending {
		return c.unavailable(fmt.Sprintf("/approve requires a published %s without a pending revision", stage))
	}
	if !approvalReviewReady(state.ReviewStatus) {
		return c.unavailable(fmt.Sprintf("/approve is blocked while %s review is %s", stage, state.ReviewStatus))
	}
	result, err := c.repository.Approve(ApproveStageRequest{FeatureID: c.featureID, Stage: stage, At: c.now()})
	if err != nil {
		if result.Feature.Target.ID != "" {
			c.accept(result.Feature)
		}
		return c.fail("approve stage", err)
	}
	c.accept(result.Feature)
	if !result.Committed {
		progress := c.progress(ControllerApprovalBlocked)
		progress.Blocking = append([]ApprovalBlocker(nil), result.Blocking...)
		return progress, nil
	}

	closeErr := errors.Join(c.reviewer.Close(), c.author.Close())
	c.revisionPending = false
	c.revisingSpec = false
	c.lastStage = StageResult{}
	c.lastReview = ReviewResult{}
	if reloadErr := c.reload(); reloadErr != nil {
		return c.progress(ControllerStageCommitted), errors.Join(closeErr, reloadErr)
	}
	if closeErr != nil {
		return c.progress(ControllerStageCommitted), fmt.Errorf("close committed %s sessions: %w", stage, closeErr)
	}
	if next := c.feature.State.CurrentStage(); next != stage {
		return c.startStage(next, runtimeContext)
	}
	return c.progress(ControllerStageCommitted), nil
}

func (c *FeatureController) close() (Progress, error) {
	engineErr := errors.Join(c.reviewer.Close(), c.author.Close())
	if errors.Is(engineErr, ErrExternalChanges) || errors.Is(engineErr, ErrRepositoryBlocked) {
		engineErr = nil
	}
	var sessionErr error
	if closer, ok := c.author.(interface{ CloseFeatureSessions(string) error }); ok {
		sessionErr = closer.CloseFeatureSessions(c.featureID)
	}
	err := errors.Join(engineErr, sessionErr)
	c.revisionPending = false
	c.lastStage = StageResult{}
	c.lastReview = ReviewResult{}
	c.externalIntentHash = ""
	c.revisingSpec = false
	if reloadErr := c.reload(); reloadErr != nil {
		err = errors.Join(err, reloadErr)
	}
	progress := c.progress(ControllerSessionClosed)
	if err != nil {
		return progress, fmt.Errorf("close feature session: %w", err)
	}
	return progress, nil
}

func (c *FeatureController) reviseSpec(runtimeContext string) (Progress, error) {
	if c.feature.State.CurrentStage() != StagePlan {
		return c.unavailable("/revise-spec is available only from plan")
	}
	spec, _ := c.feature.State.Stage(StageSpec)
	if spec.Status != StageCommitted {
		return c.unavailable("/revise-spec requires a committed spec")
	}
	if policy, active := c.author.Policy(); active {
		if policy.Stage == StageSpec {
			c.revisingSpec = true
			return c.progress(ControllerAuthorStarted), nil
		}
		if err := errors.Join(c.reviewer.Close(), c.author.Close()); err != nil {
			return c.fail("switch to spec author", err)
		}
	}
	if _, err := c.author.Start(StartStageRequest{FeatureID: c.featureID, Stage: StageSpec, RuntimeContext: runtimeContext}); err != nil {
		return c.fail("start spec revision author", err)
	}
	c.revisingSpec = true
	return c.progress(ControllerAuthorStarted), nil
}

func (c *FeatureController) activateRevisedSpec() (Progress, error) {
	result, err := c.repository.AcceptExternalRevision(ExternalRevisionRequest{FeatureID: c.featureID, Stage: StageSpec, At: c.now()})
	if err != nil {
		return c.fail("activate revised spec", err)
	}
	c.accept(result.Feature)
	if !result.Accepted {
		progress := c.progress(ControllerExternalRead)
		progress.Diagnostics = append([]DocumentDiagnostic(nil), result.Validation.Diagnostics...)
		return progress, nil
	}
	c.revisingSpec = false
	return c.progress(ControllerExternalApplied), nil
}

func (c *FeatureController) handleExternalRevision(runtimeContext string) (Progress, error) {
	if len(c.feature.Changes.Blocking) > 0 {
		return c.progress(ControllerStateShown), c.feature.Changes.Error()
	}
	if len(c.feature.Changes.DocumentRevisions) != 1 {
		return c.progress(ControllerStateShown), fmt.Errorf("%w: exactly one document may be revised at a time", ErrExternalChanges)
	}
	change := c.feature.Changes.DocumentRevisions[0]
	if change.Missing {
		return c.progress(ControllerStateShown), fmt.Errorf("%w: revised %s document must not be removed", ErrExternalChanges, change.Stage)
	}
	stageState, _ := c.feature.State.Stage(change.Stage)
	if change.Stage == StageIntent && stageState.Status != StageCommitted {
		return c.progress(ControllerStateShown), fmt.Errorf("%w: only a committed intent uses the external revision lifecycle", ErrExternalChanges)
	}
	if change.Stage == StageSpec && c.feature.State.CurrentStage() != StagePlan && c.feature.State.CurrentStage() != StageSpec {
		return c.progress(ControllerStateShown), fmt.Errorf("%w: spec cannot be revised from %s", ErrExternalChanges, c.feature.State.CurrentStage())
	}
	if change.Stage == StagePlan && c.feature.State.CurrentStage() != StagePlan {
		return c.progress(ControllerStateShown), fmt.Errorf("%w: plan cannot be revised from %s", ErrExternalChanges, c.feature.State.CurrentStage())
	}
	if err := c.prepareExternalAuthor(change.Stage, runtimeContext); err != nil {
		return c.fail("start external revision author", err)
	}
	read, err := c.author.ReReadCurrentDocument()
	if err != nil {
		return c.fail("re-read external revision", err)
	}
	c.lastStage = read
	inspection, err := c.repository.InspectExternalRevision(ExternalRevisionRequest{FeatureID: c.featureID, Stage: change.Stage, At: c.now()})
	if err != nil {
		return c.fail("inspect external revision", err)
	}
	c.accept(inspection.Feature)
	if !inspection.Validation.Valid() {
		progress := c.progress(ControllerExternalRead)
		progress.Diagnostics = append([]DocumentDiagnostic(nil), inspection.Validation.Diagnostics...)
		return progress, nil
	}
	if change.Stage == StageIntent {
		c.externalIntentHash = change.ActualHash
		return c.progress(ControllerExternalRead), nil
	}
	accepted, err := c.repository.AcceptExternalRevision(ExternalRevisionRequest{FeatureID: c.featureID, Stage: change.Stage, At: c.now()})
	if err != nil {
		return c.fail("accept external revision", err)
	}
	c.accept(accepted.Feature)
	if !accepted.Accepted {
		progress := c.progress(ControllerExternalRead)
		progress.Diagnostics = append([]DocumentDiagnostic(nil), accepted.Validation.Diagnostics...)
		return progress, nil
	}
	c.revisingSpec = change.Stage == StageSpec
	return c.progress(ControllerExternalApplied), nil
}

func (c *FeatureController) prepareExternalAuthor(stage Stage, runtimeContext string) error {
	if policy, active := c.author.Policy(); active {
		if policy.Stage == stage {
			return nil
		}
		closeErr := errors.Join(c.reviewer.Close(), c.author.Close())
		if closeErr != nil && !errors.Is(closeErr, ErrExternalChanges) && !errors.Is(closeErr, ErrRepositoryBlocked) {
			return closeErr
		}
	}
	_, err := c.author.Start(StartStageRequest{FeatureID: c.featureID, Stage: stage, RuntimeContext: runtimeContext})
	return err
}

func (c *FeatureController) classifyIntentRevision(command ControllerCommand) (Progress, error) {
	if command.IntentRevision != IntentRevisionNonMaterial && command.IntentRevision != IntentRevisionMaterial {
		return c.unavailable("intent revision classification must be material or non-material")
	}
	closeErr := errors.Join(c.reviewer.Close(), c.author.Close())
	if closeErr != nil && !errors.Is(closeErr, ErrExternalChanges) && !errors.Is(closeErr, ErrRepositoryBlocked) {
		return c.fail("close intent revision author", closeErr)
	}
	if command.IntentRevision == IntentRevisionNonMaterial {
		result, err := c.repository.ReviseIntent(ReviseIntentRequest{FeatureID: c.featureID, At: c.now()})
		if err != nil {
			return c.fail("commit non-material intent revision", err)
		}
		c.accept(result.Feature)
		if !result.Committed {
			progress := c.progress(ControllerApprovalBlocked)
			progress.Blocking = append([]ApprovalBlocker(nil), result.Blocking...)
			return progress, nil
		}
		c.externalIntentHash = ""
		return c.progress(ControllerExternalApplied), nil
	}
	if strings.TrimSpace(command.NewFeatureID) == "" {
		return c.unavailable("a new feature ID is required for a material intent revision")
	}
	result, err := c.repository.SupersedeIntent(SupersedeIntentRequest{OldFeatureID: c.featureID, NewFeatureID: command.NewFeatureID, At: c.now()})
	if err != nil {
		return c.fail("supersede material intent revision", err)
	}
	if !result.Committed {
		c.accept(result.Old)
		progress := c.progress(ControllerApprovalBlocked)
		progress.Blocking = append([]ApprovalBlocker(nil), result.Blocking...)
		return progress, nil
	}
	c.accept(result.New)
	c.externalIntentHash = ""
	c.revisionPending = false
	c.lastStage = StageResult{}
	c.lastReview = ReviewResult{}
	if _, err := c.author.Start(StartStageRequest{FeatureID: c.featureID, Stage: StageIntent, RuntimeContext: command.RuntimeContext}); err != nil {
		return c.progress(ControllerSuperseded), fmt.Errorf("start superseding intent author: %w", err)
	}
	return c.progress(ControllerSuperseded), nil
}

func (c *FeatureController) externalIntentStillPending() bool {
	if len(c.feature.Changes.DocumentRevisions) != 1 {
		return false
	}
	change := c.feature.Changes.DocumentRevisions[0]
	return change.Stage == StageIntent && change.ActualHash == c.externalIntentHash
}

func (c *FeatureController) currentAuthorStage() Stage {
	if c.revisingSpec {
		return StageSpec
	}
	return c.feature.State.CurrentStage()
}

func (c *FeatureController) reload() error {
	feature, err := c.repository.Load(c.featureID)
	if err != nil {
		return err
	}
	c.accept(feature)
	return nil
}

func (c *FeatureController) accept(feature FeatureSnapshot) {
	if feature.Target.ID == "" {
		return
	}
	c.featureID = feature.Target.ID
	c.feature = feature
}

func (c *FeatureController) fail(operation string, cause error) (Progress, error) {
	reloadErr := c.reload()
	if reloadErr != nil {
		cause = errors.Join(cause, fmt.Errorf("reload durable snapshot: %w", reloadErr))
	}
	return c.progress(ControllerStateShown), fmt.Errorf("%s: %w", operation, cause)
}

func (c *FeatureController) unavailable(message string) (Progress, error) {
	return c.progress(ControllerStateShown), fmt.Errorf("%w: %s", ErrControllerCommandInvalid, message)
}

func (c *FeatureController) currentReviewStatus() ReviewStatus {
	state, ok := c.feature.State.Stage(c.feature.State.CurrentStage())
	if !ok {
		return ReviewNotStarted
	}
	return state.ReviewStatus
}

func (c *FeatureController) progress(event ControllerEvent) Progress {
	if c.feature.Target.ID == "" {
		return Progress{Event: event}
	}
	hints, textAllowed := controllerHints(c.feature.State, c.revisionPending)
	progress, err := NewProgress(c.feature.State, hints, textAllowed)
	if err != nil {
		return Progress{Event: event, FeatureID: c.featureID}
	}
	progress.Event = event
	progress.FeatureID = c.featureID
	progress.Message = c.lastStage.Message
	progress.Diff = c.lastStage.Diff
	progress.Diagnostics = append([]DocumentDiagnostic(nil), c.lastStage.Diagnostics...)
	progress.Revision = append([]RevisionAction(nil), c.lastStage.RevisionActions...)
	progress.Review = c.lastReview
	if c.externalIntentHash != "" {
		progress.CommandHints = mustCommandHints([][2]string{
			{"/non-material", "Keep downstream approvals and commit the revised intent."},
			{"/material", "Supersede this feature and approve the revised intent in a new feature."},
			{"/status", "Show the durable flow state."},
			{"/exit", "Close the current session while preserving the revised document."},
		})
		progress.TextAllowed = false
	}
	if len(c.lastReview.Diagnostics) > 0 {
		progress.Diagnostics = append([]DocumentDiagnostic(nil), c.lastReview.Diagnostics...)
	}
	progress.Documents = make([]DocumentPath, 0, len(c.feature.Documents))
	for _, stage := range stages {
		if document, ok := c.feature.Documents[stage]; ok {
			progress.Documents = append(progress.Documents, DocumentPath{Stage: stage, Path: document.Path})
			if stage == c.feature.State.CurrentStage() {
				progress.Path = document.Path
			}
		}
	}
	return progress
}

func controllerHints(state FlowState, revisionPending bool) ([]CommandHint, bool) {
	stage := state.CurrentStage()
	stageState, _ := state.Stage(stage)
	if revisionPending {
		return mustCommandHints([][2]string{{"/status", "Show the durable flow state."}, {"/exit", "Close the current session and discard the pending draft."}}), false
	}
	pairs := make([][2]string, 0, 5)
	authorTextAllowed := (stageState.Status == StageDrafting || stageState.Status == StagePublished) && !reviewBlocksAuthorInput(stageState.ReviewStatus)
	reviewerTextAllowed := stageState.ReviewStatus == ReviewRunning || stageState.ReviewStatus == ReviewAwaitingDecisions
	if stageState.Status == StagePublished && (stage == StageSpec || stage == StagePlan) &&
		(stageState.ReviewStatus == ReviewNotStarted || stageState.ReviewStatus == ReviewCompleted || stageState.ReviewStatus == ReviewEscalated) {
		pairs = append(pairs, [2]string{"/review", "Start agent review of the current published document."})
	}
	if stageState.ReviewStatus == ReviewAwaitingDecisions {
		pairs = append(pairs, [2]string{"/apply", "Accept every pending material review recommendation."})
	}
	if stageState.Status == StagePublished && approvalReviewReady(stageState.ReviewStatus) {
		pairs = append(pairs, [2]string{"/approve", "Validate and commit the current stage."})
	}
	if stage == StagePlan {
		pairs = append(pairs, [2]string{"/revise-spec", "Open the specification author dialogue."})
	}
	pairs = append(pairs, [2]string{"/status", "Show stages, documents, and review state."})
	pairs = append(pairs, [2]string{"/exit", "Close the current session while preserving the flow."})
	return mustCommandHints(pairs), authorTextAllowed || reviewerTextAllowed
}

func mustCommandHints(values [][2]string) []CommandHint {
	result := make([]CommandHint, 0, len(values))
	for _, value := range values {
		hint, err := NewCommandHint(value[0], value[1])
		if err != nil {
			panic(err)
		}
		result = append(result, hint)
	}
	return result
}

func approvalReviewReady(status ReviewStatus) bool {
	return status == ReviewNotStarted || status == ReviewCompleted
}

func reviewBlocksAuthorInput(status ReviewStatus) bool {
	switch status {
	case ReviewRunning, ReviewAwaitingDecisions, ReviewAutomaticRework:
		return true
	default:
		return false
	}
}

func reviewAcceptsInput(status ReviewStatus) bool {
	return status == ReviewRunning || status == ReviewAwaitingDecisions || status == ReviewEscalated
}
