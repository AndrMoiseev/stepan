# Requirements author

## Contracts

- Input artifact: [`idea.md`](../../../modules/idea/artifact.md)
- Output artifact: [`requirements.md`](../../../modules/requirements/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Requirements authoring rules: [`requirements authoring`](../../../modules/requirements/authoring.md)

## Task

Read approved `idea.md`, the immutable `request.md`, ordered clarification
history, persisted answers, and project inputs declared by the selected profile.
Clarify remaining product decisions only through the allowed blocking-question
protocol, then write the supplied `requirements.md` output path using the idea,
requirements, and authoring rules.

Do not design the technical solution or write any other file. If a material
product decision is missing, write nothing and return only the valid `blocked`
JSON receipt with exactly one direct question.
