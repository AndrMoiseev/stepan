# Requirements review rules

When reviewing `requirements.md`:

- Evaluate the artifact against its artifact contract, the shared requirements
  rules, approved `idea.md` as binding product framing, the initial request and
  completed clarifications as its evidence, and only the declared project inputs, including the
  declared baseline specification when a delta depends on it.
- Never choose among plausible requirement interpretations or supply a missing
  product decision. If any unresolved uncertainty could affect meaning, emit
  exactly one highest-impact blocking finding with `resolution: user-decision`,
  put one neutral direct question to the user in `recommendation`, and stop the
  review. When unsure whether user input is needed, require it.
- Use `resolution: author-revision` only for an objective defect whose correction
  is fully determined by approved inputs and requires no product judgment.
- Report a blocking finding for a missing in-scope obligation, conflicting or
  invented behavior, an incorrect delta operation, an empty delta section, or a
  broken trace.
- For `ADDED` and `MODIFIED`, report a blocking finding for a missing or
  non-atomic `SHALL` statement, a duplicate or invalid requirement name, a
  missing scenario, an untestable scenario, or a scenario that does not
  exercise its requirement. Also block an `ADDED` requirement that already
  exists in the baseline or a `MODIFIED` requirement that is neither present in
  the baseline nor the target of a valid `RENAMED` entry. Also block a
  `MODIFIED` entry that supplies only a partial requirement block.
- For `REMOVED`, require the exact baseline name plus non-empty `Reason` and
  `Migration`. For `RENAMED`, require an exact baseline source, a non-conflicting
  target, and the exact adjacent `FROM`/`TO` form.
- Report a blocking finding when one requirement appears in conflicting delta
  sections, when a renamed source is also removed, when a rename target is also
  added, or when a rename with behavioral changes uses the old name instead of
  the new name under `MODIFIED`.
- Report an advisory finding only for a concrete non-blocking risk. Ignore
  stylistic preferences that do not affect correctness or verification. An
  advisory finding must carry `resolution: none`.
- Keep each finding focused on one independent problem and cite the canonical
  delta reference from the artifact contract when available.
- Recommend the smallest correction that resolves the problem without designing
  the solution or rewriting the artifact.
- On re-review, read the previous review supplied in feedback and preserve every
  finding ID whose underlying problem remains unresolved. Never allocate a new
  ID merely because the author revised the artifact.

Do not apply requirements authoring rules or modify an artifact. Write only the
supplied review output file.
