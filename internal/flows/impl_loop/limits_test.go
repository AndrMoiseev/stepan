package impl_loop

import (
	"testing"

	"github.com/AndrMoiseev/stepan/internal/setting"
)

func TestCycleLimitsMapsEffectiveConfiguration(t *testing.T) {
	got := CycleLimits(setting.DefaultLoopLimits())
	if got.AssignmentReview != 3 || got.MandatoryChecks != 3 || got.ChecksRequested != 5 || got.BriefRefinement != 3 || got.Explorer != 10 || got.TechnicalAttempts != 3 || got.FinalReview != 3 {
		t.Fatalf("cycle limits = %#v", got)
	}
}
