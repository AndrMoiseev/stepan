package specflow

import "fmt"

// StagePolicy contains the fixed control-plane differences between author
// stages. Callers may inspect a copy, but cannot replace the policy used by a
// StageEngine.
type StagePolicy struct {
	Stage            Stage
	AuthorRole       Role
	ArtifactFilename string
	DocumentKind     DocumentKind
	ParserMode       DocumentValidationMode
	UpstreamStages   []Stage
	ReviewAvailable  bool
	Commands         []string
}

func (p StagePolicy) clone() StagePolicy {
	p.UpstreamStages = append([]Stage(nil), p.UpstreamStages...)
	p.Commands = append([]string(nil), p.Commands...)
	return p
}

var fixedStagePolicies = map[Stage]StagePolicy{
	StageIntent: {
		Stage: StageIntent, AuthorRole: RoleIntentAuthor, ArtifactFilename: "intent.md",
		DocumentKind: DocumentIntent, ParserMode: ValidateDraft,
		UpstreamStages: []Stage{}, ReviewAvailable: false,
		Commands: []string{"/approve", "/status", "/exit"},
	},
	StageSpec: {
		Stage: StageSpec, AuthorRole: RoleSpecAuthor, ArtifactFilename: "spec.md",
		DocumentKind: DocumentSpec, ParserMode: ValidateDraft,
		UpstreamStages: []Stage{StageIntent}, ReviewAvailable: true,
		Commands: []string{"/review", "/approve", "/status", "/exit"},
	},
	StagePlan: {
		Stage: StagePlan, AuthorRole: RolePlanAuthor, ArtifactFilename: "plan.md",
		DocumentKind: DocumentPlan, ParserMode: ValidateDraft,
		UpstreamStages: []Stage{StageIntent, StageSpec}, ReviewAvailable: true,
		Commands: []string{"/review", "/approve", "/revise-spec", "/status", "/exit"},
	},
}

// PolicyForStage returns the immutable author policy selected by the control
// plane. In particular, intent never advertises an agent review capability.
func PolicyForStage(stage Stage) (StagePolicy, error) {
	policy, ok := fixedStagePolicies[stage]
	if !ok {
		return StagePolicy{}, fmt.Errorf("stage policy: %w", domainError("stage", stage))
	}
	return policy.clone(), nil
}
