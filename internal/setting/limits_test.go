package setting

import (
	"strings"
	"testing"
)

func TestResolveLimitsUsesDesignDefaults(t *testing.T) {
	configuration, err := Merge(sources(t, ``, ``))
	if err != nil {
		t.Fatal(err)
	}
	got, err := configuration.ResolveLimits()
	if err != nil {
		t.Fatal(err)
	}
	want := LoopLimits{
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
	if got != want {
		t.Fatalf("limits = %#v, want %#v", got, want)
	}
}

func TestResolveLimitsAppliesProjectOverrides(t *testing.T) {
	configuration, err := Merge(sources(t, `{
  "limits":{"review_rounds":4,"agent_timeout_seconds":100}
}`, `{
  "limits":{
    "review_rounds":5,
    "required_check_attempts":6,
    "checks_requested":7,
    "brief_clarifications":8,
    "exploration_limit":9,
    "technical_attempts":10,
    "final_review_rounds":11,
    "agent_timeout_seconds":12,
    "explorer_characters":13000
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := configuration.ResolveLimits()
	if err != nil {
		t.Fatal(err)
	}
	want := LoopLimits{
		ReviewRounds:          5,
		RequiredCheckAttempts: 6,
		ChecksRequested:       7,
		BriefClarifications:   8,
		ExplorationLimit:      9,
		TechnicalAttempts:     10,
		FinalReviewRounds:     11,
		AgentTimeoutSeconds:   12,
		ExplorerCharacters:    13000,
	}
	if got != want {
		t.Fatalf("project overrides = %#v", got)
	}
}

func TestResolveLimitsRejectsInvalidKnownValues(t *testing.T) {
	for _, test := range []struct {
		name, value string
	}{
		{"string", `"3"`},
		{"fraction", `3.5`},
		{"zero", `0`},
		{"negative", `-1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration, err := Merge(sources(t, `{"limits":{"review_rounds":`+test.value+`}}`, ``))
			if err != nil {
				t.Fatal(err)
			}
			_, err = configuration.ResolveLimits()
			if err == nil || !strings.Contains(err.Error(), "limits.review_rounds must be a positive integer") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
