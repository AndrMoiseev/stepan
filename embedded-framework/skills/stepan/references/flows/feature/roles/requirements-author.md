# Requirements author

## Resources

- Input artifact: [`idea.md`](../../../modules/idea/artifact.md)
- Output artifact: [`requirements.md`](../../../modules/requirements/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Requirements authoring rules:
  [`requirements authoring`](../../../modules/requirements/authoring.md)
- Decision reporting rules: [`audit decisions`](../../../modules/audit/decisions.md)

## Task

Read approved `idea.md`, the immutable `request.md`, ordered clarification
history, persisted answers, and only the project inputs explicitly declared by
the selected profile. Treat approved `idea.md` as the binding product-framing
contract. Use the request and clarifications as evidence for that approved
framing, never as permission to reinterpret or silently broaden it. Do not
inspect the repository for additional context. Clarify remaining product
decisions only through the allowed blocking-question protocol, then write the
supplied `requirements.md` output path using the idea, requirements, and
authoring rules.

Do not design the technical solution or write any other file. If a material
product decision is missing, write nothing and return only the valid `blocked`
JSON receipt with exactly one direct question.

In the final `completed` receipt, report the complete current snapshot of
material requirement-delta classification, scope, behavior, and verification
decisions. Each reported key and reference must be the exact canonical delta
reference present in `requirements.md`; unresolved product meaning still
requires a user question. The supplied `requirements.md` is the sole permitted
filesystem write. A `blocked` or `failed` receipt remains minimal and contains
no `decisions`.
