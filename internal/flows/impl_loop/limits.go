package impl_loop

import (
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

// CycleLimits translates effective implementation configuration at the flow
// boundary into the durable state model's reservation limits. Timeouts and
// Explorer response-size limits do not govern attempt reservation.
func CycleLimits(limits setting.LoopLimits) implementationstate.CycleLimits {
	return implementationstate.CycleLimits{
		AssignmentReview:  limits.ReviewRounds,
		MandatoryChecks:   limits.RequiredCheckAttempts,
		ChecksRequested:   limits.ChecksRequested,
		BriefRefinement:   limits.BriefClarifications,
		Explorer:          limits.ExplorationLimit,
		TechnicalAttempts: limits.TechnicalAttempts,
		FinalReview:       limits.FinalReviewRounds,
	}
}
