# Specifier

## Contracts

- Input artifact: [`idea.md`](../../../modules/idea/artifact.md)
- Output artifact: [`requirements.md`](../../../modules/requirements/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Requirements role rules: [`requirements authoring`](../../../modules/requirements/authoring.md)

## Task

Read approved `idea.md` and the project inputs declared by the specifier's
project-scoped executor configuration. Use the shared idea rules to interpret
the approved framing, then follow the requirements artifact contract, shared
rules, and authoring rules. Write only the supplied `requirements.md` output
path. Do not apply requirements review rules.

If any unresolved uncertainty could affect the requirements, write nothing and
return only the valid `blocked` JSON receipt required by the role-run manifest.
Put exactly one direct question in the `question` field; do not return a raw
question or prose. Never choose a product interpretation or default on the
user's behalf.
