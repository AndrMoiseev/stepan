package specflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	FlowStateVersion  = 1
	DefaultRetryLimit = 3
)

type RetryCounters struct {
	ParserRepair int `json:"parser_repair"`
	ReviewRework int `json:"review_rework"`
}

func (r RetryCounters) Validate() error {
	if r.ParserRepair < 0 || r.ParserRepair > DefaultRetryLimit || r.ReviewRework < 0 || r.ReviewRework > DefaultRetryLimit {
		return fmt.Errorf("%w: retry counters must be between 0 and %d", ErrInvalidDomainValue, DefaultRetryLimit)
	}
	return nil
}

type ReviewRun struct {
	ID                  uint64       `json:"id"`
	Path                string       `json:"path"`
	Status              ReviewStatus `json:"status"`
	OriginalFingerprint Fingerprint  `json:"original_fingerprint"`
	AcceptedFingerprint *Fingerprint `json:"accepted_fingerprint,omitempty"`
	Attempts            int          `json:"attempts"`
	ReportHash          string       `json:"report_hash,omitempty"`
}

func (r ReviewRun) Validate() error {
	if r.ID == 0 || strings.TrimSpace(r.Path) == "" || !r.Status.Valid() || !r.OriginalFingerprint.Valid() {
		return fmt.Errorf("%w: invalid review metadata", ErrInvalidDomainValue)
	}
	if r.AcceptedFingerprint != nil && !r.AcceptedFingerprint.Valid() {
		return fmt.Errorf("%w: invalid accepted review fingerprint", ErrInvalidDomainValue)
	}
	if r.Attempts < 0 || r.Attempts > DefaultRetryLimit {
		return fmt.Errorf("%w: review attempts must be between 0 and %d", ErrInvalidDomainValue, DefaultRetryLimit)
	}
	if r.ReportHash != "" && len(r.ReportHash) != 64 {
		return fmt.Errorf("%w: invalid review report hash", ErrInvalidDomainValue)
	}
	return nil
}

type StageState struct {
	Status         StageStatus    `json:"status"`
	ReviewStatus   ReviewStatus   `json:"review_status"`
	CurrentHash    string         `json:"current_hash,omitempty"`
	ApprovedHash   string         `json:"approved_hash,omitempty"`
	UpstreamHashes []UpstreamHash `json:"upstream_hashes"`
	Outdated       bool           `json:"outdated"`
	RetryCounters  RetryCounters  `json:"retry_counters"`
	Reviews        []ReviewRun    `json:"reviews"`
}

func (s StageState) clone() StageState {
	value := s
	value.UpstreamHashes = append([]UpstreamHash(nil), s.UpstreamHashes...)
	value.Reviews = append([]ReviewRun(nil), s.Reviews...)
	for i := range value.Reviews {
		if value.Reviews[i].AcceptedFingerprint != nil {
			copyFingerprint := *value.Reviews[i].AcceptedFingerprint
			value.Reviews[i].AcceptedFingerprint = &copyFingerprint
		}
	}
	return value
}

func (s StageState) Validate() error {
	if !s.Status.Valid() || !s.ReviewStatus.Valid() {
		return fmt.Errorf("%w: invalid stage or review status", ErrInvalidDomainValue)
	}
	if err := s.RetryCounters.Validate(); err != nil {
		return err
	}
	seenUpstream := make(map[Stage]struct{}, len(s.UpstreamHashes))
	for _, upstream := range s.UpstreamHashes {
		if !upstream.Stage.Valid() || strings.TrimSpace(upstream.Hash) == "" {
			return fmt.Errorf("%w: invalid stage upstream hash", ErrInvalidDomainValue)
		}
		if _, exists := seenUpstream[upstream.Stage]; exists {
			return fmt.Errorf("%w: duplicate upstream stage %q", ErrInvalidDomainValue, upstream.Stage)
		}
		seenUpstream[upstream.Stage] = struct{}{}
	}
	seenReviews := make(map[uint64]struct{}, len(s.Reviews))
	for _, review := range s.Reviews {
		if err := review.Validate(); err != nil {
			return err
		}
		if _, exists := seenReviews[review.ID]; exists {
			return fmt.Errorf("%w: duplicate review ID %d", ErrInvalidDomainValue, review.ID)
		}
		seenReviews[review.ID] = struct{}{}
	}
	return nil
}

type SupersessionLinks struct {
	Supersedes   string `json:"supersedes,omitempty"`
	SupersededBy string `json:"superseded_by,omitempty"`
}

func (s SupersessionLinks) Validate() error {
	if s.Supersedes != "" && s.Supersedes == s.SupersededBy {
		return fmt.Errorf("%w: supersession links must identify different flows", ErrInvalidDomainValue)
	}
	return nil
}

type FlowStateSnapshot struct {
	Version      int                  `json:"version"`
	FlowStatus   FlowStatus           `json:"flow_status"`
	CurrentStage Stage                `json:"current_stage"`
	Stages       map[Stage]StageState `json:"stages"`
	IssuedIDs    []StableID           `json:"issued_ids"`
	Supersession SupersessionLinks    `json:"supersession"`
}

// FlowState has no exported mutable fields. FeatureController and the future
// FeatureRepository recovery operations live in this package and own changes;
// consumers receive defensive snapshots.
type FlowState struct{ snapshot FlowStateSnapshot }

func NewFlowState() FlowState {
	state, err := NewFlowStateFromSnapshot(FlowStateSnapshot{
		Version:      FlowStateVersion,
		FlowStatus:   FlowActive,
		CurrentStage: StageIntent,
		Stages: map[Stage]StageState{
			StageIntent: {Status: StageDrafting, ReviewStatus: ReviewNotStarted, UpstreamHashes: []UpstreamHash{}, Reviews: []ReviewRun{}},
			StageSpec:   {Status: StageNotStarted, ReviewStatus: ReviewNotStarted, UpstreamHashes: []UpstreamHash{}, Reviews: []ReviewRun{}},
			StagePlan:   {Status: StageNotStarted, ReviewStatus: ReviewNotStarted, UpstreamHashes: []UpstreamHash{}, Reviews: []ReviewRun{}},
		},
		IssuedIDs: []StableID{},
	})
	if err != nil {
		panic("construct default flow state: " + err.Error())
	}
	return state
}

func NewFlowStateFromSnapshot(snapshot FlowStateSnapshot) (FlowState, error) {
	snapshot = cloneFlowStateSnapshot(snapshot)
	if err := validateFlowStateSnapshot(snapshot); err != nil {
		return FlowState{}, err
	}
	return FlowState{snapshot: snapshot}, nil
}

func (s FlowState) Snapshot() FlowStateSnapshot { return cloneFlowStateSnapshot(s.snapshot) }
func (s FlowState) Status() FlowStatus          { return s.snapshot.FlowStatus }
func (s FlowState) CurrentStage() Stage         { return s.snapshot.CurrentStage }

func (s FlowState) Stage(stage Stage) (StageState, bool) {
	value, ok := s.snapshot.Stages[stage]
	return value.clone(), ok
}

func (s FlowState) IssuedIDs() []StableID {
	return append([]StableID(nil), s.snapshot.IssuedIDs...)
}

func (s FlowState) CanAdvanceTo(next Stage) error {
	if !next.Valid() {
		return domainError("stage", next)
	}
	if s.snapshot.FlowStatus != FlowActive {
		return fmt.Errorf("%w: superseded flow cannot advance", ErrInvalidDomainValue)
	}
	current := s.snapshot.CurrentStage
	if next.order() != current.order()+1 {
		return fmt.Errorf("%w: cannot advance from %s to %s", ErrInvalidDomainValue, current, next)
	}
	if s.snapshot.Stages[current].Status != StageCommitted {
		return fmt.Errorf("%w: %s must be committed before %s", ErrInvalidDomainValue, current, next)
	}
	return nil
}

func (s FlowState) MarshalJSON() ([]byte, error) {
	if err := validateFlowStateSnapshot(s.snapshot); err != nil {
		return nil, err
	}
	return json.Marshal(s.snapshot)
}

func (s *FlowState) UnmarshalJSON(data []byte) error {
	parsed, err := DecodeFlowState(data)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

func DecodeFlowState(data []byte) (FlowState, error) {
	var snapshot FlowStateSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return FlowState{}, fmt.Errorf("decode flow state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return FlowState{}, fmt.Errorf("decode flow state: trailing JSON")
	}
	return NewFlowStateFromSnapshot(snapshot)
}

func validateFlowStateSnapshot(snapshot FlowStateSnapshot) error {
	if snapshot.Version != FlowStateVersion {
		return fmt.Errorf("%w: unsupported flow state version %d", ErrInvalidDomainValue, snapshot.Version)
	}
	if !snapshot.FlowStatus.Valid() || !snapshot.CurrentStage.Valid() {
		return fmt.Errorf("%w: invalid flow status or current stage", ErrInvalidDomainValue)
	}
	if len(snapshot.Stages) != len(stages) {
		return fmt.Errorf("%w: state must contain intent, spec, and plan", ErrInvalidDomainValue)
	}
	for _, stage := range stages {
		state, exists := snapshot.Stages[stage]
		if !exists {
			return fmt.Errorf("%w: missing stage %q", ErrInvalidDomainValue, stage)
		}
		if err := state.Validate(); err != nil {
			return fmt.Errorf("stage %s: %w", stage, err)
		}
	}
	for stage := range snapshot.Stages {
		if !stage.Valid() {
			return domainError("stage", stage)
		}
	}
	seenIDs := make(map[StableID]struct{}, len(snapshot.IssuedIDs))
	for _, id := range snapshot.IssuedIDs {
		if !id.Valid() {
			return domainError("issued ID", id.String())
		}
		if _, exists := seenIDs[id]; exists {
			return fmt.Errorf("%w: duplicate issued ID %s", ErrInvalidDomainValue, id)
		}
		seenIDs[id] = struct{}{}
	}
	if err := snapshot.Supersession.Validate(); err != nil {
		return err
	}
	return nil
}

func cloneFlowStateSnapshot(snapshot FlowStateSnapshot) FlowStateSnapshot {
	value := snapshot
	value.Stages = make(map[Stage]StageState, len(snapshot.Stages))
	for stage, state := range snapshot.Stages {
		value.Stages[stage] = state.clone()
	}
	value.IssuedIDs = append([]StableID(nil), snapshot.IssuedIDs...)
	return value
}
