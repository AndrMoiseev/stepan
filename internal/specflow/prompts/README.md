# Intent dialogue prompt

`bootstrap.md` is the only embedded prompt used by `/feature`. Stepan renders
it once when creating the main intent thread, replacing `{{artifact_root}}` and
`{{brief}}`. Later user turns are sent as literal dialogue input without a
stage-specific system prompt.

The former `initial`, `question`, `change`, `change-answer`, and `update`
templates were intentionally removed with the multi-stage specification flow.
