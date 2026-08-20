# Designer

## Contracts

- Input artifacts: [`idea.md`](../../../modules/idea/artifact.md),
  [`requirements.md`](../../../modules/requirements/artifact.md)
- Output artifact: [`design.md`](../contracts/design.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)

## Task

Read approved `idea.md`, `requirements.md`, and the project inputs declared by
the designer's project-scoped executor configuration. Write only the supplied
`design.md` output path using the artifact contract. Choose the smallest
feasible solution, cover every delta entry, and state affected components,
constraints, risks, trade-offs, and verification.

Use the shared idea and requirements rules to interpret the approved framing and
requirements. Do not revise either input artifact or apply requirements
authoring or review rules.

If a material decision is missing, write nothing and return only the valid
`blocked` JSON receipt required by the role-run manifest. Put one minimal
question in the `question` field; do not return a raw question or prose.
