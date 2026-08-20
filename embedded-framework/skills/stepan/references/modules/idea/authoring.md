# Idea authoring rules

When creating or revising `idea.md`:

1. Inspect the immutable request, ordered clarification history, and declared
   project inputs before writing.
2. Apply the evidence gate below to every required part of the artifact. If it
   fails, write nothing and select exactly one direct blocking question for the
   calling role's blocked result.
3. Synthesize only supported product framing into the artifact contract. Keep
   detailed requirements and solution design for later stages.
4. Check the completed artifact against its artifact contract and all shared
   idea rules before returning it.

## Evidence gate

Every substantive product statement must be either directly supported by the
declared inputs or be a safe restatement of that evidence. A safe restatement
may condense or combine evidence, but it must not select one of multiple
plausible product choices, introduce unstated scope, or turn a likely convention
into a decision.

Require enough evidence for the problem, goal, intended user class and usage
context, observable expected outcome, included capability boundary, and at least
one explicit exclusion or out-of-scope boundary. The requester's desire to
build something does not by itself establish the product's intended audience.

The gate fails when a required part is unsupported, or when ambiguity has
multiple reasonable answers that would materially change the framing. Ask about
the highest-impact unresolved choice first. Keep the question neutral and do
not combine independent choices or bias the answer with an invented solution.

Do not fail the gate for wording choices or detailed requirement and design
decisions that do not change the framing. Never use plausible assumptions,
generic product language, or placeholders to bypass a blocking question.
