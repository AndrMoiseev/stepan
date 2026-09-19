package impl_loop

import (
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

// CycleLimits translates effective implementation configuration at the flow
// boundary into the durable state model's reservation limits. Timeouts and
// Explorer response-size limits do not govern attempt reservation.
func CycleLimits(limits setting.LoopLimits) implstate.CycleLimits {
	return implstate.CycleLimits{
		AssignmentReview:  limits.ReviewRounds,
		MandatoryChecks:   limits.RequiredCheckAttempts,
		ChecksRequested:   limits.ChecksRequested,
		BriefRefinement:   limits.BriefClarifications,
		Explorer:          limits.ExplorationLimit,
		TechnicalAttempts: limits.TechnicalAttempts,
		FinalReview:       limits.FinalReviewRounds,
	}
}
