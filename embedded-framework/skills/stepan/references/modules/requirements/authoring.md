# Requirements authoring rules

When creating or revising `requirements.md`:

1. Extract the actors, triggers, states, required responses, constraints,
   boundaries, and assumptions from the declared inputs.
2. Split independent obligations into separate requirements. Do not combine
   behavior that can fail or be accepted independently.
3. Express each statement using the EARS-like form defined by the shared rules.
4. Write one observable verification criterion for each requirement.
5. Preserve stable IDs during revision and append new IDs without filling gaps.
6. Check the completed artifact against the artifact contract and all shared
   rules before returning it.

If a material decision is missing, write nothing and return one minimal blocking
question.
