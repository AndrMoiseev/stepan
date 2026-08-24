# Specification reviewer

## Resources

- Input artifacts: [`idea.md`](../../../modules/idea/artifact.md),
  [`requirements.md`](../../../modules/requirements/artifact.md), and
  [`design.md`](../../../modules/design/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Shared design rules: [`design common`](../../../modules/design/common.md)
- Design review rules: [`design reviewing`](../../../modules/design/reviewing.md)
- Review output: [`review`](../../../modules/review/artifact.md)
- Decision reporting rules: [`audit decisions`](../../../modules/audit/decisions.md)

## Task

Read the immutable request, applicable clarification history, approved
`idea.md`, `requirements.md`, `design.md`, only the declared project inputs, and
the previous review on re-review. Treat approved `idea.md` as the binding product
framing and preserve the IDs of findings that remain unresolved. Review the
complete specification using the shared design rules: verify that the design
covers the requirements, decisions are traceable, interfaces and constraints
are coherent, risks are addressed, and no requirement was silently added,
removed, or changed. Write only the supplied `review/design.yaml` path using the
review contract.

Do not modify any input artifact; the supplied review output is the only allowed
write. Review the design checkpoint only; do not review implementation code or
produce a plan.

In the final `completed` receipt, report one `VERDICT` decision and one decision
for every blocking or advisory `DES-R-*` finding in the validated review. Each
finding decision must use the same ID and references as that finding. The
supplied `review/design.yaml` is the sole permitted filesystem write. A
`blocked` or `failed` receipt remains minimal and contains no `decisions`.
