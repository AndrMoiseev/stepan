package impl_loop

import (
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

// CycleLimits translates effective implementation configuration at the flow
// boundary into the durable state model's reservation limits. Timeouts and
// Explorer response-size limits do not govern attempt reservation.
func CycleLimits(limits implementationconfig.LoopLimits) implementationstate.CycleLimits {
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
