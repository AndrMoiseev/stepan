package implementationconfig

import (
	"encoding/json"
	"fmt"
)

const (
	limitReviewRounds          = "review_rounds"
	limitRequiredCheckAttempts = "required_check_attempts"
	limitChecksRequested       = "checks_requested"
	limitBriefClarifications   = "brief_clarifications"
	limitExplorationLimit      = "exploration_limit"
	limitTechnicalAttempts     = "technical_attempts"
	limitFinalReviewRounds     = "final_review_rounds"
	limitAgentTimeoutSeconds   = "agent_timeout_seconds"
	limitExplorerCharacters    = "explorer_characters"
)

// LoopLimits contains the effective per-cycle limits. There is deliberately
// no aggregate agent-call budget.
type LoopLimits struct {
	ReviewRounds          int
	RequiredCheckAttempts int
	ChecksRequested       int
	BriefClarifications   int
	ExplorationLimit      int
	TechnicalAttempts     int
	FinalReviewRounds     int
	AgentTimeoutSeconds   int
	ExplorerCharacters    int
}

// DefaultLoopLimits returns the agreed defaults for a new implementation
// loop. Check-specific timeouts remain part of project check definitions.
func DefaultLoopLimits() LoopLimits {
	return LoopLimits{
		ReviewRounds:          3,
		RequiredCheckAttempts: 3,
		ChecksRequested:       5,
		BriefClarifications:   3,
		ExplorationLimit:      10,
		TechnicalAttempts:     3,
		FinalReviewRounds:     3,
		AgentTimeoutSeconds:   1800,
		ExplorerCharacters:    12000,
	}
}

// ResolveLimits applies the merged user and project limit declarations to the
// defaults. Known limits must be positive integers; unrecognised limit names
// are preserved by Merge for future independent configuration consumers.
func (configuration Configuration) ResolveLimits() (LoopLimits, error) {
	limits := DefaultLoopLimits()
	fields := []struct {
		name   string
		target *int
	}{
		{limitReviewRounds, &limits.ReviewRounds},
		{limitRequiredCheckAttempts, &limits.RequiredCheckAttempts},
		{limitChecksRequested, &limits.ChecksRequested},
		{limitBriefClarifications, &limits.BriefClarifications},
		{limitExplorationLimit, &limits.ExplorationLimit},
		{limitTechnicalAttempts, &limits.TechnicalAttempts},
		{limitFinalReviewRounds, &limits.FinalReviewRounds},
		{limitAgentTimeoutSeconds, &limits.AgentTimeoutSeconds},
		{limitExplorerCharacters, &limits.ExplorerCharacters},
	}
	for _, field := range fields {
		raw, ok := configuration.Limits[field.name]
		if !ok {
			continue
		}
		var value int
		if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
			return LoopLimits{}, fmt.Errorf("implementation configuration: limits.%s must be a positive integer", field.name)
		}
		*field.target = value
	}
	return limits, nil
}
