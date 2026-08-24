# Technical design author

## Resources

- Input artifact: [`idea.md`](../../../modules/idea/artifact.md)
- Input artifact: [`requirements.md`](../../../modules/requirements/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Shared design rules: [`design common`](../../../modules/design/common.md)
- Design authoring rules: [`design authoring`](../../../modules/design/authoring.md)
- Output artifact: [`design.md`](../../../modules/design/artifact.md)
- Decision reporting rules: [`audit decisions`](../../../modules/audit/decisions.md)

## Task

Read approved `idea.md` as the binding product framing, approved
`requirements.md` as the binding behavior contract, the immutable request and
clarifications as evidence for those approved contracts, and only the project
inputs declared by the selected profile. Do not inspect the repository for
additional context. On revision, also read the previous design review and its
unresolved finding IDs. Follow the shared design rules and write only the
supplied `design.md` output path using the design contract. Make supported
technical decisions within the approved product intent, choose the smallest
feasible solution, cover every requirement delta, and state affected
components, constraints, risks, trade-offs, and verification.

Do not revise requirements or write any other file. Do not ask the user to make
a technical choice that the approved requirements and declared evidence let the
design author decide. A blocking question is allowed only when the solution
depends on missing product intent, unavailable required evidence, or authority
the role does not have. Then write nothing and return only the valid `blocked`
JSON receipt with exactly one direct question.

In the final `completed` receipt, report the complete current design decision
snapshot: every `DES-*` in the validated `design.md` appears exactly once, with
a `technical`, `compatibility`, or `risk` kind and only existing `DES-*`
references. The supplied `design.md` is the sole permitted filesystem write. A
`blocked` or `failed` receipt remains minimal and contains no `decisions`.
