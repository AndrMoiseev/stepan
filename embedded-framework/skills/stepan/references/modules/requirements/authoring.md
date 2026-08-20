# Requirements authoring rules

When creating or revising `requirements.md`:

1. Inspect the declared inputs for unresolved uncertainty before writing. If
   any doubt could change requirement meaning, stop without creating or
   modifying `requirements.md` and select exactly one direct blocking question
   for the calling role's blocked result, choosing the highest-impact unresolved
   doubt first.
2. Extract the actors, triggers, states, required responses, constraints,
   boundaries, and assumptions from the declared inputs.
3. Compare the intended future behavior with the declared baseline
   specification and classify each change as `ADDED`, `MODIFIED`, `REMOVED`, or
   `RENAMED`. Never use `ADDED` for behavior already present in the baseline.
4. Split independent obligations into separate requirements. Do not combine
   behavior that can fail or be accepted independently.
5. For each `ADDED` or `MODIFIED` entry, give the requirement a concise, unique,
   behavior-oriented name followed immediately by one `SHALL` statement.
6. For `MODIFIED`, copy the complete requirement under its exact baseline name
   and edit it into its full future form. When it is also renamed, use the exact
   `RENAMED` target name instead. Do not emit a partial patch.
7. Add at least one concrete `Scenario` to every `ADDED` or `MODIFIED`
   requirement with `WHEN` and `THEN` steps. Add `GIVEN` only for required
   initial state and `AND` only for additional steps.
8. Add scenarios for important success, edge, and error cases without creating
   redundant examples.
9. Use `REMOVED` only for an exact baseline requirement and supply a concrete
   `Reason` and `Migration`.
10. Use `RENAMED` only for the naming part of a change and preserve the exact old
   and new headings in the `FROM`/`TO` pair. If behavior is unchanged, the
   rename stands alone. If behavior changes too, also put the complete updated
   requirement in `MODIFIED` under the new name.
11. Omit unused delta sections. Reject conflicting entries, including a renamed
    source that is also removed or a rename target that is also added.
12. Check the completed artifact against the artifact contract and all shared
    rules before returning it.

Never make a product decision on the user's behalf. Missing baseline evidence,
conflicting sources, multiple plausible interpretations, or uncertainty about
whether a detail matters all require the same stop-and-question behavior.
