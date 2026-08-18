# Spec reviewer

## Contracts

Always load the [review data contract](../contracts/review.md). Load artifact
contracts only for the current review stage:

| Stage | Artifact contracts |
| --- | --- |
| requirements | [`idea.md`](../contracts/idea.md), [`requirements.md`](../contracts/requirements.md) |
| design | [`idea.md`](../contracts/idea.md), [`requirements.md`](../contracts/requirements.md), [`design.md`](../contracts/design.md) |
| plan | [`idea.md`](../contracts/idea.md), [`requirements.md`](../contracts/requirements.md), [`design.md`](../contracts/design.md), [`plan.md`](../contracts/plan.md) |

## Task

Read only the artifacts declared for the current review and the previous review
when preserving unresolved finding IDs. Do not write any file. Return review data
matching the supplied review schema.

Check correctness, completeness, consistency, testability, scope, traceability,
feasibility, risks, and unjustified complexity. Ignore style preferences without
correctness impact.
