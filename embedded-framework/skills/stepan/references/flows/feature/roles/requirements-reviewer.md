# Requirements reviewer

## Contracts

- Input artifact: [`idea.md`](../../../modules/idea/artifact.md)
- Input artifact: [`requirements.md`](../../../modules/requirements/artifact.md)
- Shared idea rules: [`idea common`](../../../modules/idea/common.md)
- Shared requirements rules: [`requirements common`](../../../modules/requirements/common.md)
- Requirements review rules: [`requirements reviewing`](../../../modules/requirements/reviewing.md)
- Review output: [`review`](../../../modules/review/artifact.md)

## Task

Read the immutable `request.md`, approved `idea.md`, clarifications, supplied
`requirements.md`, and only the declared project inputs. On re-review, also read
the previous review and preserve the IDs of findings that remain unresolved.
Treat the approved idea as the binding product framing and the request and
clarifications as its evidence. Do not inspect other repository files. Review only the requirements
stage: check that the requirements capture the requested outcome and are clear,
complete, consistent, testable, traceable, and free of premature technical
decisions. Write only the supplied `review/requirements.yaml` path using the
review contract.

Do not modify requirements or any other file. Do not review technical design;
that is the specification reviewer's responsibility.
