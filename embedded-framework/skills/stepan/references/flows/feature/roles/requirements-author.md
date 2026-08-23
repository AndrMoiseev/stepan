# Requirements author

## Contracts

- Input artifact: [`idea.md`](../../../modules/idea/artifact.md)
- Output artifact: [`requirements.md`](../../../modules/requirements/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Requirements authoring rules: [`requirements authoring`](../../../modules/requirements/authoring.md)

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
