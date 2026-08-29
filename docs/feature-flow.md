# Intent flow `/feature`

`/feature` ведёт один продолжительный диалог об intent — проблеме, наблюдаемом
результате, границах scope и существенных ограничениях. Он не создаёт
техническую спецификацию, дизайн или план реализации и никогда не делает commit.

```mermaid
stateDiagram-v2
    [*] --> Main
    Main --> Id: /feature + brief
    Id --> Dialogue: dated feature directory and mem-log.md
    Dialogue --> Dialogue: message
    Dialogue --> Published: first draft is published
    Published --> Published: message
    Published --> Review: later draft
    Review --> Published: apply or reject
    Review --> Rework: rework
    Rework --> Review: revised draft
    Published --> Main: /approve
```

Stepan first asks a small read-only request for a semantic `feature_id`, then
creates `docs/changes/features/YYYY-MM-DD-<feature-id>/`. A collision receives
the first free suffix (`-2`, `-3`, …). Before the main dialogue starts the
directory contains `mem-log.md` with the literal brief and ID event.

The main thread has one immutable policy for its whole lifetime: Git workspace
is read-only; a unique external artifact root is read-write; all other paths,
network, and commands are denied. The agent writes only
`<artifact-root>/intent.md`; Stepan is the only writer of project `intent.md`
and `mem-log.md`.

Agent turns return exactly one of:

```json
{"kind":"message","message":"…","decisions":[]}
{"kind":"draft","decisions":[]}
```

The first valid draft is published immediately. Later drafts display a unified
diff and wait for `apply`, `reject`, or `rework`; a rework comment returns to
the same thread. `/approve` is available only after publication. It appends the
approval event, closes the flow, deletes its artifact root, and returns to the
main prompt. Interrupted and failed flows retain project artifacts but are not
resumed automatically.
