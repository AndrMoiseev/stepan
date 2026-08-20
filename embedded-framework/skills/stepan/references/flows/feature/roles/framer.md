# Framer

## Contracts

- Output artifact: [`idea.md`](../../../modules/idea/artifact.md)
- Shared rules: [`idea common`](../../../modules/idea/common.md)
- Role rules: [`idea authoring`](../../../modules/idea/authoring.md)

## Task

Read the immutable `request.md`, ordered clarification history, any supplied
persisted answer to your previous blocking question, and the project inputs
declared by the framer's project-scoped executor configuration. Write only the
supplied `idea.md` output path using the artifact contract, shared rules, and
authoring rules. Do not inspect parent chat, design the solution, or produce
detailed requirements.

If the authoring evidence gate fails, write nothing and return only the valid
`blocked` JSON receipt required by the role-run manifest. Put its one required
question in the `question` field; do not return a raw question or prose.
