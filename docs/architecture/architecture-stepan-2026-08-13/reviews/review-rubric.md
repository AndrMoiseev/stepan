# Reviewer gate: good-spine rubric

## Verdict

**Needs revision before handoff.** The spine is strong at initiative altitude and covers the product's main divergence points, but its crash-safety protocol and source-blind execution boundary still contain rules that two compliant implementations could interpret differently or could not prove after a crash.

## Critical findings

None.

## High findings

### H1. “One writer” is a convention, not an enforced invariant

- **Location:** `Consistency Conventions`; AD-2.
- **Problem:** the document says that one process owns a run and concurrent writes are unsupported, but it does not require `runstore` to reject a second writer. Two Stepan processes can therefore both satisfy the prose while allocating the same `seq` or interleaving JSONL writes. This defeats the sole durable source of truth.
- **Why it matters:** accidental concurrent invocation is possible even before task parallelism exists; deferring worktree parallelism does not remove this data-integrity race.
- **Disposition:** **autofix**. Require an exclusive per-run writer lock/lease before the first append, fail closed if it cannot be acquired, and release it only on orderly close. The exact cross-platform locking primitive can remain an implementation detail.

### H2. The durable commit protocol cannot distinguish every state it names

- **Location:** AD-2, AD-3, AD-4.
- **Problem:** “append + `fsync` is commit” is not observable after a restart: a complete newline-delimited record may reach disk even if the process died before learning whether `fsync` completed. Conversely, an event can be durable while its referenced evidence file is absent or not durable because evidence write ordering and validation are unspecified. The text distinguishes an uncommitted truncated tail from a damaged committed record without defining the framing rule that makes that distinction.
- **Why it matters:** recovery implementations can legitimately disagree about whether to replay or discard the last complete record and whether missing/hash-mismatched evidence blocks replay.
- **Disposition:** **autofix**. Define the recoverable on-disk rule, not the syscall acknowledgement: evidence is written and synced before its referencing event; a complete newline-terminated JSON record visible after restart is replayable; only a non-terminated final fragment is discarded; missing or SHA-mismatched referenced evidence fails closed. If atomic evidence replacement is required, state temp-write/sync/rename ordering explicitly.

### H3. The source-blind guarantee has no unambiguous enforcement owner

- **Location:** AD-5, AD-8, AD-9; dependency diagram and Structural Seed.
- **Problem:** AD-8 requires OS-inaccessible source plus preventive network/read restrictions, while AD-9 gives `processcontrol` only lifecycle mechanisms (Job Object or process group/signals), and adapters are only required to reject an unenforceable policy. The spine does not say which component proves filesystem/network capabilities, which result is returned to `execution`, or which live canary gates support. A Windows Job Object and a macOS process group do not themselves provide those access controls.
- **Why it matters:** one implementation can treat a separate directory as sufficient while another requires an OS security principal/sandbox, even though AD-8 explicitly rejects the former.
- **Disposition:** **discuss, then fix in spine**. Keep the minimal package map, but assign ownership: `execution` must obtain a positive, provider/platform-specific enforcement result before launch; `processcontrol` owns only process-tree lifecycle; an adapter/platform combination is unsupported for a policy until the source and network canaries pass live. No new generic sandbox framework is required.

### H4. Deferred extension policy can change observable security semantics today

- **Location:** AD-8; `Deferred Decisions` item for user extensions.
- **Problem:** the three deferred choices—clean profile, allowlisted skills, inherited profile—have materially different read/execute/network surfaces. AD-8 fixes a Codex baseline but provides no corresponding release gate for Claude Code or a future execution-profile feature. Two units can therefore both claim conformance while one loads user skills and the other does not.
- **Why it matters:** the reviewer rubric permits deferral only when the deferral cannot create divergent downstream behavior.
- **Disposition:** **defer with an explicit gate**. Do not choose among the three options here; state that no user-extension/execution-profile capability may be enabled or declared supported for an adapter until that decision and its live enforcement test exist. Existing Codex containment remains the only grandfathered baseline.

### H5. The Stack contains an unresolved placeholder as if it were a selected technology version

- **Location:** `Stack`, Claude Code row.
- **Problem:** `minimum/verified pending live conformance` is not a version and leaves the versioned Stack mechanically incomplete. The following paragraph and Deferred section already express the correct state: Claude Code is a target adapter, not a verified stack entry.
- **Why it matters:** a builder cannot determine whether any installed Claude Code version is admissible, and a placeholder can survive into a supposedly final spine.
- **Disposition:** **autofix**. Remove Claude Code from the versioned Stack until the first live conformance run establishes a minimum; retain AD-5/AD-12 and the Deferred entry so the target architecture remains explicit.

## Medium findings

### M1. Event-schema upgrade compatibility is silent

- **Location:** AD-2; Stack (`event schema v1`).
- **Problem:** unknown versions fail closed, but the spine does not say whether a later Stepan binary must replay v1, migrate a copy, or refuse without mutation. This is an operational divergence for durable runs across upgrades.
- **Disposition:** **autofix**. Add the minimal invariant: binaries never rewrite an unknown/older run in place; they either support replay of that schema or refuse before mutation. Design migrations only when v2 exists.

### M2. “Secrets are never written” is not presently enforceable

- **Location:** AD-3; AD-14.
- **Problem:** prompts, model output and logs may contain secret-like data even when production credentials are out of scope. No deterministic classifier can prove their absence, yet the rule is absolute.
- **Disposition:** **discuss**. Either define concrete prevention/redaction at every persistence boundary, or narrow the invariant to credentials controlled by Stepan and classify `.stepan` evidence as sensitive project-local data with restrictive permissions. Do not promise detection that cannot be proven.

### M3. Git CLI is a load-bearing external dependency but has no target contract

- **Location:** AD-15; Structural Seed; Stack.
- **Problem:** candidate capture, diff verification and commits depend on Git behavior, but the target Stack does not state that `gitrepo` uses Git CLI, nor a minimum/preflight policy. ADR 0002 records the current mechanism only as baseline.
- **Disposition:** **autofix**. Add Git CLI to the Stack with the current verified version if known, or move its minimum/version verification to an explicit Deferred item and require preflight refusal until established. Do not introduce a Git library abstraction.

### M4. AD-1's import rule and diagram admit two dependency shapes

- **Location:** AD-1; dependency diagram.
- **Problem:** the diagram has `workflow --> runstore` and `workflow --> execution`, while AD-1 says concrete implementations are bound only by `cmd/stepan`. It is unclear whether workflow imports concrete packages, consumer-owned interfaces, or pure domain types from them.
- **Disposition:** **autofix**. Clarify that `cmd/stepan` constructs concrete collaborators, while `workflow` depends only on its consumer-owned minimal seams/domain contracts; `runstore` remains concrete and need not gain a storage abstraction prematurely.

### M5. Durable data lifecycle is omitted from the operational envelope

- **Location:** AD-3; Structural Seed; operational scope.
- **Problem:** evidence can grow indefinitely and may be sensitive, but retention, automatic cleanup and ownership are silent.
- **Disposition:** **autofix**. For the simple local CLI, state that Stepan never automatically deletes run/evidence data; explicit cleanup can be designed when retention becomes a real feature. This prevents one implementation from silently pruning recovery evidence.

## Low findings

### L1. Final document state is still `draft`

- **Location:** frontmatter.
- **Disposition:** **autofix at handoff** after accepted reviewer fixes.

### L2. `Close()` lifecycle details belong to the first runner specification

- **Location:** AD-5.
- **Problem:** the architecture does not state whether `Close` is idempotent or returns an error. This need not expand the spine, but the first common-runner specification must settle it consistently for both adapters.
- **Disposition:** **defer to feature specification**; no spine change required.

## Checklist assessment

- **Real divergence points:** mostly covered: workflow authority, approval gates, role/session isolation, adapter neutrality, process lifecycle, workspace roots, version policy, candidate identity, commit authority and recovery are all explicit.
- **Rule enforceability:** generally good; H1–H4 and M2 are the exceptions.
- **Deferred safety:** YAGNI deferrals for parallelism, storage abstraction, snapshots, DSL and plugin protocol are safely gated. User extensions need the H4 release gate.
- **Named technology reality:** Go, huh and Codex versions are ratified by the brownfield ADR; Claude Code is correctly described as documentation-designed but should not appear as a versioned Stack row yet. Git compatibility remains unstated.
- **Brownfield fit:** good. The spine explicitly distinguishes current MVP facts from target invariants and avoids pretending migration already happened.
- **Product capability coverage:** good. Specification, planning, approval, execution, deterministic checks, independent verification, bounded rework, commit, final verification and recovery all have architectural homes.
- **Operational/environmental envelope:** local single-process deployment, Windows/macOS support gates and absence of daemon/database/direct model API are decided. Durable-data retention and source/network enforcement ownership need the fixes above.
- **Scope discipline:** good. The structural seed is lean, and speculative workflow/storage/plugin abstractions are deferred behind evidence-based triggers.

## Suggested gate result

Apply H1, H2, H4 and H5 as clear text fixes. Resolve H3 before declaring macOS or a source-blind adapter/platform pair supported. M1, M3, M4 and M5 are compact clarifications worth applying now; M2 needs an explicit scope choice rather than an unverifiable absolute claim.

## Resolution check

H1, H3, H4 and H5 are resolved: `runstore` now enforces an exclusive OS lock; `execution` owns the closed `AccessPolicy` boundary; extension profiles have an explicit release gate; and Claude Code is no longer presented as a versioned Stack entry. H2 is almost resolved by LF framing and evidence-before-event publication, but one enforceable outcome remains unstated: AD-3 must say that missing evidence or a SHA-256 mismatch stops replay/use fail-closed. Merely saying that the hash is checked still permits a warning-and-continue implementation.

**Final resolution: PASS.** AD-3 now makes evidence publication durable before the referencing event and explicitly stops replay/effect fail-closed on a missing file or SHA-256 mismatch. No clear fixes remain from this review.
