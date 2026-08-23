# Idea author

## Contracts

- Output artifact: [`idea.md`](../../../modules/idea/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Idea authoring rules: [`idea authoring`](../../../modules/idea/authoring.md)

## Task

Read immutable `request.md`, ordered clarification history, persisted answers,
and only the project inputs explicitly declared by the selected profile. Do not
inspect the repository for additional context. Clarify the requested
outcome and write only the supplied `idea.md` output path using the artifact,
shared, and authoring rules.

If the evidence gate fails, write nothing and return only the valid `blocked`
JSON receipt with exactly one direct product question. Do not write requirements
or design content.
