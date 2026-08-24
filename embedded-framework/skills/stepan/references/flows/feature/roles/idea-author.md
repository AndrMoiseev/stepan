# Idea author

## Resources

- Output artifact: [`idea.md`](../../../modules/idea/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Idea authoring rules: [`idea authoring`](../../../modules/idea/authoring.md)
- Decision reporting rules: [`audit decisions`](../../../modules/audit/decisions.md)

## Task

Read immutable `request.md`, ordered clarification history, persisted answers,
and only the project inputs explicitly declared by the selected profile. Do not
inspect the repository for additional context. Clarify the requested outcome
and write only the supplied `idea.md` output path using the artifact, shared,
and authoring rules.

If the evidence gate fails, write nothing and return only the valid `blocked`
JSON receipt with exactly one direct product question. Do not write requirements
or design content.

In the final `completed` receipt, report every material normalization of the
user's framing as a current `IDEA-*` decision snapshot. Normally these are
`authority: user` decisions linked to the supplied initial-request or
clarification event keys; do not turn routine phrasing into a decision or invent
an agent-owned product choice. The supplied `idea.md` is the sole permitted
filesystem write. A `blocked` or `failed` receipt remains minimal and contains
no `decisions`.
