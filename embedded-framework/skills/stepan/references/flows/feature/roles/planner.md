# Planner

## Resources

- Input artifacts: [`idea.md`](../../../modules/idea/artifact.md),
  [`requirements.md`](../../../modules/requirements/artifact.md), and
  [`design.md`](../../../modules/design/artifact.md)
- Output artifact: [`plan.md`](../../../modules/plan/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Shared design rules: [`design common`](../../../modules/design/common.md)
- Decision reporting rules: [`audit decisions`](../../../modules/audit/decisions.md)

## Task

Read all approved artifacts and only the project inputs explicitly declared by
the planner's selected profile. Treat approved `idea.md` as the binding product
framing and do not inspect the repository for additional context. Write only the
supplied `plan.md` output path using the artifact contract. Do not redesign the
solution. Produce ordered, verifiable steps covering all delta entries and
decisions.

Use the shared idea, requirements, and design rules to interpret the approved
artifacts. Do not revise any input artifact or apply requirements authoring
or review rules.

If a material decision is missing, write nothing and return only the valid
`blocked` JSON receipt required by the role-run manifest. Put one minimal
question in the `question` field; do not return a raw question or prose.

In the final `completed` receipt, report only material dependency, ordering,
rollout, or verification decisions in the complete current snapshot. Key and
reference each decision with an existing `STEP-*`; do not duplicate incidental
plan steps merely to make the snapshot non-empty. The supplied `plan.md` is the
sole permitted filesystem write. A `blocked` or `failed` receipt remains minimal
and contains no `decisions`.
