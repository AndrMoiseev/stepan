# Specification reviewer

## Contracts

- Input artifacts: [`idea.md`](../../../modules/idea/artifact.md), [`requirements.md`](../../../modules/requirements/artifact.md), [`design`](../contracts/design.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Shared design rules: [`design common`](../../../modules/design/common.md)
- Review output: [`review`](../contracts/review.md)

## Task

Read approved `idea.md`, `requirements.md`, `design.md`, the declared project inputs, and
the previous review when preserving unresolved finding IDs. Review the complete
specification using the shared design rules: verify that the design covers the
requirements, decisions are traceable, interfaces and constraints are coherent,
risks are addressed, and no requirement was silently added, removed, or changed.
Write only the supplied
`review/design.yaml` path using the review contract.

Do not modify either artifact or any other file. Review the design checkpoint
only; do not review implementation code or produce a plan.
