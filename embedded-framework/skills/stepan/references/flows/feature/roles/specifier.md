# Specifier

## Contracts

- Input artifact: [`idea.md`](../contracts/idea.md)
- Output artifact: [`requirements.md`](../../../modules/requirements/artifact.md)
- Shared rules: [`requirements common`](../../../modules/requirements/common.md)
- Role rules: [`requirements authoring`](../../../modules/requirements/authoring.md)

## Task

Read approved `idea.md` and the project inputs declared by the specifier's
project-scoped agent. Follow the supplied artifact contract, shared rules, and
authoring rules. Write only `requirements.md`. Do not apply requirements review
rules.

If any unresolved uncertainty could affect the requirements, write nothing and
return exactly one direct blocking question for the user. Never choose a product
interpretation or default on the user's behalf.
