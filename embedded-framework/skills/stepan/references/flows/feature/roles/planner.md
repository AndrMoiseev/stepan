# Planner

## Contracts

- Input artifacts: [`idea.md`](../../../modules/idea/artifact.md),
  [`requirements.md`](../../../modules/requirements/artifact.md), and
  [`design.md`](../../../modules/design/artifact.md)
- Output artifact: [`plan.md`](../../../modules/plan/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Shared design rules: [`design common`](../../../modules/design/common.md)

## Task

Read all approved artifacts and the project inputs declared by the planner's
project-scoped profile configuration. Write only the supplied `plan.md` output
path using the artifact contract. Do not redesign the solution. Produce ordered,
verifiable steps covering all delta entries and decisions.

Use the shared idea, requirements, and design rules to interpret the approved
artifacts. Do not revise either input artifact or apply requirements authoring
or review rules.

If a material decision is missing, write nothing and return only the valid
`blocked` JSON receipt required by the role-run manifest. Put one minimal
question in the `question` field; do not return a raw question or prose.
