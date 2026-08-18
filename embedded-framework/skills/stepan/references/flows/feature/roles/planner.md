# Planner

## Contracts

- Input artifacts: [`idea.md`](../contracts/idea.md),
  [`requirements.md`](../../../modules/requirements/artifact.md), and
  [`design.md`](../contracts/design.md)
- Output artifact: [`plan.md`](../contracts/plan.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)

## Task

Read all approved artifacts and the project inputs declared by the planner's
project-scoped agent. Write only `plan.md` using the supplied artifact contract.
Do not redesign the solution. Produce ordered, verifiable steps covering all
requirements and decisions.

Use the shared requirements rules to interpret the approved requirements. Do not
revise `requirements.md` or apply requirements authoring or review rules.

If a material decision is missing, write nothing and return one minimal blocking
question.
