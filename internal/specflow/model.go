package specflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var ErrInvalidDomainValue = errors.New("invalid specflow domain value")

type Stage string

const (
	StageIntent Stage = "intent"
	StageSpec   Stage = "spec"
	StagePlan   Stage = "plan"
)

var stages = [...]Stage{StageIntent, StageSpec, StagePlan}

func ParseStage(value string) (Stage, error) {
	stage := Stage(value)
	if !stage.Valid() {
		return "", domainError("stage", value)
	}
	return stage, nil
}

func (s Stage) Valid() bool {
	return s == StageIntent || s == StageSpec || s == StagePlan
}

func (s Stage) order() int {
	switch s {
	case StageIntent:
		return 0
	case StageSpec:
		return 1
	case StagePlan:
		return 2
	default:
		return len(stages)
	}
}

type Role string

const (
	RoleIntentAuthor Role = "intent-author"
	RoleSpecAuthor   Role = "spec-author"
	RoleSpecReviewer Role = "spec-reviewer"
	RolePlanAuthor   Role = "plan-author"
	RolePlanReviewer Role = "plan-reviewer"
)

func ParseRole(value string) (Role, error) {
	role := Role(value)
	if !role.Valid() {
		return "", domainError("role", value)
	}
	return role, nil
}

func (r Role) Valid() bool {
	switch r {
	case RoleIntentAuthor, RoleSpecAuthor, RoleSpecReviewer, RolePlanAuthor, RolePlanReviewer:
		return true
	default:
		return false
	}
}

func AuthorRole(stage Stage) (Role, error) {
	switch stage {
	case StageIntent:
		return RoleIntentAuthor, nil
	case StageSpec:
		return RoleSpecAuthor, nil
	case StagePlan:
		return RolePlanAuthor, nil
	default:
		return "", domainError("stage", stage)
	}
}

func ReviewerRole(stage Stage) (Role, error) {
	switch stage {
	case StageSpec:
		return RoleSpecReviewer, nil
	case StagePlan:
		return RolePlanReviewer, nil
	case StageIntent:
		return "", fmt.Errorf("%w: intent has no agent reviewer", ErrInvalidDomainValue)
	default:
		return "", domainError("stage", stage)
	}
}

type FlowStatus string

const (
	FlowActive     FlowStatus = "active"
	FlowSuperseded FlowStatus = "superseded"
)

func (s FlowStatus) Valid() bool { return s == FlowActive || s == FlowSuperseded }

type StageStatus string

const (
	StageNotStarted StageStatus = "not_started"
	StageDrafting   StageStatus = "drafting"
	StagePublished  StageStatus = "published"
	StageCommitted  StageStatus = "committed"
)

func (s StageStatus) Valid() bool {
	switch s {
	case StageNotStarted, StageDrafting, StagePublished, StageCommitted:
		return true
	default:
		return false
	}
}

type ReviewStatus string

const (
	ReviewNotStarted        ReviewStatus = "not_started"
	ReviewRunning           ReviewStatus = "running"
	ReviewAwaitingDecisions ReviewStatus = "awaiting_decisions"
	ReviewAutomaticRework   ReviewStatus = "automatic_rework"
	ReviewEscalated         ReviewStatus = "escalated"
	ReviewCompleted         ReviewStatus = "completed"
)

func (s ReviewStatus) Valid() bool {
	switch s {
	case ReviewNotStarted, ReviewRunning, ReviewAwaitingDecisions, ReviewAutomaticRework, ReviewEscalated, ReviewCompleted:
		return true
	default:
		return false
	}
}

type IDFamily string

const (
	IDRequirement IDFamily = "REQ"
	IDDecision    IDFamily = "DEC"
	IDAcceptance  IDFamily = "AC"
	IDTask        IDFamily = "TASK"
	IDSpecFinding IDFamily = "SPEC-F"
	IDPlanFinding IDFamily = "PLAN-F"
)

var stableIDPattern = regexp.MustCompile(`^(REQ|DEC|AC|TASK|SPEC-F|PLAN-F)-([0-9]+)$`)

// StableID deliberately excludes the source spelling. A parsed document can
// retain that spelling, while durable identity uses the canonical numeric form.
type StableID struct {
	family IDFamily
	number string
}

func ParseStableID(value string) (StableID, error) {
	match := stableIDPattern.FindStringSubmatch(value)
	if match == nil {
		return StableID{}, domainError("stable ID", value)
	}
	number := strings.TrimLeft(match[2], "0")
	if number == "" {
		return StableID{}, domainError("stable ID", value)
	}
	return StableID{family: IDFamily(match[1]), number: number}, nil
}

func (id StableID) Valid() bool {
	if id.number == "" || id.number[0] == '0' {
		return false
	}
	for _, digit := range id.number {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	switch id.family {
	case IDRequirement, IDDecision, IDAcceptance, IDTask, IDSpecFinding, IDPlanFinding:
		return true
	default:
		return false
	}
}

func (id StableID) Family() IDFamily { return id.family }
func (id StableID) Number() string   { return id.number }

func (id StableID) String() string {
	if !id.Valid() {
		return ""
	}
	return string(id.family) + "-" + id.number
}

func (id StableID) MarshalJSON() ([]byte, error) {
	if !id.Valid() {
		return nil, domainError("stable ID", id.String())
	}
	return json.Marshal(id.String())
}

func (id *StableID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode stable ID: %w", err)
	}
	parsed, err := ParseStableID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

type UpstreamHash struct {
	Stage Stage  `json:"stage"`
	Hash  string `json:"hash"`
}

// Fingerprint equality is intentionally limited to document bytes. Runtime,
// provider and session identity do not participate in it.
type Fingerprint struct {
	targetHash string
	upstream   []UpstreamHash
}

func NewFingerprint(targetHash string, upstream []UpstreamHash) (Fingerprint, error) {
	targetHash = strings.TrimSpace(targetHash)
	if targetHash == "" {
		return Fingerprint{}, domainError("target hash", targetHash)
	}
	copyUpstream := append([]UpstreamHash(nil), upstream...)
	seen := make(map[Stage]struct{}, len(copyUpstream))
	for i := range copyUpstream {
		copyUpstream[i].Hash = strings.TrimSpace(copyUpstream[i].Hash)
		if !copyUpstream[i].Stage.Valid() || copyUpstream[i].Hash == "" {
			return Fingerprint{}, fmt.Errorf("%w: invalid upstream hash at index %d", ErrInvalidDomainValue, i)
		}
		if _, exists := seen[copyUpstream[i].Stage]; exists {
			return Fingerprint{}, fmt.Errorf("%w: duplicate upstream stage %q", ErrInvalidDomainValue, copyUpstream[i].Stage)
		}
		seen[copyUpstream[i].Stage] = struct{}{}
	}
	sort.Slice(copyUpstream, func(i, j int) bool { return copyUpstream[i].Stage.order() < copyUpstream[j].Stage.order() })
	return Fingerprint{targetHash: targetHash, upstream: copyUpstream}, nil
}

func (f Fingerprint) Valid() bool {
	_, err := NewFingerprint(f.targetHash, f.upstream)
	return err == nil
}

func (f Fingerprint) TargetHash() string { return f.targetHash }

func (f Fingerprint) UpstreamHashes() []UpstreamHash {
	return append([]UpstreamHash(nil), f.upstream...)
}

func (f Fingerprint) Equal(other Fingerprint) bool {
	if f.targetHash != other.targetHash || len(f.upstream) != len(other.upstream) {
		return false
	}
	for i := range f.upstream {
		if f.upstream[i] != other.upstream[i] {
			return false
		}
	}
	return true
}

func (f Fingerprint) MarshalJSON() ([]byte, error) {
	if !f.Valid() {
		return nil, domainError("fingerprint", "")
	}
	return json.Marshal(struct {
		TargetHash     string         `json:"target_hash"`
		UpstreamHashes []UpstreamHash `json:"upstream_hashes"`
	}{f.targetHash, f.upstream})
}

func (f *Fingerprint) UnmarshalJSON(data []byte) error {
	var value struct {
		TargetHash     string         `json:"target_hash"`
		UpstreamHashes []UpstreamHash `json:"upstream_hashes"`
	}
	if err := decodeExact(data, &value); err != nil {
		return err
	}
	parsed, err := NewFingerprint(value.TargetHash, value.UpstreamHashes)
	if err != nil {
		return err
	}
	*f = parsed
	return nil
}

type FindingKind string

const (
	FindingContractViolation FindingKind = "contract_violation"
	FindingMaterial          FindingKind = "material"
)

func (k FindingKind) Valid() bool { return k == FindingContractViolation || k == FindingMaterial }

type FindingSeverity string

const (
	SeverityBlocker FindingSeverity = "blocker"
	SeverityMajor   FindingSeverity = "major"
	SeverityMinor   FindingSeverity = "minor"
)

func (s FindingSeverity) Valid() bool {
	return s == SeverityBlocker || s == SeverityMajor || s == SeverityMinor
}

type FindingStatus string

const (
	FindingOpen       FindingStatus = "open"
	FindingResolved   FindingStatus = "resolved"
	FindingDismissed  FindingStatus = "dismissed"
	FindingSuperseded FindingStatus = "superseded"
)

func (s FindingStatus) Valid() bool {
	switch s {
	case FindingOpen, FindingResolved, FindingDismissed, FindingSuperseded:
		return true
	default:
		return false
	}
}

type FindingDecision string

const (
	DecisionPending FindingDecision = "pending"
	DecisionFix     FindingDecision = "fix"
	DecisionDismiss FindingDecision = "dismiss"
)

func (d FindingDecision) Valid() bool {
	return d == DecisionPending || d == DecisionFix || d == DecisionDismiss
}

type DecisionMaker string

const (
	DecidedByNone     DecisionMaker = "none"
	DecidedByUser     DecisionMaker = "user"
	DecidedByReviewer DecisionMaker = "reviewer"
)

func (m DecisionMaker) Valid() bool {
	return m == DecidedByNone || m == DecidedByUser || m == DecidedByReviewer
}

type FindingDecisionRecord struct {
	Decision  FindingDecision `json:"decision"`
	DecidedBy DecisionMaker   `json:"decided_by"`
	Rationale string          `json:"rationale"`
}

type FindingInput struct {
	ID             StableID
	Kind           FindingKind
	Severity       FindingSeverity
	Problem        string
	Location       string
	Traces         []StableID
	Recommendation string
	Rationale      string
}

type FindingSnapshot struct {
	ID             StableID              `json:"id"`
	Kind           FindingKind           `json:"kind"`
	Severity       FindingSeverity       `json:"severity"`
	Problem        string                `json:"problem"`
	Location       string                `json:"location"`
	Traces         []StableID            `json:"traces"`
	Status         FindingStatus         `json:"status"`
	Recommendation string                `json:"recommendation"`
	Resolution     string                `json:"resolution"`
	SupersededBy   StableID              `json:"superseded_by,omitempty"`
	Decision       FindingDecisionRecord `json:"finding_decision"`
}

// Finding keeps identity private; callers can only obtain changed copies by
// applying a validated lifecycle update.
type Finding struct{ snapshot FindingSnapshot }

func NewFinding(input FindingInput) (Finding, error) {
	decision := FindingDecisionRecord{Decision: DecisionPending, DecidedBy: DecidedByNone}
	if input.Kind == FindingContractViolation {
		decision = FindingDecisionRecord{
			Decision:  DecisionFix,
			DecidedBy: DecidedByReviewer,
			Rationale: strings.TrimSpace(input.Rationale),
		}
	} else if strings.TrimSpace(input.Rationale) != "" {
		return Finding{}, fmt.Errorf("%w: pending material finding cannot have rationale", ErrInvalidDomainValue)
	}
	return RestoreFinding(FindingSnapshot{
		ID:             input.ID,
		Kind:           input.Kind,
		Severity:       input.Severity,
		Problem:        strings.TrimSpace(input.Problem),
		Location:       strings.TrimSpace(input.Location),
		Traces:         append([]StableID(nil), input.Traces...),
		Status:         FindingOpen,
		Recommendation: strings.TrimSpace(input.Recommendation),
		Decision:       decision,
	})
}

func RestoreFinding(snapshot FindingSnapshot) (Finding, error) {
	snapshot.Traces = append([]StableID(nil), snapshot.Traces...)
	snapshot.Problem = strings.TrimSpace(snapshot.Problem)
	snapshot.Location = strings.TrimSpace(snapshot.Location)
	snapshot.Recommendation = strings.TrimSpace(snapshot.Recommendation)
	snapshot.Resolution = strings.TrimSpace(snapshot.Resolution)
	snapshot.Decision.Rationale = strings.TrimSpace(snapshot.Decision.Rationale)
	if err := validateFinding(snapshot); err != nil {
		return Finding{}, err
	}
	return Finding{snapshot: snapshot}, nil
}

func (f Finding) Snapshot() FindingSnapshot {
	value := f.snapshot
	value.Traces = append([]StableID(nil), f.snapshot.Traces...)
	return value
}

// WithSeverity exists to make the immutability rule explicit at the public
// interface instead of relying on callers to remember it.
func (f Finding) WithSeverity(severity FindingSeverity) (Finding, error) {
	if !severity.Valid() {
		return Finding{}, domainError("finding severity", severity)
	}
	if severity != f.snapshot.Severity {
		return Finding{}, fmt.Errorf("%w: finding severity is immutable", ErrInvalidDomainValue)
	}
	return f, nil
}

type FindingUpdate struct {
	Status         FindingStatus
	Recommendation string
	Resolution     string
	SupersededBy   StableID
	Decision       FindingDecisionRecord
}

func (f Finding) Update(update FindingUpdate) (Finding, error) {
	value := f.Snapshot()
	value.Status = update.Status
	value.Recommendation = strings.TrimSpace(update.Recommendation)
	value.Resolution = strings.TrimSpace(update.Resolution)
	value.SupersededBy = update.SupersededBy
	value.Decision = update.Decision
	return RestoreFinding(value)
}

func validateFinding(value FindingSnapshot) error {
	if !value.ID.Valid() || (value.ID.Family() != IDSpecFinding && value.ID.Family() != IDPlanFinding) {
		return domainError("finding ID", value.ID.String())
	}
	if !value.Kind.Valid() || !value.Severity.Valid() || !value.Status.Valid() {
		return fmt.Errorf("%w: invalid finding kind, severity, or status", ErrInvalidDomainValue)
	}
	if value.Problem == "" {
		return domainError("finding problem", value.Problem)
	}
	for _, trace := range value.Traces {
		if !trace.Valid() {
			return domainError("finding trace", trace.String())
		}
	}
	if !value.Decision.Decision.Valid() || !value.Decision.DecidedBy.Valid() {
		return fmt.Errorf("%w: invalid finding decision", ErrInvalidDomainValue)
	}
	if value.Kind == FindingContractViolation {
		if value.Status == FindingDismissed {
			return fmt.Errorf("%w: contract violation cannot be dismissed", ErrInvalidDomainValue)
		}
		if value.Decision.Decision != DecisionFix || value.Decision.DecidedBy != DecidedByReviewer || value.Decision.Rationale == "" {
			return fmt.Errorf("%w: contract violation must be fixed by reviewer decision", ErrInvalidDomainValue)
		}
	} else {
		if value.Decision.DecidedBy == DecidedByReviewer {
			return fmt.Errorf("%w: reviewer cannot decide a material finding", ErrInvalidDomainValue)
		}
		if value.Decision.Decision == DecisionPending && value.Decision.DecidedBy != DecidedByNone {
			return fmt.Errorf("%w: pending material finding must have no decision maker", ErrInvalidDomainValue)
		}
		if value.Decision.Decision != DecisionPending && value.Decision.DecidedBy != DecidedByUser {
			return fmt.Errorf("%w: material finding decision belongs to user", ErrInvalidDomainValue)
		}
	}
	if value.Decision.DecidedBy == DecidedByNone && value.Decision.Rationale != "" {
		return fmt.Errorf("%w: undecided finding cannot have rationale", ErrInvalidDomainValue)
	}
	if value.Decision.DecidedBy != DecidedByNone && value.Decision.Rationale == "" {
		return fmt.Errorf("%w: decided finding requires rationale", ErrInvalidDomainValue)
	}
	if value.Status == FindingResolved && value.Resolution == "" {
		return fmt.Errorf("%w: resolved finding requires resolution", ErrInvalidDomainValue)
	}
	if value.Status == FindingResolved && value.Decision.Decision != DecisionFix {
		return fmt.Errorf("%w: resolved finding requires fix decision", ErrInvalidDomainValue)
	}
	if value.Status == FindingDismissed {
		if value.Kind != FindingMaterial || value.Decision.Decision != DecisionDismiss || value.Decision.DecidedBy != DecidedByUser || value.Decision.Rationale == "" {
			return fmt.Errorf("%w: dismissed material finding requires user rationale", ErrInvalidDomainValue)
		}
	}
	if value.Decision.Decision == DecisionDismiss && value.Status != FindingDismissed {
		return fmt.Errorf("%w: dismiss decision requires dismissed status", ErrInvalidDomainValue)
	}
	if value.Status == FindingSuperseded {
		if !value.SupersededBy.Valid() || value.SupersededBy == value.ID {
			return fmt.Errorf("%w: superseded finding requires a different successor", ErrInvalidDomainValue)
		}
	} else if value.SupersededBy.Valid() {
		return fmt.Errorf("%w: superseded-by is only valid for superseded finding", ErrInvalidDomainValue)
	}
	return nil
}

func domainError(field string, value any) error {
	return fmt.Errorf("%w: %s %q", ErrInvalidDomainValue, field, value)
}
