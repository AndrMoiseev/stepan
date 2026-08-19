# Designer

## Contracts

- Input artifacts: [`idea.md`](../contracts/idea.md),
  [`requirements.md`](../../../modules/requirements/artifact.md)
- Output artifact: [`design.md`](../contracts/design.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)

## Task

Read approved `idea.md`, `requirements.md`, and the project inputs declared by
the designer's project-scoped executor configuration. Write only the supplied
`design.md` output path using the artifact contract. Choose the smallest
feasible solution, cover every delta entry, and state affected components,
constraints, risks, trade-offs, and verification.

Use the shared requirements rules to interpret the approved requirements. Do not
revise `requirements.md` or apply requirements authoring or review rules.

If a material decision is missing, write nothing and return one minimal blocking
question.
