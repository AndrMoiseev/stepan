# Spec reviewer

## Contracts

Always load the [review data contract](../contracts/review.md). Load only the
artifact contracts and private modules listed for the current review stage:

| Stage | Contracts and modules |
| --- | --- |
| requirements | [`idea.md`](../../../modules/idea/artifact.md), [`idea common`](../../../modules/idea/common.md), [`requirements.md`](../../../modules/requirements/artifact.md), [`requirements common`](../../../modules/requirements/common.md), [`requirements reviewing`](../../../modules/requirements/reviewing.md) |
| design | [`idea.md`](../../../modules/idea/artifact.md), [`idea common`](../../../modules/idea/common.md), [`requirements.md`](../../../modules/requirements/artifact.md), [`requirements common`](../../../modules/requirements/common.md), [`design.md`](../contracts/design.md) |
| plan | [`idea.md`](../../../modules/idea/artifact.md), [`idea common`](../../../modules/idea/common.md), [`requirements.md`](../../../modules/requirements/artifact.md), [`requirements common`](../../../modules/requirements/common.md), [`design.md`](../contracts/design.md), [`plan.md`](../contracts/plan.md) |

## Task

Read only the artifacts and private modules declared for the current review, the
project inputs declared by the reviewer's project-scoped executor configuration,
and the previous review when preserving unresolved finding IDs. Write review
data matching the supplied review schema only to the supplied
`review/<stage>.yaml` output path. Do not modify an artifact or any other file.

Apply shared idea rules at every stage. Apply requirements review rules only at
the `requirements` stage. Never apply idea or requirements authoring rules.

Check correctness, completeness, consistency, testability, scope, traceability,
feasibility, risks, and unjustified complexity. Ignore style preferences without
correctness impact.
