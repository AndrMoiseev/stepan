#!/usr/bin/env -S uv run --no-project --no-python-downloads --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Deterministic helpers for the Stepan router."""

from __future__ import annotations

import argparse
import copy
import datetime as dt
import hashlib
import json
import os
import re
import subprocess
import sys
import tomllib
from collections.abc import Callable
from pathlib import Path, PurePosixPath
from tempfile import TemporaryDirectory, mkstemp


SPEC_ID_PATTERN = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")
LOCAL_ID_PATTERN = re.compile(r"[a-z][a-z0-9]*(?:-[a-z0-9]+)*")
HASH_PATTERN = re.compile(r"sha256:[0-9a-f]{64}")
TIMESTAMP_PATTERN = re.compile(
    r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z"
)
STAGES = ("idea", "requirements", "design", "plan")
STATUSES = (
    "drafting",
    "reviewing",
    "revising",
    "awaiting-approval",
    "awaiting-decision",
    "waiting-executor",
    "approved",
)
ROLES = (
    "idea-author",
    "requirements-author",
    "requirements-reviewer",
    "design-author",
    "specification-reviewer",
    "planner",
)
PURPOSES = ("draft", "revise", "review")
ROLE_OWNED_ARTIFACTS = {
    "idea-author": "idea.md",
    "requirements-author": "requirements.md",
    "design-author": "design.md",
    "planner": "plan.md",
}
ADAPTER_KINDS = ("codex", "claude-code", "mailbox")
APPROVAL_ACTIONS = ("continue", "continue-and-commit")
CODEX_HOST = "codex"
CLAUDE_CODE_HOST = "claude-code"
CONTROL_FILE_MAX_BYTES = 256 * 1024
ARTIFACT_MAX_BYTES = 2 * 1024 * 1024
PROJECT_INPUT_MAX_BYTES = 10 * 1024 * 1024

AUDIT_SCHEMA_VERSION = 1
AUDIT_LOG_MAX_BYTES = 10 * 1024 * 1024
AUDIT_MAX_EVENTS_PER_TRANSACTION = 256
AUDIT_MAX_DECISIONS_PER_RECEIPT = 256
AUDIT_MAX_ALTERNATIVES_PER_DECISION = 64
AUDIT_MAX_REFERENCES_PER_DECISION = 256
AUDIT_MAX_TEXT_CHARACTERS = 128 * 1024
AUDIT_MAX_EVENT_KEY_CHARACTERS = 512
AUDIT_STAGES = ("workflow", *STAGES)
AUDIT_KINDS = ("interaction", "decision", "lifecycle")
AUDIT_ACTORS = ("user", "router", *ROLES)
AUDIT_EVENT_ID_PATTERN = re.compile(r"MEM-[0-9]{6,}")
AUDIT_TRANSACTION_ID_PATTERN = re.compile(r"audit-tx-[0-9]{6,}")
AUDIT_EVENT_KEY_PATTERN = re.compile(
    r"[a-z0-9][A-Za-z0-9._:-]*(?:/[A-Za-z0-9][A-Za-z0-9._:-]*)*"
)
AUDIT_RECORDED_AT_PATTERN = TIMESTAMP_PATTERN
AUDIT_EVENT_HASH_SENTINEL = "sha256:" + "0" * 64
AUDIT_HEADER_NOTICE = (
    "This file is append-only. Current product truth remains in the verified feature\n"
    "state and approved specification artifacts.\n\n"
)
USER_REVISION_PENDING_REQUEST = "Apply the submitted user revision feedback."
DESIGN_DECISION_KEY_PATTERN = re.compile(r"DES-[0-9]{3,}")
PLAN_DECISION_KEY_PATTERN = re.compile(r"STEP-[0-9]{3,}")
IDEA_DECISION_KEY_PATTERN = re.compile(r"IDEA-[0-9]{3,}")
REQUIREMENTS_REVIEW_KEY_PATTERN = re.compile(r"REQ-R-[0-9]{3,}")
DESIGN_REVIEW_KEY_PATTERN = re.compile(r"DES-R-[0-9]{3,}")
REQUIREMENT_REFERENCE_PATTERN = re.compile(
    r'(?:ADDED|MODIFIED|REMOVED) Requirement "[^"\r\n]+"'
    r'|RENAMED Requirement "[^"\r\n]+" -> "[^"\r\n]+"'
)

AUDIT_INTERACTION_EVENTS = (
    "initial-request-captured",
    "blocking-question",
    "user-response",
    "clarification-applied",
    "user-question",
    "agent-answer",
    "stage-approved",
    "revision-feedback-submitted",
    "risk-accepted",
    "specification-collision-selected",
    "recovery-selected",
    "flow-stopped",
)
AUDIT_DECISION_EVENTS = (
    "agent-decision-introduced",
    "agent-decision-revised",
    "agent-decision-retired",
)
AUDIT_LIFECYCLE_EVENTS = (
    "specification-created",
    "stage-entered",
    "role-run-reserved",
    "role-run-completed",
    "role-run-blocked",
    "role-run-failed",
    "role-run-interrupted",
    "executor-wait-started",
    "executor-wait-ended",
    "artifact-accepted",
    "review-accepted",
    "automatic-revision-started",
    "automatic-revision-limit-reached",
    "workflow-integrity-failure",
    "upstream-invalidated",
    "checkpoint-commit-completed",
    "checkpoint-commit-failed",
    "workflow-resumed",
    "workflow-completed",
)
AUDIT_EVENTS_BY_KIND = {
    "interaction": AUDIT_INTERACTION_EVENTS,
    "decision": AUDIT_DECISION_EVENTS,
    "lifecycle": AUDIT_LIFECYCLE_EVENTS,
}

ROLE_DECISION_KINDS = {
    "idea-author": ("framing",),
    "requirements-author": ("classification", "scope", "behavior", "verification"),
    "requirements-reviewer": ("verdict", "finding"),
    "design-author": ("technical", "compatibility", "risk"),
    "specification-reviewer": ("verdict", "finding"),
    "planner": ("dependency", "ordering", "rollout", "verification"),
}
DECISION_AUTHORITIES = ("agent", "user")
DECISION_SEMANTIC_FIELDS = (
    "authority",
    "kind",
    "summary",
    "rationale",
    "alternatives",
    "references",
)
AUDIT_DECISION_RETIREMENT_REASON = (
    "Absent from the accepted full agent-decision snapshot."
)
AUDIT_USER_INPUT_EVENTS = (
    "initial-request-captured",
    "user-response",
)
ROLE_STAGES = {
    "idea-author": "idea",
    "requirements-author": "requirements",
    "requirements-reviewer": "requirements",
    "design-author": "design",
    "specification-reviewer": "design",
    "planner": "plan",
}
AUDIT_RUN_ID_PATTERN = re.compile(
    rf"(?P<specification>{SPEC_ID_PATTERN.pattern})--"
    rf"(?P<stage>{'|'.join(STAGES)})--"
    rf"(?P<role>{'|'.join(ROLES)})--(?P<sequence>[1-9][0-9]*)"
)

# Each payload schema is exact. Values name reusable primitive validators below.
AUDIT_EVENT_PAYLOAD_SCHEMAS = {
    ("interaction", "initial-request-captured"): {
        "request_path": "project_path",
        "request_sha256": "hash",
        "verbatim": "text",
    },
    ("interaction", "blocking-question"): {
        "question": "text",
        "origin": ("author", "review", "router"),
        "resume_purpose": ("draft", "revise"),
    },
    ("interaction", "user-response"): {"verbatim": "text"},
    ("interaction", "clarification-applied"): {
        "key": "text",
        "decision_kind": "decision_kind",
        "summary": "text",
        "rationale": "text",
        "alternatives": "alternatives",
        "references": "references",
        "artifact_path": "project_path",
        "artifact_sha256": "hash",
        "receipt_sha256": "hash",
    },
    ("interaction", "user-question"): {"verbatim": "text"},
    ("interaction", "agent-answer"): {"answer": "text"},
    ("interaction", "stage-approved"): {
        "action": APPROVAL_ACTIONS,
        "verbatim": "nullable_text",
        "artifact_path": "project_path",
        "artifact_sha256": "hash",
        "review_sha256": "nullable_hash",
        "commit_message": "nullable_text",
    },
    ("interaction", "revision-feedback-submitted"): {"verbatim": "text"},
    ("interaction", "risk-accepted"): {
        "risks": "text_list",
        "verbatim": "nullable_text",
    },
    ("interaction", "specification-collision-selected"): {
        "choice": "spec_id",
        "verbatim": "nullable_text",
    },
    ("interaction", "recovery-selected"): {
        "choice": "text",
        "verbatim": "nullable_text",
    },
    ("interaction", "flow-stopped"): {"verbatim": "nullable_text"},
    ("decision", "agent-decision-introduced"): {
        "key": "text",
        "decision_kind": "decision_kind",
        "summary": "text",
        "rationale": "text",
        "alternatives": "alternatives",
        "references": "references",
        "artifact_path": "project_path",
        "artifact_sha256": "hash",
        "receipt_sha256": "hash",
        "semantic_sha256": "hash",
    },
    ("decision", "agent-decision-revised"): {
        "key": "text",
        "decision_kind": "decision_kind",
        "summary": "text",
        "rationale": "text",
        "alternatives": "alternatives",
        "references": "references",
        "artifact_path": "project_path",
        "artifact_sha256": "hash",
        "receipt_sha256": "hash",
        "semantic_sha256": "hash",
        "supersedes_event_id": "event_id",
    },
    ("decision", "agent-decision-retired"): {
        "key": "text",
        "retired_event_id": "event_id",
        "reason": "text",
        "artifact_path": "project_path",
        "artifact_sha256": "hash",
        "receipt_sha256": "hash",
    },
    ("lifecycle", "specification-created"): {
        "specification": "spec_id",
        "request_path": "project_path",
        "request_sha256": "hash",
        "log_path": "project_path",
    },
    ("lifecycle", "stage-entered"): {
        "from_stage": "nullable_stage",
        "reason": ("initial", "approval", "upstream-return", "resume"),
    },
    ("lifecycle", "role-run-reserved"): {
        "purpose": PURPOSES,
        "profile": "text",
        "adapter": ADAPTER_KINDS,
        "configured_agent": "nullable_text",
        "requested_model": "nullable_text",
        "requested_reasoning": "nullable_text",
        "output_path": "project_path",
    },
    ("lifecycle", "role-run-completed"): {
        "receipt_sha256": "hash",
        "effective_model": "runtime_identity",
        "effective_reasoning": "runtime_identity",
    },
    ("lifecycle", "role-run-blocked"): {"receipt_sha256": "hash"},
    ("lifecycle", "role-run-failed"): {
        "receipt_sha256": "hash",
        "error": "text",
    },
    ("lifecycle", "role-run-interrupted"): {"reason": "text"},
    ("lifecycle", "executor-wait-started"): {
        "wait_seconds": "non_negative_int",
    },
    ("lifecycle", "executor-wait-ended"): {
        "outcome": ("completed", "timeout", "malformed-response", "interrupted"),
        "detail": "nullable_text",
    },
    ("lifecycle", "artifact-accepted"): {
        "path": "project_path",
        "sha256": "hash",
        "receipt_sha256": "hash",
    },
    ("lifecycle", "review-accepted"): {
        "path": "project_path",
        "sha256": "hash",
        "receipt_sha256": "hash",
        "verdict": ("pass", "changes-required"),
        "finding_ids": "text_list",
    },
    ("lifecycle", "automatic-revision-started"): {
        "attempt": "positive_int",
        "reason": "text",
    },
    ("lifecycle", "automatic-revision-limit-reached"): {
        "attempt": "positive_int",
        "reason": "text",
    },
    ("lifecycle", "workflow-integrity-failure"): {
        "problem": "text",
        "affected_paths": "project_path_list",
    },
    ("lifecycle", "upstream-invalidated"): {
        "from_stage": "stage",
        "to_stage": "stage",
        "reason": "text",
    },
    ("lifecycle", "checkpoint-commit-completed"): {
        "commit_message": "text",
        "audit_event_id": "event_id",
    },
    ("lifecycle", "checkpoint-commit-failed"): {
        "commit_message": "text",
        "audit_event_id": "event_id",
        "error": "text",
    },
    ("lifecycle", "workflow-resumed"): {"reason": "text"},
    ("lifecycle", "workflow-completed"): {
        "plan_path": "project_path",
        "plan_sha256": "hash",
    },
}

# Outbox validation sorts by rank, kind, event name, and event key. The latter
# fields make same-rank batches deterministic without embedding a second schema.
AUDIT_EVENT_ORDER = {
    ("interaction", "initial-request-captured"): 10,
    ("interaction", "user-response"): 10,
    ("interaction", "user-question"): 10,
    ("interaction", "revision-feedback-submitted"): 10,
    ("interaction", "risk-accepted"): 9,
    ("interaction", "stage-approved"): 10,
    ("interaction", "specification-collision-selected"): 10,
    ("interaction", "recovery-selected"): 10,
    ("interaction", "flow-stopped"): 10,
    ("lifecycle", "specification-created"): 20,
    ("lifecycle", "role-run-reserved"): 20,
    ("lifecycle", "executor-wait-started"): 20,
    ("lifecycle", "executor-wait-ended"): 25,
    ("lifecycle", "role-run-completed"): 30,
    ("lifecycle", "role-run-blocked"): 30,
    ("lifecycle", "role-run-failed"): 30,
    ("lifecycle", "role-run-interrupted"): 30,
    ("interaction", "blocking-question"): 40,
    ("interaction", "agent-answer"): 40,
    ("lifecycle", "artifact-accepted"): 50,
    ("lifecycle", "review-accepted"): 50,
    ("interaction", "clarification-applied"): 60,
    ("decision", "agent-decision-introduced"): 70,
    ("decision", "agent-decision-revised"): 71,
    ("decision", "agent-decision-retired"): 72,
    ("lifecycle", "automatic-revision-started"): 80,
    ("lifecycle", "automatic-revision-limit-reached"): 35,
    ("lifecycle", "workflow-integrity-failure"): 35,
    ("lifecycle", "upstream-invalidated"): 80,
    ("lifecycle", "checkpoint-commit-completed"): 80,
    ("lifecycle", "checkpoint-commit-failed"): 80,
    ("lifecycle", "workflow-resumed"): 80,
    ("lifecycle", "stage-entered"): 90,
    ("lifecycle", "workflow-completed"): 100,
}

# This is the single executable routing/audit matrix.  Existing specialized
# commands and the generic workflow-transition command both validate their
# branch against this table; protocol prose must not maintain a second event
# mapping.  Event values are the complete allowed kind/name set for a branch;
# role-result acceptance additionally permits its declared dynamic decision
# events.
FEATURE_TRANSITION_TABLE = {
    "initialization": {
        "mode": "initialize-feature",
        "handler": None,
        "audited": True,
        "events": (
            "interaction/initial-request-captured",
            "lifecycle/specification-created",
            "lifecycle/stage-entered",
        ),
    },
    "owner-run-reservation": {
        "mode": "reserve-run",
        "handler": None,
        "audited": True,
        "events": ("lifecycle/role-run-reserved",),
    },
    "reviewer-run-reservation": {
        "mode": "reserve-run",
        "handler": None,
        "audited": True,
        "events": ("lifecycle/role-run-reserved",),
    },
    "owner-result-accepted": {
        "mode": "accept-role-result",
        "handler": None,
        "audited": True,
        "dynamic_decisions": True,
        "events": (
            "lifecycle/role-run-completed",
            "lifecycle/artifact-accepted",
            "interaction/clarification-applied",
        ),
    },
    "reviewer-result-accepted": {
        "mode": "accept-role-result",
        "handler": None,
        "audited": True,
        "dynamic_decisions": True,
        "events": (
            "lifecycle/role-run-completed",
            "lifecycle/review-accepted",
            "interaction/blocking-question",
            "lifecycle/automatic-revision-started",
            "lifecycle/automatic-revision-limit-reached",
        ),
    },
    "normalized-clarification-accepted": {
        "mode": "accept-role-result",
        "handler": None,
        "audited": True,
        "events": ("interaction/clarification-applied",),
    },
    "role-blocked": {
        "mode": "workflow-transition",
        "handler": "role-outcome",
        "audited": True,
        "events": (
            "lifecycle/role-run-blocked",
            "interaction/blocking-question",
        ),
    },
    "role-failed": {
        "mode": "workflow-transition",
        "handler": "role-outcome",
        "audited": True,
        "events": ("lifecycle/role-run-failed",),
    },
    "pending-answer": {
        "mode": "workflow-transition",
        "handler": "pending-answer",
        "audited": True,
        "events": ("interaction/user-response",),
    },
    "collision-selection": {
        "mode": "workflow-transition",
        "handler": "collision-selection",
        "audited": True,
        "events": ("interaction/specification-collision-selected",),
    },
    "user-revision": {
        "mode": "workflow-transition",
        "handler": "user-revision",
        "audited": True,
        "events": ("interaction/revision-feedback-submitted",),
    },
    "approve-stage": {
        "mode": "workflow-transition",
        "handler": "approve-stage",
        "audited": True,
        "events": (
            "interaction/risk-accepted",
            "interaction/stage-approved",
            "lifecycle/stage-entered",
            "lifecycle/workflow-completed",
        ),
    },
    "checkpoint-commit-succeeded": {
        "mode": "workflow-transition",
        "handler": "checkpoint-outcome",
        "audited": True,
        "events": (
            "lifecycle/checkpoint-commit-completed",
            "lifecycle/stage-entered",
            "lifecycle/workflow-completed",
        ),
    },
    "checkpoint-commit-failed": {
        "mode": "workflow-transition",
        "handler": "checkpoint-outcome",
        "audited": True,
        "events": ("lifecycle/checkpoint-commit-failed",),
    },
    "artifact-question": {
        "mode": "workflow-transition",
        "handler": "artifact-question",
        "audited": True,
        "events": ("interaction/user-question", "interaction/agent-answer"),
    },
    "flow-stop": {
        "mode": "workflow-transition",
        "handler": "flow-stop",
        "audited": True,
        "events": ("interaction/flow-stopped",),
    },
    "flow-resume": {
        "mode": "workflow-transition",
        "handler": "flow-resume",
        "audited": True,
        "events": (
            "interaction/recovery-selected",
            "lifecycle/workflow-resumed",
        ),
    },
    "mailbox-wait-started": {
        "mode": "workflow-transition",
        "handler": "executor-wait",
        "audited": True,
        "events": ("lifecycle/executor-wait-started",),
    },
    "mailbox-wait-completed": {
        "mode": "workflow-transition",
        "handler": "executor-wait",
        "audited": True,
        "events": ("lifecycle/executor-wait-ended",),
    },
    "mailbox-wait-timeout": {
        "mode": "workflow-transition",
        "handler": "executor-wait",
        "audited": True,
        "events": ("lifecycle/executor-wait-ended",),
    },
    "mailbox-response-malformed": {
        "mode": "workflow-transition",
        "handler": "executor-wait",
        "audited": True,
        "events": ("lifecycle/executor-wait-ended",),
    },
    "mailbox-wait-interrupted": {
        "mode": "workflow-transition",
        "handler": "executor-wait",
        "audited": True,
        "events": ("lifecycle/executor-wait-ended",),
    },
    "native-run-interrupted": {
        "mode": "workflow-transition",
        "handler": "native-interruption",
        "audited": True,
        "events": ("lifecycle/role-run-interrupted",),
    },
    "native-run-recovery": {
        "mode": "workflow-transition",
        "handler": "flow-resume",
        "audited": True,
        "events": (
            "interaction/recovery-selected",
            "lifecycle/workflow-resumed",
        ),
    },
    "upstream-invalidation": {
        "mode": "workflow-transition",
        "handler": "upstream-invalidation",
        "audited": True,
        "events": (
            "interaction/revision-feedback-submitted",
            "lifecycle/upstream-invalidated",
            "lifecycle/stage-entered",
        ),
    },
    "trusted-integrity-pause": {
        "mode": "workflow-transition",
        "handler": "trusted-integrity-pause",
        "audited": True,
        "events": (
            "lifecycle/workflow-integrity-failure",
            "interaction/blocking-question",
        ),
    },
    "status": {
        "mode": "observation",
        "handler": None,
        "audited": False,
        "events": (),
        "reason": "status is a read-only observation",
    },
    "awaiting-checkpoint-presentation": {
        "mode": "observation",
        "handler": None,
        "audited": False,
        "events": (),
        "reason": "presenting an already durable checkpoint is not a decision",
    },
    "approved-outcome-presentation": {
        "mode": "observation",
        "handler": None,
        "audited": False,
        "events": (),
        "reason": "presenting an already durable completion is not a decision",
    },
    "native-response-malformed": {
        "mode": "hard-stop",
        "handler": None,
        "audited": False,
        "events": (),
        "reason": "preserve the active run through the bounded format-repair decision",
    },
    "untrusted-audit-integrity-failure": {
        "mode": "hard-stop",
        "handler": None,
        "audited": False,
        "events": (),
        "reason": "an untrusted audit log must never be appended to",
    },
    "missing-initial-idea": {
        "mode": "pre-state-observation",
        "handler": None,
        "audited": False,
        "events": (),
        "reason": "no specification state exists yet",
    },
}

ROLE_BRIEFS = {
    role: f"references/flows/feature/roles/{role}.md" for role in ROLES
}
STAGE_OWNERS = {
    "idea": "idea-author",
    "requirements": "requirements-author",
    "design": "design-author",
    "plan": "planner",
}
STAGE_REVIEWERS = {
    "requirements": "requirements-reviewer",
    "design": "specification-reviewer",
}


TRANSLITERATION = str.maketrans(
    {
        "а": "a",
        "б": "b",
        "в": "v",
        "г": "g",
        "д": "d",
        "е": "e",
        "ё": "yo",
        "ж": "zh",
        "з": "z",
        "и": "i",
        "й": "y",
        "к": "k",
        "л": "l",
        "м": "m",
        "н": "n",
        "о": "o",
        "п": "p",
        "р": "r",
        "с": "s",
        "т": "t",
        "у": "u",
        "ф": "f",
        "х": "kh",
        "ц": "ts",
        "ч": "ch",
        "ш": "sh",
        "щ": "shch",
        "ъ": "",
        "ы": "y",
        "ь": "",
        "э": "e",
        "ю": "yu",
        "я": "ya",
    }
)


CODEX_AGENT_PROFILES = (
    (
        "orchestrator",
        "gpt-5.6",
        "high",
        "Orchestrates persisted Stepan workflows and dispatches role runs.",
    ),
    (
        "author",
        "gpt-5.6",
        "high",
        "Authors the artifact selected by a workflow role brief.",
    ),
    (
        "architect",
        "gpt-5.6",
        "high",
        "Authors technical designs selected by a workflow role brief.",
    ),
    (
        "planner",
        "gpt-5.6-terra",
        "high",
        "Produces an implementation plan from approved specification artifacts.",
    ),
    (
        "reviewer",
        "gpt-5.6-terra",
        "high",
        "Reviews one Stepan artifact for blocking product and consistency risks.",
    ),
)


CODEX_ROLE_BINDINGS = (
    ("idea-author", "author"),
    ("requirements-author", "author"),
    ("requirements-reviewer", "reviewer"),
    ("design-author", "architect"),
    ("specification-reviewer", "reviewer"),
    ("planner", "planner"),
)


CLAUDE_AGENT_PROFILES = (
    (
        "orchestrator",
        "opus",
        "high",
        "Orchestrates persisted Stepan workflows and dispatches role runs.",
    ),
    (
        "author",
        "opus",
        "high",
        "Authors the artifact selected by a workflow role brief.",
    ),
    (
        "architect",
        "opus",
        "high",
        "Authors technical designs selected by a workflow role brief.",
    ),
    (
        "planner",
        "sonnet",
        "high",
        "Produces an implementation plan from approved specification artifacts.",
    ),
    (
        "reviewer",
        "sonnet",
        "high",
        "Reviews one Stepan artifact for blocking product and consistency risks.",
    ),
)


CLAUDE_ROLE_BINDINGS = CODEX_ROLE_BINDINGS


def codex_agent_instructions(profile: str) -> str:
    if profile == "orchestrator":
        return (
            "Act only as a Stepan workflow orchestrator when given one "
            "router launch manifest.\n"
            "Read and follow every protocol and every audit, execution, router, "
            "or adapter contract declared by that manifest.\n"
            "Never invoke the Stepan skill recursively or treat parent "
            "conversation as product input.\n"
            "Dispatch only the fresh role runs selected by the persisted "
            "workflow state.\n"
            "Return only the exact JSON router result required by the router "
            "contract."
        )
    return """Act only as a Stepan workflow role executor when given one role-run manifest.
Read only the skill resources and project inputs declared by that manifest.
Follow the selected role brief and its ordered directly linked resources exactly.
Write only the manifest's one allowed output; for a blocking question, write no file.
Ignore parent conversation and undeclared runtime data as product inputs.
Return only one exact JSON receipt permitted by the execution contract."""


def codex_agent_toml(
    profile: str, model: str, reasoning: str, description: str
) -> str:
    name = f"stepan_{profile}"
    instructions = codex_agent_instructions(profile)
    return (
        f"name = {json.dumps(name)}\n"
        f"description = {json.dumps(description)}\n"
        f"model = {json.dumps(model)}\n"
        f"model_reasoning_effort = {json.dumps(reasoning)}\n"
        'developer_instructions = """\n'
        f"{instructions}\n"
        '"""\n'
    )


def codex_stepan_config() -> str:
    lines = ["schema_version: 1", "", "adapters:", "  codex:", "    kind: codex"]
    lines.extend(("", "profiles:"))
    for profile, _, _, _ in CODEX_AGENT_PROFILES:
        lines.extend(
            (
                f"  {profile}:",
                "    adapter: codex",
                f"    agent: stepan_{profile}",
                "    project_inputs: []",
                "",
            )
        )
    lines.extend(("workflows:", "  feature:", "    router: orchestrator", "    roles:"))
    lines.extend(f"      {role}: {profile}" for role, profile in CODEX_ROLE_BINDINGS)
    return "\n".join(lines) + "\n"


def claude_agent_instructions(profile: str) -> str:
    if profile == "orchestrator":
        return (
            "Act only as the named Stepan workflow orchestrator for this project.\n"
            "Before any workflow transition, require the current Claude Code "
            "agent name, model family, and effort to match this project agent "
            "definition; stop on a mismatch or unavailable runtime value.\n"
            "Read and follow every protocol and every audit, execution, router, "
            "or adapter contract selected by the explicit Stepan invocation.\n"
            "Never invoke the Stepan skill recursively or treat unrelated "
            "conversation history as product input.\n"
            "Dispatch only the fresh role runs selected by persisted workflow "
            "state."
        )
    return (
        "Act only as a Stepan workflow role executor when given one role-run "
        "manifest.\n"
        "Read only the skill resources and project inputs declared by that "
        "manifest.\n"
        "Follow the selected role brief and its ordered directly linked "
        "resources exactly.\n"
        "Write only the manifest's one allowed output; for a blocking question, "
        "write no file.\n"
        "Ignore parent conversation and undeclared runtime data as product "
        "inputs.\n"
        "Return only one exact JSON receipt permitted by the execution contract."
    )


def claude_agent_markdown(
    profile: str, model: str, effort: str, description: str
) -> str:
    name = f"stepan-{profile}"
    return (
        "---\n"
        f"name: {name}\n"
        f"description: {json.dumps(description)}\n"
        f"model: {model}\n"
        f"effort: {effort}\n"
        "---\n\n"
        f"{claude_agent_instructions(profile)}\n"
    )


def claude_runtime_check_script() -> str:
    return '''#!/usr/bin/env -S uv run --no-project --no-python-downloads --script
"""Validate Stepan's project-local Claude Code runtime settings."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from pathlib import Path


MAX_INPUT_BYTES = 256 * 1024
AGENT_PATTERN = re.compile(r"stepan-[a-z]+")


def fail(message: str, hook: bool) -> int:
    reason = f"Stepan Claude configuration mismatch: {message}"
    if hook:
        print(
            json.dumps(
                {
                    "continue": False,
                    "stopReason": reason,
                    "systemMessage": reason,
                },
                sort_keys=True,
            )
        )
        return 0
    print(reason, file=sys.stderr)
    return 1


def model_matches(actual: str, family: str) -> bool:
    value = actual.lower()
    return value == family or re.search(
        rf"(?:^|[^a-z]){re.escape(family)}(?:[^a-z]|$)", value
    ) is not None


def read_agent_definition(
    project_root: Path, agent: str, model_family: str, effort: str
) -> None:
    if not AGENT_PATTERN.fullmatch(agent):
        raise ValueError("invalid expected agent name")
    root = project_root.resolve(strict=True)
    if root.parent == root:
        raise ValueError("project root must not be a filesystem root")
    path = root / ".claude" / "agents" / f"{agent}.md"
    if path.is_symlink() or not path.is_file():
        raise ValueError(f"missing regular project agent definition: {path}")
    resolved = path.resolve(strict=True)
    try:
        resolved.relative_to(root)
    except ValueError as error:
        raise ValueError("project agent definition escapes the project root") from error
    content = path.read_bytes()
    if len(content) > MAX_INPUT_BYTES:
        raise ValueError("project agent definition is too large")
    text = content.decode("utf-8")
    if text.startswith("\\ufeff") or "\\r" in text or not text.startswith("---\\n"):
        raise ValueError("project agent definition has invalid encoding or frontmatter")
    frontmatter, separator, _ = text[4:].partition("\\n---\\n")
    if not separator:
        raise ValueError("project agent definition has invalid frontmatter")
    fields: dict[str, str] = {}
    for line in frontmatter.splitlines():
        key, marker, value = line.partition(": ")
        if not marker or key in fields:
            raise ValueError("project agent definition has invalid frontmatter fields")
        fields[key] = value
    if set(fields) != {"name", "description", "model", "effort"}:
        raise ValueError("project agent definition fields differ from initialization")
    if fields["name"] != agent:
        raise ValueError("project agent name differs from its binding")
    if fields["model"] != model_family:
        raise ValueError("project agent model differs from its binding")
    if fields["effort"] != effort:
        raise ValueError("project agent effort differs from its binding")


def session_start(args: argparse.Namespace) -> int:
    try:
        read_agent_definition(
            args.project_root, args.agent, args.model_family, args.effort
        )
        raw = sys.stdin.buffer.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            raise ValueError("SessionStart input is too large")
        event = json.loads(raw.decode("utf-8"))
        if not isinstance(event, dict) or event.get("hook_event_name") != "SessionStart":
            raise ValueError("invalid SessionStart hook input")
        if event.get("agent_type") != args.agent:
            raise ValueError(
                f"active agent is {event.get('agent_type')!r}, expected {args.agent!r}"
            )
        model = event.get("model")
        if not isinstance(model, str) or not model_matches(model, args.model_family):
            raise ValueError(
                f"active model is {model!r}, expected family {args.model_family!r}"
            )
        effort_value = event.get("effort")
        active_effort = (
            effort_value.get("level") if isinstance(effort_value, dict) else None
        ) or os.environ.get("CLAUDE_EFFORT")
        if active_effort != args.effort:
            raise ValueError(
                f"active effort is {active_effort!r}, expected {args.effort!r}"
            )
    except (OSError, UnicodeError, ValueError, json.JSONDecodeError) as error:
        return fail(str(error), True)
    print(
        json.dumps(
            {
                "hookSpecificOutput": {
                    "hookEventName": "SessionStart",
                    "additionalContext": (
                        "Stepan verified the project router agent, model family, "
                        "and effort for this session."
                    ),
                }
            },
            sort_keys=True,
        )
    )
    return 0


def role_preflight(args: argparse.Namespace) -> int:
    try:
        read_agent_definition(
            args.project_root, args.agent, args.model_family, args.effort
        )
        model_override = os.environ.get("CLAUDE_CODE_SUBAGENT_MODEL")
        if model_override and model_override != "inherit":
            raise ValueError(
                "CLAUDE_CODE_SUBAGENT_MODEL overrides project role agents"
            )
        effort_override = os.environ.get("CLAUDE_CODE_EFFORT_LEVEL")
        if effort_override and effort_override != args.effort:
            raise ValueError(
                "CLAUDE_CODE_EFFORT_LEVEL differs from the project role effort"
            )
    except (OSError, UnicodeError, ValueError) as error:
        return fail(str(error), False)
    print(
        json.dumps(
            {
                "agent": args.agent,
                "model_family": args.model_family,
                "effort": args.effort,
                "status": "verified",
            },
            sort_keys=True,
        )
    )
    return 0


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)
    for command in ("session-start", "role-preflight"):
        check = commands.add_parser(command)
        check.add_argument("--project-root", required=True, type=Path)
        check.add_argument("--agent", required=True)
        check.add_argument("--model-family", required=True, choices=("opus", "sonnet"))
        check.add_argument("--effort", required=True, choices=("low", "medium", "high", "xhigh", "max"))
    return result


def main() -> int:
    args = parser().parse_args()
    if args.command == "session-start":
        return session_start(args)
    return role_preflight(args)


if __name__ == "__main__":
    raise SystemExit(main())
'''


def expected_claude_settings() -> dict[str, object]:
    return {
        "agent": "stepan-orchestrator",
        "hooks": {
            "SessionStart": [
                {
                    "hooks": [
                        {
                            "type": "command",
                            "command": "uv",
                            "args": [
                                "run",
                                "--no-project",
                                "--no-python-downloads",
                                "${CLAUDE_PROJECT_DIR}/.claude/hooks/stepan-runtime.py",
                                "session-start",
                                "--project-root",
                                "${CLAUDE_PROJECT_DIR}",
                                "--agent",
                                "stepan-orchestrator",
                                "--model-family",
                                "opus",
                                "--effort",
                                "high",
                            ],
                            "timeout": 10,
                        }
                    ]
                }
            ]
        },
    }


def claude_settings() -> str:
    return json.dumps(expected_claude_settings(), indent=2) + "\n"


def claude_stepan_config() -> str:
    lines = [
        "schema_version: 1",
        "",
        "adapters:",
        "  claude:",
        "    kind: claude-code",
    ]
    lines.extend(("", "profiles:"))
    for profile, _, _, _ in CLAUDE_AGENT_PROFILES:
        lines.extend(
            (
                f"  {profile}:",
                "    adapter: claude",
                f"    agent: stepan-{profile}",
                "    project_inputs: []",
                "",
            )
        )
    lines.extend(("workflows:", "  feature:", "    router: orchestrator", "    roles:"))
    lines.extend(f"      {role}: {profile}" for role, profile in CLAUDE_ROLE_BINDINGS)
    return "\n".join(lines) + "\n"


def _parse_yaml_scalar(value: str, line_number: int) -> object:
    if value == "null":
        return None
    if value == "true":
        return True
    if value == "false":
        return False
    if value == "[]":
        return []
    if value == "{}":
        return {}
    if re.fullmatch(r"0|[1-9][0-9]*", value):
        return int(value)
    if value.startswith('"'):
        try:
            parsed = json.loads(value)
        except json.JSONDecodeError as error:
            raise ValueError(f"invalid YAML string on line {line_number}") from error
        if not isinstance(parsed, str):
            raise ValueError(f"invalid YAML scalar on line {line_number}")
        return parsed
    if value.startswith("'"):
        if len(value) < 2 or not value.endswith("'"):
            raise ValueError(f"invalid YAML string on line {line_number}")
        return value[1:-1].replace("''", "'")
    if value.startswith("["):
        if not value.endswith("]"):
            raise ValueError(f"invalid YAML sequence on line {line_number}")
        body = value[1:-1].strip()
        if not body:
            return []
        items: list[str] = []
        start = 0
        quote: str | None = None
        escaped = False
        for index, character in enumerate(body):
            if escaped:
                escaped = False
            elif quote == '"' and character == "\\":
                escaped = True
            elif quote is not None:
                if character == quote:
                    quote = None
            elif character in {"'", '"'}:
                quote = character
            elif character == ",":
                items.append(body[start:index].strip())
                start = index + 1
        if quote is not None:
            raise ValueError(f"invalid YAML sequence on line {line_number}")
        items.append(body[start:].strip())
        if any(not item for item in items):
            raise ValueError(f"invalid YAML sequence on line {line_number}")
        return [_parse_yaml_scalar(item, line_number) for item in items]
    if (
        not value
        or value[0] in "-?:,[]{}#&*!|>@`"
        or " #" in value
        or ": " in value
    ):
        raise ValueError(f"unsupported YAML scalar on line {line_number}")
    return value


def parse_strict_yaml(content: str, name: str) -> object:
    if content.startswith("\ufeff"):
        raise ValueError(f"{name} must not contain a byte-order mark")
    if "\r" in content or not content.endswith("\n"):
        raise ValueError(f"{name} must use LF and end with a newline")

    tokens: list[tuple[int, str, int]] = []
    for line_number, line in enumerate(content.splitlines(), 1):
        if not line:
            continue
        if "\t" in line or line.rstrip() != line:
            raise ValueError(f"invalid {name} line {line_number}")
        indentation = len(line) - len(line.lstrip(" "))
        if indentation % 2:
            raise ValueError(f"invalid {name} indentation on line {line_number}")
        tokens.append((indentation, line[indentation:], line_number))
    if not tokens or tokens[0][0] != 0:
        raise ValueError(f"{name} must contain one root value")

    def parse_mapping_entry(text: str, line_number: int) -> tuple[str, str]:
        if text.startswith('"'):
            try:
                key, offset = json.JSONDecoder().raw_decode(text)
            except json.JSONDecodeError as error:
                raise ValueError(f"invalid {name} key on line {line_number}") from error
            if not isinstance(key, str) or not key or text[offset : offset + 1] != ":":
                raise ValueError(f"invalid {name} key on line {line_number}")
            return key, text[offset + 1 :]
        match = re.fullmatch(r"([a-zA-Z][a-zA-Z0-9_.-]*):(.*)", text)
        if match is None:
            raise ValueError(f"invalid {name} key on line {line_number}")
        return match.groups()

    def parse_mapping(index: int, indentation: int) -> tuple[dict[str, object], int]:
        mapping: dict[str, object] = {}
        while index < len(tokens):
            current_indent, text, line_number = tokens[index]
            if current_indent < indentation:
                break
            if current_indent != indentation or text.startswith("- "):
                raise ValueError(f"invalid {name} mapping on line {line_number}")
            key, remainder = parse_mapping_entry(text, line_number)
            if key in mapping:
                raise ValueError(f"duplicate {name} key on line {line_number}")
            if remainder:
                if not remainder.startswith(" ") or remainder != f" {remainder[1:].strip()}":
                    raise ValueError(f"invalid {name} scalar on line {line_number}")
                mapping[key] = _parse_yaml_scalar(remainder[1:], line_number)
                index += 1
                continue
            index += 1
            if index >= len(tokens) or tokens[index][0] != indentation + 2:
                raise ValueError(f"empty {name} mapping value on line {line_number}")
            mapping[key], index = parse_node(index, indentation + 2)
        return mapping, index

    def parse_sequence(index: int, indentation: int) -> tuple[list[object], int]:
        sequence: list[object] = []
        while index < len(tokens):
            current_indent, text, line_number = tokens[index]
            if current_indent < indentation:
                break
            if current_indent != indentation or not text.startswith("- "):
                raise ValueError(f"invalid {name} sequence on line {line_number}")
            remainder = text[2:]
            try:
                key, value_text = parse_mapping_entry(remainder, line_number)
            except ValueError:
                sequence.append(_parse_yaml_scalar(remainder, line_number))
                index += 1
                continue
            item: dict[str, object] = {}
            if value_text:
                if not value_text.startswith(" "):
                    raise ValueError(f"invalid {name} scalar on line {line_number}")
                item[key] = _parse_yaml_scalar(value_text[1:], line_number)
                index += 1
            else:
                index += 1
                if index >= len(tokens) or tokens[index][0] != indentation + 2:
                    raise ValueError(f"empty {name} sequence value on line {line_number}")
                item[key], index = parse_node(index, indentation + 2)
            if index < len(tokens) and tokens[index][0] == indentation + 2:
                remainder_mapping, index = parse_mapping(index, indentation + 2)
                if set(item) & set(remainder_mapping):
                    raise ValueError(f"duplicate {name} sequence mapping key")
                item.update(remainder_mapping)
            sequence.append(item)
        return sequence, index

    def parse_node(index: int, indentation: int) -> tuple[object, int]:
        if tokens[index][0] != indentation:
            raise ValueError(f"invalid {name} nesting on line {tokens[index][2]}")
        if tokens[index][1].startswith("- "):
            return parse_sequence(index, indentation)
        return parse_mapping(index, indentation)

    result, final_index = parse_node(0, 0)
    if final_index != len(tokens):
        raise ValueError(f"invalid trailing {name} content")
    return result


def parse_generated_mapping_yaml(content: str) -> dict[str, object]:
    value = parse_strict_yaml(content, "generated YAML")
    if not isinstance(value, dict):
        raise ValueError("generated YAML must be a mapping")
    return value


def expected_codex_config() -> dict[str, object]:
    return {
        "schema_version": 1,
        "adapters": {"codex": {"kind": "codex"}},
        "profiles": {
            profile: {
                "adapter": "codex",
                "agent": f"stepan_{profile}",
                "project_inputs": [],
            }
            for profile, _, _, _ in CODEX_AGENT_PROFILES
        },
        "workflows": {
            "feature": {
                "router": "orchestrator",
                "roles": dict(CODEX_ROLE_BINDINGS),
            }
        },
    }


def expected_claude_config() -> dict[str, object]:
    return {
        "schema_version": 1,
        "adapters": {"claude": {"kind": "claude-code"}},
        "profiles": {
            profile: {
                "adapter": "claude",
                "agent": f"stepan-{profile}",
                "project_inputs": [],
            }
            for profile, _, _, _ in CLAUDE_AGENT_PROFILES
        },
        "workflows": {
            "feature": {
                "router": "orchestrator",
                "roles": dict(CLAUDE_ROLE_BINDINGS),
            }
        },
    }


def canonical_project_root(project_root: Path) -> Path:
    if not project_root.exists() or not project_root.is_dir():
        raise ValueError("project root must be an existing directory")
    result = project_root.resolve(strict=True)
    if result.parent == result:
        raise ValueError("project root must not be a filesystem root")
    return result


def resolve_project_file(
    project_root: Path,
    relative: str,
    name: str,
    maximum_bytes: int | None = None,
) -> Path:
    path = PurePosixPath(relative)
    if (
        path.is_absolute()
        or not path.parts
        or any(part in {"", ".", ".."} for part in path.parts)
        or path.as_posix() != relative
    ):
        raise ValueError(f"invalid {name} path: {relative}")
    current = project_root
    for part in path.parts:
        current = current / part
        if current.is_symlink():
            raise ValueError(f"{name} path contains a symlink: {relative}")
    if not current.is_file():
        raise ValueError(f"declared {name} is unavailable: {relative}")
    if maximum_bytes is not None and current.stat().st_size > maximum_bytes:
        raise ValueError(f"{name} exceeds {maximum_bytes} bytes: {relative}")
    return current


def read_bounded_utf8(path: Path, name: str, maximum_bytes: int) -> str:
    if path.is_symlink() or not path.is_file():
        raise ValueError(f"{name} path must be a regular file")
    if path.stat().st_size > maximum_bytes:
        raise ValueError(f"{name} exceeds {maximum_bytes} bytes")
    return path.read_bytes().decode("utf-8")


def validate_project_config(
    value: object,
    project_root: Path,
    current_host: str,
) -> dict[str, object]:
    if current_host not in {"codex", "claude-code"}:
        raise ValueError("unsupported current host")
    config = require_object(
        value, "configuration", {"schema_version", "adapters", "profiles", "workflows"}
    )
    if type(config["schema_version"]) is not int or config["schema_version"] != 1:
        raise ValueError("configuration schema_version must be 1")

    adapters = config["adapters"]
    if not isinstance(adapters, dict):
        raise ValueError("configuration.adapters must be an object")
    if not adapters:
        raise ValueError("configuration must declare at least one adapter")
    normalized_adapters: dict[str, object] = {}
    for adapter_name, adapter_value in adapters.items():
        if not LOCAL_ID_PATTERN.fullmatch(adapter_name):
            raise ValueError(f"invalid adapter name: {adapter_name}")
        adapter = require_object(
            adapter_value,
            f"adapter {adapter_name}",
            {"kind"},
            {"root", "wait_seconds"},
        )
        kind = require_non_empty_string(adapter["kind"], f"adapter {adapter_name}.kind")
        if kind == "native":
            require_object(adapter, f"adapter {adapter_name}", {"kind"})
            normalized_adapters[adapter_name] = {"kind": current_host}
        elif kind in {"codex", "claude-code"}:
            require_object(adapter, f"adapter {adapter_name}", {"kind"})
            if kind != current_host:
                raise ValueError(f"adapter {adapter_name} does not match current host")
            normalized_adapters[adapter_name] = {"kind": kind}
        elif kind == "mailbox":
            mailbox = require_object(
                adapter,
                f"adapter {adapter_name}",
                {"kind", "root"},
                {"wait_seconds"},
            )
            root = Path(require_non_empty_string(mailbox["root"], "mailbox root"))
            if not root.is_absolute() or root.parent == root:
                raise ValueError("mailbox root must be absolute and non-root")
            wait_seconds = mailbox.get("wait_seconds", 3600)
            if type(wait_seconds) is not int or not 0 <= wait_seconds <= 3600:
                raise ValueError("mailbox wait_seconds must be from 0 through 3600")
            normalized_adapters[adapter_name] = {
                "kind": "mailbox",
                "root": str(root),
                "wait_seconds": wait_seconds,
            }
        else:
            raise ValueError(f"unsupported adapter kind: {kind}")

    profiles = config["profiles"]
    if not isinstance(profiles, dict):
        raise ValueError("configuration.profiles must be an object")
    if not profiles:
        raise ValueError("configuration must declare at least one profile")
    normalized_profiles: dict[str, object] = {}
    for profile_name, profile_value in profiles.items():
        if not LOCAL_ID_PATTERN.fullmatch(profile_name):
            raise ValueError(f"invalid profile name: {profile_name}")
        profile_base = require_object(
            profile_value,
            f"profile {profile_name}",
            {"adapter", "project_inputs"},
            {"agent", "model", "reasoning"},
        )
        adapter_name = require_non_empty_string(
            profile_base["adapter"], f"profile {profile_name}.adapter"
        )
        if adapter_name not in normalized_adapters:
            raise ValueError(f"profile {profile_name} names an unknown adapter")
        adapter_kind = normalized_adapters[adapter_name]["kind"]
        normalized_profile: dict[str, object] = {"adapter": adapter_name}
        if adapter_kind in {"codex", "claude-code"}:
            profile = require_object(
                profile_base,
                f"profile {profile_name}",
                {"adapter", "agent", "project_inputs"},
            )
            normalized_profile["agent"] = require_non_empty_string(
                profile["agent"], f"profile {profile_name}.agent"
            )
        else:
            profile = require_object(
                profile_base,
                f"profile {profile_name}",
                {"adapter", "model", "project_inputs"},
                {"reasoning"},
            )
            normalized_profile["model"] = require_non_empty_string(
                profile["model"], f"profile {profile_name}.model"
            )
            if "reasoning" in profile:
                normalized_profile["reasoning"] = require_non_empty_string(
                    profile["reasoning"], f"profile {profile_name}.reasoning"
                )

        input_paths = profile["project_inputs"]
        if not isinstance(input_paths, list) or any(
            not isinstance(path, str) for path in input_paths
        ):
            raise ValueError(f"profile {profile_name}.project_inputs must be a list of paths")
        seen_inputs: set[str] = set()
        normalized_inputs: list[dict[str, str]] = []
        for relative in input_paths:
            if relative in seen_inputs:
                raise ValueError(f"duplicate project input for profile {profile_name}: {relative}")
            seen_inputs.add(relative)
            input_path = resolve_project_file(
                project_root, relative, "project input", PROJECT_INPUT_MAX_BYTES
            )
            normalized_inputs.append({"path": relative, "sha256": file_hash(input_path)})
        normalized_profile["project_inputs"] = normalized_inputs
        normalized_profiles[profile_name] = normalized_profile

    workflows = require_object(config["workflows"], "configuration.workflows", {"feature"})
    feature = require_object(
        workflows["feature"], "configuration.workflows.feature", {"router", "roles"}
    )
    router_profile = require_non_empty_string(feature["router"], "feature router")
    if router_profile not in normalized_profiles:
        raise ValueError("feature router names an unknown profile")
    router = normalized_profiles[router_profile]
    router_adapter = normalized_adapters[router["adapter"]]
    if router_adapter["kind"] not in {"codex", "claude-code"}:
        raise ValueError("feature router must use a native host adapter")
    if router.get("agent") == "default":
        raise ValueError("feature router must use a named agent")

    roles = require_object(feature["roles"], "feature roles", set(ROLES))
    normalized_bindings: dict[str, str] = {"router": router_profile}
    for role in ROLES:
        profile_name = require_non_empty_string(roles[role], f"binding {role}")
        if profile_name not in normalized_profiles:
            raise ValueError(f"binding {role} names an unknown profile")
        normalized_bindings[role] = profile_name

    return {
        "adapters": normalized_adapters,
        "profiles": normalized_profiles,
        "bindings": normalized_bindings,
    }


def validate_project_config_path(
    config_path: Path, project_root: Path, current_host: str
) -> dict[str, object]:
    project_root = canonical_project_root(project_root)
    expected_path = project_root / ".stepan/config.yaml"
    if config_path.resolve(strict=False) != expected_path:
        raise ValueError("configuration path must be <project-root>/.stepan/config.yaml")
    content = read_bounded_utf8(config_path, "configuration", CONTROL_FILE_MAX_BYTES)
    parsed = parse_strict_yaml(content, "configuration")
    snapshot = validate_project_config(parsed, project_root, current_host)
    return {
        "source": "project",
        "config_sha256": file_hash(config_path),
        **snapshot,
    }


def role_resource_manifest(skill_root: Path, role: str) -> dict[str, object]:
    if role not in ROLE_BRIEFS:
        raise ValueError("unknown role")
    skill_root = skill_root.resolve(strict=True)
    brief_relative = PurePosixPath(ROLE_BRIEFS[role])
    brief_path = skill_root.joinpath(*brief_relative.parts)
    if brief_path.is_symlink() or not brief_path.is_file():
        raise ValueError("role brief is unavailable")
    content = read_bounded_utf8(brief_path, "role brief", CONTROL_FILE_MAX_BYTES)
    if content.count("## Resources\n") != 1 or content.count("## Task\n") != 1:
        raise ValueError(
            "role brief must contain one Resources section and one Task section"
        )
    resources_section = content.split("## Task", 1)[0]
    resources: list[str] = []
    modules_root = (skill_root / "references/modules").resolve(strict=True)
    modules_prefix = str(modules_root) + os.sep
    for match in re.finditer(r"\[[^\]]+\]\(([^)]+)\)", resources_section):
        target = match.group(1)
        resolved = (brief_path.parent / target).resolve(strict=True)
        if not str(resolved).startswith(modules_prefix) or not resolved.is_file():
            raise ValueError(f"invalid role resource: {target}")
        relative = resolved.relative_to(skill_root).as_posix()
        if relative in resources:
            raise ValueError(f"duplicate role resource: {relative}")
        module_content = read_bounded_utf8(resolved, "role resource", CONTROL_FILE_MAX_BYTES)
        if re.search(r"\[[^\]]+\]\(([^)]+)\)", module_content):
            raise ValueError(f"role resource links another resource: {relative}")
        resources.append(relative)
    if not resources:
        raise ValueError("role brief declares no resources")
    return {"brief": brief_relative.as_posix(), "resources": resources}


def validate_role_manifest_examples(skill_root: Path) -> None:
    execution_contract = (
        skill_root / "references/flows/feature/execution.md"
    ).read_text(encoding="utf-8")
    examples: list[dict[str, object]] = []
    for block in re.findall(r"```json\n(.*?)```", execution_contract, flags=re.DOTALL):
        try:
            value = json.loads(block)
        except json.JSONDecodeError as error:
            raise ValueError("execution contract contains invalid JSON") from error
        if isinstance(value, dict) and value.get("brief") == ROLE_BRIEFS["design-author"]:
            examples.append(value)
    if len(examples) != 2:
        raise ValueError("execution contract must show common and mailbox role manifests")
    expected_resources = role_resource_manifest(skill_root, "design-author")["resources"]
    if any(example.get("resources") != expected_resources for example in examples):
        raise ValueError("role manifest example resources differ from the role brief")
    common = next((value for value in examples if "executor" not in value), None)
    mailbox = next((value for value in examples if "executor" in value), None)
    if common is None or mailbox is None:
        raise ValueError("role manifest examples must show common and mailbox forms")
    if {key: value for key, value in mailbox.items() if key != "executor"} != common:
        raise ValueError("common and mailbox role manifests have different semantics")

    yaml_blocks = re.findall(r"```yaml\n(.*?)```", execution_contract, flags=re.DOTALL)
    if len(yaml_blocks) != 2:
        raise ValueError("execution contract must show configuration and active-run YAML")
    config = require_object(
        parse_strict_yaml(yaml_blocks[0], "execution configuration example"),
        "execution configuration example",
        {"schema_version", "adapters", "profiles", "workflows"},
    )
    if config["schema_version"] != 1:
        raise ValueError("execution configuration example must use schema version 1")
    active_document = require_object(
        parse_strict_yaml(yaml_blocks[1], "execution active-run example"),
        "execution active-run example",
        {"active_run"},
    )
    active = require_object(
        active_document["active_run"],
        "execution active-run example",
        {
            "run_id",
            "sequence",
            "stage",
            "role",
            "purpose",
            "executor",
            "adapter",
            "output",
            "request_sha256",
        },
    )
    feature = require_object(
        require_object(config["workflows"], "example workflows", {"feature"})[
            "feature"
        ],
        "example feature workflow",
        {"router", "roles"},
    )
    roles = require_object(feature["roles"], "example feature roles", set(ROLES))
    if roles.get(active["role"]) != active["executor"]:
        raise ValueError("execution active run does not match its role binding")
    profiles = config["profiles"]
    adapters = config["adapters"]
    if not isinstance(profiles, dict) or active["executor"] not in profiles:
        raise ValueError("execution active run names an unknown profile")
    profile = require_object(
        profiles[active["executor"]],
        "execution active profile",
        {"adapter", "project_inputs"},
        {"agent", "model", "reasoning"},
    )
    if not isinstance(adapters, dict) or profile["adapter"] not in adapters:
        raise ValueError("execution active profile names an unknown adapter")
    configured_adapter = require_object(
        adapters[profile["adapter"]],
        "execution active adapter",
        {"kind"},
        {"root", "wait_seconds"},
    )["kind"]
    if configured_adapter == "native":
        if active["adapter"] not in {"codex", "claude-code"}:
            raise ValueError("native example profile has a non-native active adapter")
    elif active["adapter"] != configured_adapter:
        raise ValueError("execution active adapter differs from its configured profile")
    for field in ("run_id", "stage", "role", "purpose", "output"):
        if common.get(field) != active[field]:
            raise ValueError(f"role manifest example differs from active run: {field}")
    if configured_adapter == "mailbox":
        executor = require_object(
            mailbox["executor"],
            "mailbox manifest example executor",
            {"model"},
            {"reasoning"},
        )
        if executor["model"] != profile.get("model") or executor.get(
            "reasoning"
        ) != profile.get("reasoning"):
            raise ValueError("mailbox manifest executor differs from its configured profile")


def validate_codex_init_files(
    files: tuple[tuple[PurePosixPath, bytes], ...],
) -> None:
    expected_paths = tuple(
        PurePosixPath(f".codex/agents/stepan_{profile}.toml")
        for profile, _, _, _ in CODEX_AGENT_PROFILES
    ) + (PurePosixPath(".stepan/config.yaml"),)
    if tuple(path for path, _ in files) != expected_paths:
        raise ValueError("generated Codex initialization paths are inconsistent")

    parsed_agents: dict[str, dict[str, object]] = {}
    for (profile, model, reasoning, description), (path, content) in zip(
        CODEX_AGENT_PROFILES, files[:-1], strict=True
    ):
        if content.startswith(b"\xef\xbb\xbf") or b"\r" in content:
            raise ValueError(f"invalid generated line endings: {path}")
        try:
            document = tomllib.loads(content.decode("utf-8"))
        except (UnicodeError, tomllib.TOMLDecodeError) as error:
            raise ValueError(f"invalid generated TOML: {path}") from error
        expected_name = f"stepan_{profile}"
        expected_document = {
            "name": expected_name,
            "description": description,
            "model": model,
            "model_reasoning_effort": reasoning,
            "developer_instructions": codex_agent_instructions(profile) + "\n",
        }
        if document != expected_document:
            raise ValueError(f"generated TOML fields are inconsistent: {path}")
        if path.stem != document["name"] or document["name"] == "default":
            raise ValueError(f"generated agent filename and name differ: {path}")
        parsed_agents[profile] = document

    config_path, config_content = files[-1]
    try:
        config_text = config_content.decode("utf-8")
    except UnicodeError as error:
        raise ValueError(f"invalid generated YAML encoding: {config_path}") from error
    config = parse_generated_mapping_yaml(config_text)
    if config != expected_codex_config():
        raise ValueError("generated Codex configuration is inconsistent")

    profiles = config["profiles"]
    feature = config["workflows"]["feature"]
    router_profile = feature["router"]
    if not isinstance(profiles, dict) or not isinstance(router_profile, str):
        raise ValueError("generated Codex router binding is invalid")
    router = profiles.get(router_profile)
    if not isinstance(router, dict) or router.get("agent") != "stepan_orchestrator":
        raise ValueError("generated Codex router binding is inconsistent")
    if (
        parsed_agents[router_profile]["model"] != "gpt-5.6"
        or parsed_agents[router_profile]["model_reasoning_effort"] != "high"
    ):
        raise ValueError("generated Codex orchestrator is not on the strong model")


def codex_init_files() -> tuple[tuple[PurePosixPath, bytes], ...]:
    files = [
        (
            PurePosixPath(f".codex/agents/stepan_{profile}.toml"),
            codex_agent_toml(profile, model, reasoning, description).encode("utf-8"),
        )
        for profile, model, reasoning, description in CODEX_AGENT_PROFILES
    ]
    files.append(
        (PurePosixPath(".stepan/config.yaml"), codex_stepan_config().encode("utf-8"))
    )
    result = tuple(files)
    validate_codex_init_files(result)
    return result


def validate_claude_init_files(
    files: tuple[tuple[PurePosixPath, bytes], ...],
) -> None:
    expected_paths = tuple(
        PurePosixPath(f".claude/agents/stepan-{profile}.md")
        for profile, _, _, _ in CLAUDE_AGENT_PROFILES
    ) + (
        PurePosixPath(".claude/settings.json"),
        PurePosixPath(".claude/hooks/stepan-runtime.py"),
        PurePosixPath(".stepan/config.yaml"),
    )
    if tuple(path for path, _ in files) != expected_paths:
        raise ValueError("generated Claude initialization paths are inconsistent")

    parsed_agents: dict[str, dict[str, object]] = {}
    for (profile, model, effort, description), (path, content) in zip(
        CLAUDE_AGENT_PROFILES, files[:-3], strict=True
    ):
        if content.startswith(b"\xef\xbb\xbf") or b"\r" in content:
            raise ValueError(f"invalid generated line endings: {path}")
        try:
            text = content.decode("utf-8")
        except UnicodeError as error:
            raise ValueError(f"invalid generated Markdown encoding: {path}") from error
        if not text.startswith("---\n"):
            raise ValueError(f"generated Claude agent lacks frontmatter: {path}")
        frontmatter, separator, body = text[4:].partition("\n---\n")
        if not separator:
            raise ValueError(f"generated Claude agent has invalid frontmatter: {path}")
        document = parse_generated_mapping_yaml(frontmatter + "\n")
        expected_name = f"stepan-{profile}"
        expected_document = {
            "name": expected_name,
            "description": description,
            "model": model,
            "effort": effort,
        }
        if document != expected_document:
            raise ValueError(f"generated Claude agent fields are inconsistent: {path}")
        if path.stem != expected_name:
            raise ValueError(f"generated Claude agent filename and name differ: {path}")
        if body != "\n" + claude_agent_instructions(profile) + "\n":
            raise ValueError(f"generated Claude agent instructions are inconsistent: {path}")
        parsed_agents[profile] = document

    settings_path, settings_content = files[-3]
    try:
        settings = json.loads(settings_content.decode("utf-8"))
    except (UnicodeError, json.JSONDecodeError) as error:
        raise ValueError(f"invalid generated JSON: {settings_path}") from error
    if settings != expected_claude_settings():
        raise ValueError("generated Claude project settings are inconsistent")

    hook_path, hook_content = files[-2]
    if hook_content != claude_runtime_check_script().encode("utf-8"):
        raise ValueError(f"generated Claude runtime hook is inconsistent: {hook_path}")

    config_path, config_content = files[-1]
    try:
        config_text = config_content.decode("utf-8")
    except UnicodeError as error:
        raise ValueError(f"invalid generated YAML encoding: {config_path}") from error
    config = parse_generated_mapping_yaml(config_text)
    if config != expected_claude_config():
        raise ValueError("generated Claude configuration is inconsistent")

    profiles = config["profiles"]
    router_profile = config["workflows"]["feature"]["router"]
    router = profiles.get(router_profile) if isinstance(profiles, dict) else None
    if not isinstance(router, dict) or router.get("agent") != "stepan-orchestrator":
        raise ValueError("generated Claude router binding is inconsistent")
    if (
        parsed_agents[router_profile]["model"] != "opus"
        or parsed_agents[router_profile]["effort"] != "high"
    ):
        raise ValueError("generated Claude orchestrator settings are inconsistent")


def claude_init_files() -> tuple[tuple[PurePosixPath, bytes], ...]:
    files = [
        (
            PurePosixPath(f".claude/agents/stepan-{profile}.md"),
            claude_agent_markdown(profile, model, effort, description).encode("utf-8"),
        )
        for profile, model, effort, description in CLAUDE_AGENT_PROFILES
    ]
    files.extend(
        (
            (PurePosixPath(".claude/settings.json"), claude_settings().encode("utf-8")),
            (
                PurePosixPath(".claude/hooks/stepan-runtime.py"),
                claude_runtime_check_script().encode("utf-8"),
            ),
            (
                PurePosixPath(".stepan/config.yaml"),
                claude_stepan_config().encode("utf-8"),
            ),
        )
    )
    result = tuple(files)
    validate_claude_init_files(result)
    return result


def validate_codex_contract_examples(
    files: tuple[tuple[PurePosixPath, bytes], ...]
) -> None:
    skill_root = Path(__file__).resolve().parent.parent
    validate_role_manifest_examples(skill_root)
    init_contract = (skill_root / "references/flows/init/protocol.md").read_text(
        encoding="utf-8"
    )
    expected_command = (
        'uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" '
        'init-codex --host codex --project-root "<project-root>"'
    )
    if expected_command not in init_contract:
        raise ValueError("init contract command does not match init-codex arguments")
    for path, _ in files:
        if path.as_posix() not in init_contract:
            raise ValueError(f"init contract omits generated path: {path}")

    codex_adapter = (
        skill_root / "references/flows/feature/adapters/codex.md"
    ).read_text(encoding="utf-8")
    examples = re.findall(r"```toml\n(.*?)```", codex_adapter, flags=re.DOTALL)
    if len(examples) != 1:
        raise ValueError("Codex adapter must contain one orchestrator TOML example")
    orchestrator_toml = files[0][1].decode("utf-8")
    if examples[0] != orchestrator_toml:
        raise ValueError("Codex adapter orchestrator example differs from generated TOML")


def validate_claude_contract_examples(
    files: tuple[tuple[PurePosixPath, bytes], ...]
) -> None:
    skill_root = Path(__file__).resolve().parent.parent
    init_contract = (skill_root / "references/flows/init/protocol.md").read_text(
        encoding="utf-8"
    )
    expected_command = (
        'uv run --no-project --no-python-downloads "<skill-root>/scripts/stepan.py" '
        'init-claude --host <codex|claude-code> --project-root "<project-root>"'
    )
    if expected_command not in init_contract:
        raise ValueError("init contract command does not match init-claude arguments")
    for path, _ in files:
        if path.as_posix() not in init_contract:
            raise ValueError(f"init contract omits generated path: {path}")

    claude_adapter = (
        skill_root / "references/flows/feature/adapters/claude-code.md"
    ).read_text(encoding="utf-8")
    examples = re.findall(r"```markdown\n(.*?)```", claude_adapter, flags=re.DOTALL)
    if len(examples) != 1:
        raise ValueError("Claude adapter must contain one orchestrator Markdown example")
    orchestrator_markdown = files[0][1].decode("utf-8")
    if examples[0] != orchestrator_markdown:
        raise ValueError(
            "Claude adapter orchestrator example differs from generated Markdown"
        )


def _validate_init_target(project_root: Path, relative: PurePosixPath) -> Path:
    if relative.is_absolute() or not relative.parts or ".." in relative.parts:
        raise ValueError("invalid init destination")
    target = project_root.joinpath(*relative.parts)
    current = project_root
    for part in relative.parts[:-1]:
        current = current / part
        if current.is_symlink():
            raise ValueError(f"init destination parent is a symlink: {relative}")
        if current.exists() and not current.is_dir():
            raise ValueError(f"init destination parent is not a directory: {relative}")
    if target.is_symlink():
        raise ValueError(f"init destination is a symlink: {relative}")
    if target.exists() and not target.is_file():
        raise ValueError(f"init destination is not a regular file: {relative}")
    return target


def _ensure_init_parent(
    project_root: Path,
    target: Path,
    created_directories: list[tuple[Path, tuple[int, int]]],
) -> None:
    relative_parent = target.parent.relative_to(project_root)
    current = project_root
    for part in relative_parent.parts:
        current = current / part
        if current.is_symlink():
            raise ValueError(f"init destination parent became a symlink: {current}")
        if current.exists():
            if not current.is_dir():
                raise ValueError(
                    f"init destination parent became a non-directory: {current}"
                )
            continue
        try:
            current.mkdir()
        except FileExistsError:
            if current.is_symlink() or not current.is_dir():
                raise ValueError(
                    f"init destination parent changed while creating: {current}"
                )
        else:
            stat = current.stat()
            created_directories.append((current, (stat.st_dev, stat.st_ino)))


def _rollback_init(
    created_files: list[tuple[Path, tuple[int, int]]],
    created_directories: list[tuple[Path, tuple[int, int]]],
) -> None:
    for path, identity in reversed(created_files):
        try:
            stat = path.stat()
            if not path.is_symlink() and (stat.st_dev, stat.st_ino) == identity:
                path.unlink()
        except OSError:
            pass
    for path, identity in reversed(created_directories):
        try:
            stat = path.stat()
            if not path.is_symlink() and (stat.st_dev, stat.st_ino) == identity:
                path.rmdir()
        except OSError:
            pass


def _initialize_project_files(
    project_root: Path,
    files: tuple[tuple[PurePosixPath, bytes], ...],
    result_host: str,
) -> dict[str, object]:
    if not project_root.exists() or not project_root.is_dir():
        raise ValueError("project root must be an existing directory")
    project_root = project_root.resolve(strict=True)
    if project_root.parent == project_root:
        raise ValueError("project root must not be a filesystem root")

    missing: list[tuple[PurePosixPath, Path, bytes]] = []
    unchanged: list[str] = []
    conflicts: list[str] = []
    for relative, expected in files:
        target = _validate_init_target(project_root, relative)
        if not target.exists():
            missing.append((relative, target, expected))
        elif target.read_bytes() == expected:
            unchanged.append(relative.as_posix())
        else:
            conflicts.append(relative.as_posix())

    if conflicts:
        raise ValueError("conflicting existing files: " + ", ".join(conflicts))

    created: list[str] = []
    created_files: list[tuple[Path, tuple[int, int]]] = []
    created_directories: list[tuple[Path, tuple[int, int]]] = []
    try:
        for relative, target, expected in missing:
            _ensure_init_parent(project_root, target, created_directories)
            try:
                descriptor = os.open(
                    target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644
                )
            except FileExistsError:
                if (
                    not target.is_symlink()
                    and target.is_file()
                    and target.read_bytes() == expected
                ):
                    unchanged.append(relative.as_posix())
                    continue
                raise ValueError(
                    f"init destination changed while creating: {relative}"
                )
            stat = os.fstat(descriptor)
            created_files.append((target, (stat.st_dev, stat.st_ino)))
            with os.fdopen(descriptor, "wb") as stream:
                stream.write(expected)
                stream.flush()
                os.fsync(stream.fileno())
            if target.is_symlink() or target.read_bytes() != expected:
                raise ValueError(f"could not verify created file: {relative}")
            created.append(relative.as_posix())
    except (OSError, ValueError):
        _rollback_init(created_files, created_directories)
        raise

    return {
        "schema_version": 1,
        "host": result_host,
        "status": "created" if created else "unchanged",
        "created": created,
        "unchanged": unchanged,
    }


def initialize_codex(project_root: Path, current_host: str) -> dict[str, object]:
    if current_host != CODEX_HOST:
        raise ValueError("init-codex requires the current host to be Codex")
    return _initialize_project_files(project_root, codex_init_files(), CODEX_HOST)


def initialize_claude(project_root: Path, current_host: str) -> dict[str, object]:
    if current_host not in {CODEX_HOST, CLAUDE_CODE_HOST}:
        raise ValueError("init-claude requires Codex or Claude Code as the current host")
    return _initialize_project_files(
        project_root, claude_init_files(), CLAUDE_CODE_HOST
    )


def spec_id(id_hint: str) -> str:
    value = id_hint.lower().translate(TRANSLITERATION)
    value = re.sub(r"[^a-z0-9]+", "-", value).strip("-")[:63].rstrip("-")
    return value or "change"


def next_available_spec_id(base: str, exists: Callable[[str], bool]) -> str:
    candidate = base
    suffix = 2
    while exists(candidate):
        ending = f"-{suffix}"
        candidate = f"{base[: 63 - len(ending)].rstrip('-')}{ending}"
        suffix += 1
    return candidate


def collision_result(id_hint: str, root: Path) -> dict[str, object]:
    base = spec_id(id_hint)
    exists = lambda candidate: (root / candidate).exists()
    return {
        "spec_id": base,
        "collision": exists(base),
        "next_available": next_available_spec_id(base, exists),
    }


def role_run_id(specification: str, stage: str, role: str, sequence: int) -> str:
    if len(specification) > 63 or not SPEC_ID_PATTERN.fullmatch(specification):
        raise ValueError("invalid specification ID")
    if stage not in STAGES:
        raise ValueError("invalid stage")
    if role not in ROLES:
        raise ValueError("invalid role")
    if sequence < 1:
        raise ValueError("sequence must be positive")
    return f"{specification}--{stage}--{role}--{sequence}"


def run_reservation(
    specification: str, stage: str, role: str, sequence: int
) -> dict[str, object]:
    return {
        "run_id": role_run_id(specification, stage, role, sequence),
        "sequence": sequence,
        "next_run_sequence": sequence + 1,
    }


def approval_transition(
    action: str,
    stage: str,
    status: str,
    pending: bool,
    review_verdict: str | None,
    unresolved_user_decision: bool,
) -> dict[str, object]:
    if action not in APPROVAL_ACTIONS:
        raise ValueError("invalid approval action")
    if stage not in STAGES:
        raise ValueError("invalid approval stage")
    if status != "awaiting-approval":
        raise ValueError("approval requires awaiting-approval status")
    if pending:
        raise ValueError("approval is forbidden while pending is non-null")
    if unresolved_user_decision:
        raise ValueError("approval is forbidden with an unresolved user-decision")
    if stage in {"requirements", "design"}:
        if review_verdict != "pass":
            raise ValueError(f"{stage} approval requires a passing review")
    elif review_verdict is not None:
        raise ValueError(f"{stage} approval must not use a review verdict")

    next_stage = {
        "idea": "requirements",
        "requirements": "design",
        "design": "plan",
        "plan": "plan",
    }[stage]
    return {
        "approved_stage": stage,
        "next_stage": next_stage,
        "next_status": "approved" if stage == "plan" else "drafting",
        "commit": action == "continue-and-commit",
    }


def expected_output_path(specification: str, stage: str, role: str) -> str:
    review_roles = {"requirements-reviewer", "specification-reviewer"}
    if role in review_roles:
        expected_review_stage = (
            "requirements" if role == "requirements-reviewer" else "design"
        )
        if stage != expected_review_stage:
            raise ValueError("role is not valid for review stage")
        suffix = f"review/{stage}.yaml"
    elif STAGE_OWNERS.get(stage) == role:
        suffix = f"{stage}.md"
    else:
        raise ValueError("role does not own the selected stage")
    return f"docs/changes/specs/{specification}/{suffix}"


def validate_execution_snapshot(value: object) -> dict[str, object]:
    execution = require_object(
        value,
        "state.execution",
        {"source", "config_sha256", "adapters", "profiles", "bindings"},
    )
    if execution["source"] != "project":
        raise ValueError("state execution source must be project")
    config_hash = require_non_empty_string(
        execution["config_sha256"], "state.execution.config_sha256"
    )
    if not HASH_PATTERN.fullmatch(config_hash):
        raise ValueError("invalid state execution configuration hash")

    adapters = execution["adapters"]
    if not isinstance(adapters, dict) or not adapters:
        raise ValueError("state execution adapters must be a non-empty object")
    for name, value in adapters.items():
        if not LOCAL_ID_PATTERN.fullmatch(name):
            raise ValueError("invalid state execution adapter name")
        adapter = require_object(
            value, f"state adapter {name}", {"kind"}, {"root", "wait_seconds"}
        )
        kind = adapter["kind"]
        if kind in {"codex", "claude-code"}:
            require_object(adapter, f"state adapter {name}", {"kind"})
        elif kind == "mailbox":
            require_object(
                adapter,
                f"state adapter {name}",
                {"kind", "root", "wait_seconds"},
            )
            root = Path(require_non_empty_string(adapter["root"], "mailbox root"))
            if not root.is_absolute() or root.parent == root:
                raise ValueError("invalid state mailbox root")
            if type(adapter["wait_seconds"]) is not int or not 0 <= adapter["wait_seconds"] <= 3600:
                raise ValueError("invalid state mailbox wait_seconds")
        else:
            raise ValueError("state adapters must use concrete supported kinds")

    profiles = execution["profiles"]
    if not isinstance(profiles, dict) or not profiles:
        raise ValueError("state execution profiles must be a non-empty object")
    for name, value in profiles.items():
        if not LOCAL_ID_PATTERN.fullmatch(name):
            raise ValueError("invalid state execution profile name")
        profile = require_object(
            value,
            f"state profile {name}",
            {"adapter", "project_inputs"},
            {"agent", "model", "reasoning"},
        )
        adapter_name = require_non_empty_string(profile["adapter"], "state profile adapter")
        if adapter_name not in adapters:
            raise ValueError("state profile names an unknown adapter")
        kind = adapters[adapter_name]["kind"]
        if kind in {"codex", "claude-code"}:
            require_object(
                profile, f"state profile {name}", {"adapter", "agent", "project_inputs"}
            )
            require_non_empty_string(profile["agent"], "state profile agent")
        else:
            require_object(
                profile,
                f"state profile {name}",
                {"adapter", "model", "project_inputs"},
                {"reasoning"},
            )
            require_non_empty_string(profile["model"], "state profile model")
            if "reasoning" in profile:
                require_non_empty_string(profile["reasoning"], "state profile reasoning")
        inputs = profile["project_inputs"]
        if not isinstance(inputs, list):
            raise ValueError("state profile project_inputs must be a list")
        seen_paths: set[str] = set()
        for item in inputs:
            project_input = require_object(item, "state project input", {"path", "sha256"})
            path = require_non_empty_string(project_input["path"], "state project input path")
            digest = require_non_empty_string(
                project_input["sha256"], "state project input hash"
            )
            if path in seen_paths or not HASH_PATTERN.fullmatch(digest):
                raise ValueError("invalid or duplicate state project input")
            seen_paths.add(path)

    bindings = require_object(
        execution["bindings"], "state execution bindings", {"router", *ROLES}
    )
    for role, profile_name in bindings.items():
        name = require_non_empty_string(profile_name, f"state binding {role}")
        if name not in profiles:
            raise ValueError(f"state binding {role} names an unknown profile")
    router = profiles[bindings["router"]]
    router_kind = adapters[router["adapter"]]["kind"]
    if router_kind not in {"codex", "claude-code"} or router.get("agent") == "default":
        raise ValueError("state router must be a named native agent")
    return execution


def validate_state_value(value: object, specification: str) -> dict[str, object]:
    state = require_object(
        value,
        "state",
        {
            "schema_version",
            "specification",
            "created_at",
            "initial_request_sha256",
            "stage",
            "status",
            "automatic_revision_attempts",
            "next_run_sequence",
            "approvals",
            "clarifications",
            "pending",
            "active_run",
            "checkpoint_commit",
            "audit",
            "execution",
        },
    )
    if type(state["schema_version"]) is not int or state["schema_version"] != 1:
        raise ValueError("state schema_version must be 1")
    if state["specification"] != specification:
        raise ValueError("state specification identity does not match")
    if not isinstance(state["created_at"], str) or not TIMESTAMP_PATTERN.fullmatch(
        state["created_at"]
    ):
        raise ValueError("invalid state created_at")
    request_hash = require_non_empty_string(
        state["initial_request_sha256"], "state initial request hash"
    )
    if not HASH_PATTERN.fullmatch(request_hash):
        raise ValueError("invalid state initial request hash")
    stage = state["stage"]
    status = state["status"]
    if stage not in STAGES or status not in STATUSES:
        raise ValueError("invalid state stage or status")
    if status == "approved" and stage != "plan":
        raise ValueError("only a plan state may be approved")
    attempts = state["automatic_revision_attempts"]
    sequence = state["next_run_sequence"]
    if type(attempts) is not int or not 0 <= attempts <= 3:
        raise ValueError("invalid automatic revision attempts")
    if type(sequence) is not int or sequence < 1:
        raise ValueError("invalid next run sequence")

    approvals = state["approvals"]
    if not isinstance(approvals, dict) or any(key not in STAGES for key in approvals):
        raise ValueError("invalid state approvals")
    for approved_stage, value in approvals.items():
        approval = require_object(
            value,
            f"approval {approved_stage}",
            {"artifact_sha256", "review_sha256", "accepted_risks"},
        )
        if not HASH_PATTERN.fullmatch(
            require_non_empty_string(approval["artifact_sha256"], "approval artifact hash")
        ):
            raise ValueError("invalid approval artifact hash")
        review_hash = approval["review_sha256"]
        if approved_stage in {"requirements", "design"}:
            if not isinstance(review_hash, str) or not HASH_PATTERN.fullmatch(review_hash):
                raise ValueError("reviewed approval requires a review hash")
        elif review_hash is not None:
            raise ValueError("unreviewed approval must use a null review hash")
        if not isinstance(approval["accepted_risks"], list) or any(
            not isinstance(item, str) or not item.strip()
            for item in approval["accepted_risks"]
        ):
            raise ValueError("invalid accepted risks")
    stage_index = STAGES.index(stage)
    required_approvals = set(STAGES[:stage_index])
    if status == "approved":
        required_approvals.add("plan")
    if set(approvals) != required_approvals:
        raise ValueError("state approvals do not match stage progression")

    clarifications = state["clarifications"]
    if not isinstance(clarifications, list):
        raise ValueError("state clarifications must be a list")
    for item in clarifications:
        clarification = require_object(
            item, "clarification", {"stage", "origin", "question", "answer"}
        )
        if clarification["stage"] not in STAGES or clarification["origin"] not in {
            "author",
            "review",
            "router",
        }:
            raise ValueError("invalid clarification stage or origin")
        require_non_empty_string(clarification["question"], "clarification question")
        require_non_empty_string(clarification["answer"], "clarification answer")

    pending = state["pending"]
    if pending is not None:
        pending = require_object(
            pending,
            "pending",
            {"kind", "origin", "stage", "request", "response", "resume_purpose"},
        )
        if pending["kind"] not in {"clarification", "revision"}:
            raise ValueError("invalid pending kind")
        if pending["origin"] not in {"author", "review", "router"}:
            raise ValueError("invalid pending origin")
        if pending["stage"] != stage or pending["resume_purpose"] not in {"draft", "revise"}:
            raise ValueError("pending stage or resume purpose does not match state")
        require_non_empty_string(pending["request"], "pending request")
        if pending["response"] is not None:
            require_non_empty_string(pending["response"], "pending response")
            expected_status = (
                "drafting" if pending["resume_purpose"] == "draft" else "revising"
            )
            if status != expected_status:
                raise ValueError("answered pending item does not match resume status")
        elif status != "awaiting-decision":
            raise ValueError("unanswered pending item requires awaiting-decision")
    elif status == "awaiting-decision":
        raise ValueError("awaiting-decision requires a pending item")

    active_run = state["active_run"]
    if active_run is not None:
        active_run = require_object(
            active_run,
            "active run",
            {
                "run_id",
                "sequence",
                "stage",
                "role",
                "purpose",
                "executor",
                "adapter",
                "output",
                "request_sha256",
            },
        )
        if type(active_run["sequence"]) is not int or active_run["sequence"] < 1:
            raise ValueError("invalid active run sequence")
        if active_run["stage"] not in STAGES or active_run["role"] not in ROLES:
            raise ValueError("invalid active run stage or role")
        require_non_empty_string(active_run["executor"], "active run executor")
        if active_run["sequence"] + 1 != sequence:
            raise ValueError("active run sequence does not match next run sequence")
        expected_id = role_run_id(
            specification, active_run["stage"], active_run["role"], active_run["sequence"]
        )
        if active_run["run_id"] != expected_id:
            raise ValueError("active run ID is inconsistent")
        if active_run["stage"] != stage or active_run["purpose"] not in PURPOSES:
            raise ValueError("active run does not match current stage")
        if active_run["adapter"] not in ADAPTER_KINDS:
            raise ValueError("invalid active run adapter")
        if active_run["output"] != expected_output_path(
            specification, stage, active_run["role"]
        ):
            raise ValueError("invalid active run output")
        if active_run["request_sha256"] != request_hash:
            raise ValueError("active run request hash does not match state")
    if status == "waiting-executor" and (
        active_run is None or active_run["adapter"] != "mailbox"
    ):
        raise ValueError("waiting-executor requires an active mailbox run")
    if active_run is not None and status != "waiting-executor":
        expected_status = {
            "draft": "drafting",
            "revise": "revising",
            "review": "reviewing",
        }[active_run["purpose"]]
        if status != expected_status:
            raise ValueError("active run purpose does not match state status")
    if status in {"awaiting-approval", "awaiting-decision", "approved"} and active_run:
        raise ValueError(f"{status} state must not contain an active run")

    checkpoint_commit = state["checkpoint_commit"]
    if checkpoint_commit is not None:
        checkpoint_commit = require_object(
            checkpoint_commit,
            "checkpoint commit",
            {
                "stage",
                "action",
                "commit_message",
                "selection_event_key",
                "artifact_path",
                "artifact_sha256",
                "review_sha256",
                "accepted_risks",
                "verbatim",
            },
        )
        if checkpoint_commit["stage"] != stage:
            raise ValueError("checkpoint commit stage does not match current stage")
        if checkpoint_commit["action"] != "continue-and-commit":
            raise ValueError("checkpoint commit action must be continue-and-commit")
        if status != "awaiting-approval" or pending is not None or active_run is not None:
            raise ValueError("checkpoint commit requires an idle approval checkpoint")
        require_bounded_text(
            checkpoint_commit["commit_message"], "checkpoint commit message"
        )
        expected_commit_message = f"stepan({specification}): approve {stage}"
        if checkpoint_commit["commit_message"] != expected_commit_message:
            raise ValueError("checkpoint commit message is not canonical")
        selection_key = validate_audit_event_key(
            checkpoint_commit["selection_event_key"],
            "checkpoint commit selection event key",
        )
        expected_selection_key = f"checkpoint/{specification}/stage/{stage}/approved"
        if selection_key != expected_selection_key:
            raise ValueError("checkpoint commit selection event key is not canonical")
        expected_artifact = (
            f"docs/changes/specs/{specification}/{stage}.md"
        )
        if (
            require_project_relative_path(
                checkpoint_commit["artifact_path"], "checkpoint commit artifact path"
            )
            != expected_artifact
        ):
            raise ValueError("checkpoint commit artifact path does not match its stage")
        validate_audit_hash(
            checkpoint_commit["artifact_sha256"], "checkpoint commit artifact hash"
        )
        review_hash = checkpoint_commit["review_sha256"]
        if stage in {"requirements", "design"}:
            validate_audit_hash(review_hash, "checkpoint commit review hash")
        elif review_hash is not None:
            raise ValueError("unreviewed checkpoint commit must use a null review hash")
        risks = validate_audit_string_list(
            checkpoint_commit["accepted_risks"],
            "checkpoint commit accepted risks",
            AUDIT_MAX_REFERENCES_PER_DECISION,
        )
        if len(set(risks)) != len(risks):
            raise ValueError("checkpoint commit accepted risks contain a duplicate")
        if checkpoint_commit["verbatim"] is not None:
            require_bounded_text(
                checkpoint_commit["verbatim"], "checkpoint commit verbatim selection"
            )
    validate_audit_state(state["audit"], specification)
    execution = validate_execution_snapshot(state["execution"])
    if active_run is not None:
        expected_role = (
            STAGE_REVIEWERS.get(stage)
            if active_run["purpose"] == "review"
            else STAGE_OWNERS[stage]
        )
        if active_run["role"] != expected_role:
            raise ValueError("active run role is invalid for its stage and purpose")
        if execution["bindings"][active_run["role"]] != active_run["executor"]:
            raise ValueError("active run executor does not match persisted binding")
        profile = execution["profiles"][active_run["executor"]]
        expected_adapter = execution["adapters"][profile["adapter"]]["kind"]
        if active_run["adapter"] != expected_adapter:
            raise ValueError("active run adapter does not match persisted profile")
    return state


def validate_state_path(
    state_path: Path,
    project_root: Path,
    specification: str,
    *,
    _allow_pending_audit_suffix: bool = False,
) -> tuple[str, dict[str, object]]:
    if len(specification) > 63 or not SPEC_ID_PATTERN.fullmatch(specification):
        raise ValueError("invalid specification ID")
    project_root = canonical_project_root(project_root)
    expected_relative = f"docs/changes/specs/{specification}/state.yaml"
    expected_path = resolve_project_file(
        project_root, expected_relative, "state", CONTROL_FILE_MAX_BYTES
    )
    if state_path.absolute() != expected_path.absolute():
        raise ValueError("state file location does not match specification")
    content = read_bounded_utf8(expected_path, "state", CONTROL_FILE_MAX_BYTES)
    state = validate_state_value(parse_strict_yaml(content, "state"), specification)
    request_relative = f"docs/changes/specs/{specification}/request.md"
    request_path = resolve_project_file(
        project_root,
        request_relative,
        "initial request",
        ARTIFACT_MAX_BYTES,
    )
    request_bytes = request_path.read_bytes()
    try:
        canonical_request = canonicalize(request_bytes)
    except UnicodeError as error:
        raise ValueError("initial request must be UTF-8") from error
    if request_bytes != canonical_request:
        raise ValueError("request.md is not canonical UTF-8 with one trailing newline")
    if file_hash(request_path) != state["initial_request_sha256"]:
        raise ValueError("state initial request hash does not match request.md")

    audit = state["audit"]
    log_path = resolve_project_file(
        project_root,
        audit["log_path"],
        "feature audit log",
        AUDIT_LOG_MAX_BYTES,
    )
    log_bytes = log_path.read_bytes()
    accepted_log_bytes = log_bytes
    if _allow_pending_audit_suffix and audit["outbox"] is not None:
        accepted_size = audit["log_size_bytes"]
        if len(log_bytes) < accepted_size:
            raise ValueError("audit log is shorter than the state-accepted prefix")
        accepted_log_bytes = log_bytes[:accepted_size]
        if len(log_bytes) > accepted_size:
            validate_audit_log_extension(accepted_log_bytes, log_bytes)
    parsed_log = validate_audit_log(
        accepted_log_bytes,
        expected_log_size_bytes=audit["log_size_bytes"],
        expected_log_sha256=audit["log_sha256"],
        expected_head_event_id=audit["head_event_id"],
        expected_head_event_sha256=audit["head_event_sha256"],
        expected_next_event_sequence=audit["next_event_sequence"],
    )
    expected_header = {
        "schema_version": AUDIT_SCHEMA_VERSION,
        "specification": specification,
        "request_path": request_relative,
        "request_sha256": state["initial_request_sha256"],
    }
    if parsed_log["header"] != expected_header:
        raise ValueError("audit log header does not match the feature state and request")
    if not parsed_log["events"]:
        raise ValueError("feature audit log must contain the initial request event")
    if len(parsed_log["events"]) < 3:
        raise ValueError("feature audit log is missing its initialization lifecycle")
    first_event, created_event, entered_event = parsed_log["events"][:3]
    first_payload = first_event["payload"]
    if (
        first_payload["request_path"] != request_relative
        or first_payload["request_sha256"] != state["initial_request_sha256"]
    ):
        raise ValueError("initial audit event does not identify request.md")
    try:
        event_request = canonicalize(str(first_payload["verbatim"]).encode("utf-8"))
    except UnicodeError as error:
        raise ValueError("initial audit request must be UTF-8") from error
    if event_request != request_bytes:
        raise ValueError("initial audit request does not match request.md verbatim")
    expected_initial_events = (
        (
            first_event,
            "MEM-000001",
            f"interaction/{specification}/initial-request",
            "interaction",
            "initial-request-captured",
            "idea",
            "user",
            [],
        ),
        (
            created_event,
            "MEM-000002",
            f"spec/{specification}/created",
            "lifecycle",
            "specification-created",
            "workflow",
            "router",
            ["MEM-000001"],
        ),
        (
            entered_event,
            "MEM-000003",
            f"spec/{specification}/stage/idea/entered",
            "lifecycle",
            "stage-entered",
            "idea",
            "router",
            ["MEM-000002"],
        ),
    )
    for (
        event,
        event_id,
        event_key,
        kind,
        event_name,
        stage,
        actor,
        related_events,
    ) in expected_initial_events:
        if (
            event["event_id"],
            event["event_key"],
            event["kind"],
            event["event"],
            event["stage"],
            event["actor"],
            event["related_events"],
            event["recorded_at"],
        ) != (
            event_id,
            event_key,
            kind,
            event_name,
            stage,
            actor,
            related_events,
            state["created_at"],
        ):
            raise ValueError("audit log initialization lifecycle is not canonical")
    if created_event["payload"] != {
        "specification": specification,
        "request_path": request_relative,
        "request_sha256": state["initial_request_sha256"],
        "log_path": audit["log_path"],
    } or entered_event["payload"] != {"from_stage": None, "reason": "initial"}:
        raise ValueError("audit log initialization payload does not match state")
    if audit_decision_index(parsed_log["events"]) != audit["decision_index"]:
        raise ValueError("audit decision index does not match the durable log")
    if audit["outbox"] is not None:
        validate_audit_outbox(
            audit["outbox"],
            head_event_id=parsed_log["head_event_id"],
            head_event_sha256=parsed_log["head_event_sha256"],
            log_sha256=parsed_log["log_sha256"],
            next_event_sequence=parsed_log["next_event_sequence"],
        )
    active_run = state["active_run"]
    if active_run is not None:
        reservation_key = f"run/{active_run['run_id']}/reserved"
        reservation_events = [
            event
            for event in parsed_log["events"]
            if event["event_key"] == reservation_key
        ]
        if audit["outbox"] is not None:
            reservation_events.extend(
                event
                for event in audit["outbox"]["events"]
                if event["event_key"] == reservation_key
            )
        if len(reservation_events) != 1:
            raise ValueError("active run has no unique queued or durable reservation event")
        reservation_event = reservation_events[0]
        profile = state["execution"]["profiles"][active_run["executor"]]
        native = active_run["adapter"] in {"codex", "claude-code"}
        expected_reservation_payload = {
            "purpose": active_run["purpose"],
            "profile": active_run["executor"],
            "adapter": active_run["adapter"],
            "configured_agent": profile["agent"] if native else None,
            "requested_model": None if native else profile["model"],
            "requested_reasoning": None if native else profile.get("reasoning"),
            "output_path": active_run["output"],
        }
        if (
            reservation_event["kind"] != "lifecycle"
            or reservation_event["event"] != "role-run-reserved"
            or reservation_event["stage"] != active_run["stage"]
            or reservation_event["actor"] != "router"
            or reservation_event.get("run_id") != active_run["run_id"]
            or reservation_event["payload"] != expected_reservation_payload
        ):
            raise ValueError("active run reservation event does not match state")
    checkpoint_commit = state["checkpoint_commit"]
    if checkpoint_commit is not None:
        selection_key = str(checkpoint_commit["selection_event_key"])
        matching = [
            event
            for event in parsed_log["events"]
            if event["event_key"] == selection_key
        ]
        if not matching and audit["outbox"] is not None:
            matching = [
                event
                for event in audit["outbox"]["events"]
                if event["event_key"] == selection_key
            ]
        if len(matching) != 1:
            raise ValueError("checkpoint commit selection evidence is missing")
        selection = matching[0]
        expected_payload = {
            "action": "continue-and-commit",
            "verbatim": checkpoint_commit["verbatim"],
            "artifact_path": checkpoint_commit["artifact_path"],
            "artifact_sha256": checkpoint_commit["artifact_sha256"],
            "review_sha256": checkpoint_commit["review_sha256"],
            "commit_message": checkpoint_commit["commit_message"],
        }
        if (
            selection["kind"] != "interaction"
            or selection["event"] != "stage-approved"
            or selection["stage"] != checkpoint_commit["stage"]
            or selection["payload"] != expected_payload
        ):
            raise ValueError("checkpoint commit selection evidence does not match state")
    for profile in state["execution"]["profiles"].values():
        for project_input in profile["project_inputs"]:
            path = resolve_project_file(
                project_root,
                project_input["path"],
                "project input",
                PROJECT_INPUT_MAX_BYTES,
            )
            if file_hash(path) != project_input["sha256"]:
                raise ValueError(f"project input changed: {project_input['path']}")
    for approved_stage, approval in state["approvals"].items():
        artifact = resolve_project_file(
            project_root,
            f"docs/changes/specs/{specification}/{approved_stage}.md",
            f"approved {approved_stage} artifact",
            ARTIFACT_MAX_BYTES,
        )
        if file_hash(artifact) != approval["artifact_sha256"]:
            raise ValueError(f"approved {approved_stage} artifact hash does not match")
        if approved_stage in {"requirements", "design"}:
            review = resolve_project_file(
                project_root,
                f"docs/changes/specs/{specification}/review/{approved_stage}.yaml",
                f"approved {approved_stage} review",
                CONTROL_FILE_MAX_BYTES,
            )
            if file_hash(review) != approval["review_sha256"]:
                raise ValueError(f"approved {approved_stage} review hash does not match")
    if checkpoint_commit is not None:
        artifact = resolve_project_file(
            project_root,
            checkpoint_commit["artifact_path"],
            "checkpoint commit artifact",
            ARTIFACT_MAX_BYTES,
        )
        if file_hash(artifact) != checkpoint_commit["artifact_sha256"]:
            raise ValueError("checkpoint commit artifact hash does not match")
        if checkpoint_commit["stage"] in {"requirements", "design"}:
            review = resolve_project_file(
                project_root,
                f"docs/changes/specs/{specification}/review/{checkpoint_commit['stage']}.yaml",
                "checkpoint commit review",
                CONTROL_FILE_MAX_BYTES,
            )
            if file_hash(review) != checkpoint_commit["review_sha256"]:
                raise ValueError("checkpoint commit review hash does not match")
    return content, state


def validate_run_eligibility(
    state: dict[str, object],
    specification: str,
    stage: str,
    role: str,
    purpose: str,
    executor: str,
    adapter: str,
    output: str,
    request_sha256: str,
) -> None:
    require_empty_audit_outbox(state, "run reservation")
    if state["active_run"] is not None:
        raise ValueError("state already contains an active run")
    if state["stage"] != stage:
        raise ValueError("requested run stage does not match state")
    allowed: tuple[str, str] | None = None
    if state["status"] == "drafting":
        allowed = (STAGE_OWNERS[stage], "draft")
    elif state["status"] == "revising":
        allowed = (STAGE_OWNERS[stage], "revise")
    elif state["status"] == "reviewing" and stage in STAGE_REVIEWERS:
        allowed = (STAGE_REVIEWERS[stage], "review")
    if allowed != (role, purpose):
        raise ValueError("role and purpose are not allowed for current stage and status")
    pending = state["pending"]
    if pending is not None and (
        pending["response"] is None or pending["resume_purpose"] != purpose
    ):
        raise ValueError("pending question is not answered for this run purpose")
    if state["initial_request_sha256"] != request_sha256:
        raise ValueError("request hash does not match state")
    bindings = state["execution"]["bindings"]
    if bindings[role] != executor:
        raise ValueError("executor does not match persisted role binding")
    profile = state["execution"]["profiles"][executor]
    expected_adapter = state["execution"]["adapters"][profile["adapter"]]["kind"]
    if adapter != expected_adapter:
        raise ValueError("adapter does not match persisted executor profile")
    if output != expected_output_path(specification, stage, role):
        raise ValueError("output path does not match role and stage")


def reserved_state_content(
    content: str,
    state: dict[str, object],
    specification: str,
    stage: str,
    role: str,
    purpose: str,
    executor: str,
    adapter: str,
    output: str,
    request_sha256: str,
) -> tuple[str, dict[str, object]]:
    if not LOCAL_ID_PATTERN.fullmatch(executor):
        raise ValueError("invalid executor identifier")
    if purpose not in PURPOSES:
        raise ValueError("invalid run purpose")
    review_roles = {"requirements-reviewer", "specification-reviewer"}
    if role in review_roles and purpose != "review":
        raise ValueError("reviewer run must use review purpose")
    if role not in review_roles and purpose == "review":
        raise ValueError("author run cannot use review purpose")
    if adapter not in ADAPTER_KINDS:
        raise ValueError("invalid adapter kind")
    if not HASH_PATTERN.fullmatch(request_sha256):
        raise ValueError("invalid request hash")
    if PurePosixPath(output).as_posix() != output:
        raise ValueError("output must be a normalized POSIX path")
    if output != expected_output_path(specification, stage, role):
        raise ValueError("output path does not match role and stage")

    validate_run_eligibility(
        state,
        specification,
        stage,
        role,
        purpose,
        executor,
        adapter,
        output,
        request_sha256,
    )
    if parse_strict_yaml(content, "state") != state:
        raise ValueError("state content does not match its validated value")
    sequence = int(state["next_run_sequence"])
    reservation = run_reservation(specification, stage, role, sequence)
    updated = copy.deepcopy(state)
    updated["next_run_sequence"] = reservation["next_run_sequence"]
    updated["active_run"] = {
        "run_id": reservation["run_id"],
        "sequence": reservation["sequence"],
        "stage": stage,
        "role": role,
        "purpose": purpose,
        "executor": executor,
        "adapter": adapter,
        "output": output,
        "request_sha256": request_sha256,
    }
    profile = updated["execution"]["profiles"][executor]
    native = adapter in {"codex", "claude-code"}
    reservation_event = build_queued_audit_event(
        event_key=f"run/{reservation['run_id']}/reserved",
        kind="lifecycle",
        event="role-run-reserved",
        stage=stage,
        actor="router",
        run_id=reservation["run_id"],
        related_events=[updated["audit"]["head_event_id"]],
        payload={
            "purpose": purpose,
            "profile": executor,
            "adapter": adapter,
            "configured_agent": profile["agent"] if native else None,
            "requested_model": None if native else profile["model"],
            "requested_reasoning": None if native else profile.get("reasoning"),
            "output_path": output,
        },
    )
    reservation_transition = (
        "reviewer-run-reservation"
        if role in STAGE_REVIEWERS.values()
        else "owner-run-reservation"
    )
    validate_transition_event_batch(reservation_transition, [reservation_event])
    updated = queue_audit_transaction(updated, specification, [reservation_event])
    validate_state_value(updated, specification)
    return render_canonical_yaml_mapping(updated).decode("utf-8"), reservation


def reserve_run_in_state(
    state_path: Path,
    project_root: Path,
    specification: str,
    stage: str,
    role: str,
    purpose: str,
    executor: str,
    adapter: str,
    output: str,
    request_sha256: str,
) -> dict[str, object]:
    content, state = validate_state_path(state_path, project_root, specification)
    initial_stat = state_path.stat()
    initial_bytes = state_path.read_bytes()
    reserved_content, reservation = reserved_state_content(
        content,
        state,
        specification,
        stage,
        role,
        purpose,
        executor,
        adapter,
        output,
        request_sha256,
    )

    _atomic_replace_bytes(
        state_path,
        reserved_content.encode("utf-8"),
        expected_bytes=initial_bytes,
        expected_stat=initial_stat,
        operation="reserving run",
    )
    validate_state_path(state_path, project_root, specification)
    return reservation


def require_object(
    value: object, name: str, required: set[str], optional: set[str] | None = None
) -> dict[str, object]:
    if not isinstance(value, dict):
        raise ValueError(f"{name} must be an object")
    allowed = required | (optional or set())
    keys = set(value)
    if keys != required and not (required <= keys <= allowed):
        raise ValueError(f"invalid {name} fields")
    return value


def require_non_empty_string(value: object, name: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{name} must be a non-empty string")
    return value


def require_bounded_text(value: object, name: str) -> str:
    result = require_non_empty_string(value, name)
    if len(result) > AUDIT_MAX_TEXT_CHARACTERS:
        raise ValueError(f"{name} exceeds the audit text limit")
    return result


def require_project_relative_path(value: object, name: str) -> str:
    result = require_non_empty_string(value, name)
    relative = PurePosixPath(result)
    if (
        relative.is_absolute()
        or not relative.parts
        or relative.as_posix() != result
        or any(part in {"", ".", ".."} for part in relative.parts)
    ):
        raise ValueError(f"{name} must be a canonical project-relative path")
    return result


def format_audit_event_id(sequence: int) -> str:
    if type(sequence) is not int or sequence < 1:
        raise ValueError("audit event sequence must be positive")
    return f"MEM-{sequence:06d}"


def parse_audit_event_id(value: object, name: str = "audit event ID") -> int:
    result = require_non_empty_string(value, name)
    if not AUDIT_EVENT_ID_PATTERN.fullmatch(result):
        raise ValueError(f"invalid {name}")
    sequence = int(result.removeprefix("MEM-"))
    if result != format_audit_event_id(sequence):
        raise ValueError(f"non-canonical {name}")
    return sequence


def format_audit_transaction_id(first_event_sequence: int) -> str:
    if type(first_event_sequence) is not int or first_event_sequence < 1:
        raise ValueError("audit transaction sequence must be positive")
    return f"audit-tx-{first_event_sequence:06d}"


def parse_audit_transaction_id(
    value: object, name: str = "audit transaction ID"
) -> int:
    result = require_non_empty_string(value, name)
    if not AUDIT_TRANSACTION_ID_PATTERN.fullmatch(result):
        raise ValueError(f"invalid {name}")
    sequence = int(result.removeprefix("audit-tx-"))
    if result != format_audit_transaction_id(sequence):
        raise ValueError(f"non-canonical {name}")
    return sequence


def validate_audit_hash(value: object, name: str) -> str:
    result = require_non_empty_string(value, name)
    if not HASH_PATTERN.fullmatch(result):
        raise ValueError(f"invalid {name}")
    return result


def validate_audit_event_key(value: object, name: str = "audit event key") -> str:
    result = require_non_empty_string(value, name)
    if (
        len(result) > AUDIT_MAX_EVENT_KEY_CHARACTERS
        or not AUDIT_EVENT_KEY_PATTERN.fullmatch(result)
    ):
        raise ValueError(f"invalid {name}")
    return result


def validate_audit_recorded_at(value: object) -> str:
    result = require_non_empty_string(value, "audit recorded_at")
    if not AUDIT_RECORDED_AT_PATTERN.fullmatch(result):
        raise ValueError("invalid audit recorded_at")
    try:
        parsed = dt.datetime.strptime(result, "%Y-%m-%dT%H:%M:%SZ")
    except ValueError as error:
        raise ValueError("invalid audit recorded_at") from error
    if parsed.strftime("%Y-%m-%dT%H:%M:%SZ") != result:
        raise ValueError("non-canonical audit recorded_at")
    return result


def validate_decision_key(role: str, value: object, name: str = "decision key") -> str:
    if role not in ROLES:
        raise ValueError("invalid decision role")
    result = require_bounded_text(value, name)
    if role == "idea-author":
        valid = bool(IDEA_DECISION_KEY_PATTERN.fullmatch(result))
    elif role == "requirements-author":
        valid = bool(REQUIREMENT_REFERENCE_PATTERN.fullmatch(result))
    elif role == "requirements-reviewer":
        valid = result == "VERDICT" or bool(
            REQUIREMENTS_REVIEW_KEY_PATTERN.fullmatch(result)
        )
    elif role == "design-author":
        valid = bool(DESIGN_DECISION_KEY_PATTERN.fullmatch(result))
    elif role == "specification-reviewer":
        valid = result == "VERDICT" or bool(DESIGN_REVIEW_KEY_PATTERN.fullmatch(result))
    else:
        valid = bool(PLAN_DECISION_KEY_PATTERN.fullmatch(result))
    if not valid:
        raise ValueError(f"invalid {name} for {role}")
    return result


def validate_audit_alternatives(value: object, name: str) -> list[object]:
    if not isinstance(value, list) or len(value) > AUDIT_MAX_ALTERNATIVES_PER_DECISION:
        raise ValueError(f"{name} must be a bounded list")
    seen: set[tuple[str, str]] = set()
    for index, item in enumerate(value):
        alternative = require_object(
            item,
            f"{name}[{index}]",
            {"option", "rejected_because"},
        )
        option = require_bounded_text(alternative["option"], f"{name}[{index}].option")
        rejected = require_bounded_text(
            alternative["rejected_because"],
            f"{name}[{index}].rejected_because",
        )
        semantic_key = (option, rejected)
        if semantic_key in seen:
            raise ValueError(f"{name} contains a duplicate alternative")
        seen.add(semantic_key)
    return value


def validate_audit_string_list(
    value: object,
    name: str,
    maximum: int,
    *,
    allow_empty: bool = True,
    paths: bool = False,
) -> list[object]:
    if (
        not isinstance(value, list)
        or len(value) > maximum
        or (not allow_empty and not value)
    ):
        raise ValueError(f"{name} must be a bounded list")
    seen: set[str] = set()
    for index, item in enumerate(value):
        result = (
            require_project_relative_path(item, f"{name}[{index}]")
            if paths
            else require_bounded_text(item, f"{name}[{index}]")
        )
        if result in seen:
            raise ValueError(f"{name} contains a duplicate value")
        seen.add(result)
    return value


def validate_decision_value(value: object, role: str) -> dict[str, object]:
    decision = require_object(
        value,
        "receipt decision",
        {
            "key",
            "authority",
            "kind",
            "summary",
            "rationale",
            "alternatives",
            "references",
            "source_event_keys",
        },
    )
    validate_decision_key(role, decision["key"])
    authority = require_non_empty_string(decision["authority"], "decision authority")
    if authority not in DECISION_AUTHORITIES:
        raise ValueError("invalid decision authority")
    kind = require_non_empty_string(decision["kind"], "decision kind")
    if kind not in ROLE_DECISION_KINDS[role]:
        raise ValueError(f"invalid decision kind for {role}")
    require_bounded_text(decision["summary"], "decision summary")
    require_bounded_text(decision["rationale"], "decision rationale")
    validate_audit_alternatives(decision["alternatives"], "decision alternatives")
    validate_audit_string_list(
        decision["references"],
        "decision references",
        AUDIT_MAX_REFERENCES_PER_DECISION,
    )
    source_keys = decision["source_event_keys"]
    if not isinstance(source_keys, list) or len(source_keys) > AUDIT_MAX_REFERENCES_PER_DECISION:
        raise ValueError("decision source_event_keys must be a bounded list")
    seen_sources: set[str] = set()
    for index, source in enumerate(source_keys):
        result = validate_audit_event_key(source, f"decision source_event_keys[{index}]")
        if result in seen_sources:
            raise ValueError("decision source_event_keys contains a duplicate")
        seen_sources.add(result)
    if authority == "agent" and source_keys:
        raise ValueError("agent-authority decision must not name source events")
    if authority == "user" and not source_keys:
        raise ValueError("user-authority decision requires source events")
    return decision


def validate_decisions_snapshot(value: object, role: str) -> list[object]:
    if role not in ROLES:
        raise ValueError("invalid decision snapshot role")
    if not isinstance(value, list) or len(value) > AUDIT_MAX_DECISIONS_PER_RECEIPT:
        raise ValueError("receipt decisions must be a bounded list")
    seen: set[str] = set()
    for item in value:
        decision = validate_decision_value(item, role)
        key = str(decision["key"])
        if key in seen:
            raise ValueError("receipt decisions contain a duplicate key")
        seen.add(key)
    return value


def canonical_decision_semantic_bytes(value: object) -> bytes:
    decision = require_object(
        value,
        "semantic decision",
        set(DECISION_SEMANTIC_FIELDS),
    )
    authority = require_non_empty_string(
        decision["authority"], "semantic decision authority"
    )
    if authority not in DECISION_AUTHORITIES:
        raise ValueError("invalid semantic decision authority")
    kind = require_bounded_text(decision["kind"], "semantic decision kind")
    summary = require_bounded_text(decision["summary"], "semantic decision summary")
    rationale = require_bounded_text(
        decision["rationale"], "semantic decision rationale"
    )
    alternatives = validate_audit_alternatives(
        decision["alternatives"], "semantic decision alternatives"
    )
    references = validate_audit_string_list(
        decision["references"],
        "semantic decision references",
        AUDIT_MAX_REFERENCES_PER_DECISION,
    )
    canonical = {
        "authority": authority,
        "kind": kind,
        "summary": summary,
        "rationale": rationale,
        "alternatives": [
            {
                "option": alternative["option"],
                "rejected_because": alternative["rejected_because"],
            }
            for alternative in alternatives
        ],
        "references": list(references),
    }
    return json.dumps(
        canonical,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
    ).encode("utf-8")


def decision_semantic_sha256(value: object) -> str:
    return "sha256:" + hashlib.sha256(
        canonical_decision_semantic_bytes(value)
    ).hexdigest()


def canonical_receipt_sha256(value: object) -> str:
    if not isinstance(value, dict):
        raise ValueError("canonical receipt must be an object")
    content = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    if len(content) > CONTROL_FILE_MAX_BYTES:
        raise ValueError("canonical receipt exceeds the control-file limit")
    return "sha256:" + hashlib.sha256(content).hexdigest()


def decision_event_key_segment(value: object) -> str:
    key = require_bounded_text(value, "decision event-key source")
    if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:-]*", key):
        return key
    return "sha256-" + hashlib.sha256(key.encode("utf-8")).hexdigest()


def validate_decision_index(value: object) -> dict[str, object]:
    if not isinstance(value, dict) or any(role not in ROLES for role in value):
        raise ValueError("audit decision_index must be role-namespaced")
    for role, entries in value.items():
        if not isinstance(entries, dict) or not entries:
            raise ValueError("audit decision-index role namespace must be non-empty")
        for key, entry_value in entries.items():
            validate_decision_key(role, key, "audit decision-index key")
            entry = require_object(
                entry_value,
                f"audit decision-index entry {role}/{key}",
                {"event_id", "semantic_sha256"},
            )
            parse_audit_event_id(entry["event_id"], "audit decision-index event ID")
            validate_audit_hash(
                entry["semantic_sha256"], "audit decision-index semantic hash"
            )
    return value


def _validate_audit_payload_field(
    value: object, rule: object, name: str, role: str | None
) -> None:
    if isinstance(rule, tuple):
        if value not in rule:
            raise ValueError(f"invalid {name}")
    elif rule == "text":
        require_bounded_text(value, name)
    elif rule == "nullable_text":
        if value is not None:
            require_bounded_text(value, name)
    elif rule == "hash":
        validate_audit_hash(value, name)
    elif rule == "nullable_hash":
        if value is not None:
            validate_audit_hash(value, name)
    elif rule == "project_path":
        require_project_relative_path(value, name)
    elif rule == "project_path_list":
        validate_audit_string_list(
            value, name, AUDIT_MAX_REFERENCES_PER_DECISION, allow_empty=False, paths=True
        )
    elif rule == "text_list":
        validate_audit_string_list(
            value, name, AUDIT_MAX_REFERENCES_PER_DECISION
        )
    elif rule == "alternatives":
        validate_audit_alternatives(value, name)
    elif rule == "references":
        validate_audit_string_list(
            value, name, AUDIT_MAX_REFERENCES_PER_DECISION
        )
    elif rule == "event_id":
        parse_audit_event_id(value, name)
    elif rule == "spec_id":
        text = require_non_empty_string(value, name)
        if len(text) > 63 or not SPEC_ID_PATTERN.fullmatch(text):
            raise ValueError(f"invalid {name}")
    elif rule == "stage":
        if value not in STAGES:
            raise ValueError(f"invalid {name}")
    elif rule == "nullable_stage":
        if value is not None and value not in STAGES:
            raise ValueError(f"invalid {name}")
    elif rule == "decision_kind":
        if role is None or value not in ROLE_DECISION_KINDS[role]:
            raise ValueError(f"invalid {name}")
    elif rule == "positive_int":
        if type(value) is not int or value < 1:
            raise ValueError(f"invalid {name}")
    elif rule == "non_negative_int":
        if type(value) is not int or value < 0:
            raise ValueError(f"invalid {name}")
    elif rule == "runtime_identity":
        text = require_bounded_text(value, name)
        if text != "unavailable" and not text.strip():
            raise ValueError(f"invalid {name}")
    else:
        raise AssertionError(f"unknown audit payload validation rule: {rule!r}")


def validate_audit_payload(
    value: object, kind: str, event: str, actor: str, stage: str
) -> dict[str, object]:
    schema = AUDIT_EVENT_PAYLOAD_SCHEMAS.get((kind, event))
    if schema is None:
        raise ValueError("unknown audit event schema")
    payload = require_object(value, f"audit payload for {event}", set(schema))
    role = actor if actor in ROLES else None
    for field, rule in schema.items():
        _validate_audit_payload_field(payload[field], rule, f"audit payload {field}", role)
    if role is not None and "key" in payload:
        validate_decision_key(role, payload["key"], "audit payload decision key")
    if event == "stage-approved":
        if (stage in {"requirements", "design"}) != (payload["review_sha256"] is not None):
            raise ValueError("audit approval review hash does not match its stage")
        commit_selected = payload["action"] == "continue-and-commit"
        if commit_selected != (payload["commit_message"] is not None):
            raise ValueError("audit approval commit message does not match its action")
    elif event == "risk-accepted" and not payload["risks"]:
        raise ValueError("audit risk acceptance requires at least one risk")
    elif event == "stage-entered":
        if (payload["reason"] == "initial") != (payload["from_stage"] is None):
            raise ValueError("audit stage entry source does not match its reason")
    elif event == "role-run-reserved":
        native = payload["adapter"] in {"codex", "claude-code"}
        if native:
            if payload["configured_agent"] is None or any(
                payload[field] is not None
                for field in ("requested_model", "requested_reasoning")
            ):
                raise ValueError("invalid native audit executor provenance")
        elif payload["configured_agent"] is not None or payload["requested_model"] is None:
            raise ValueError("invalid mailbox audit executor provenance")
    elif event == "review-accepted":
        pattern = (
            REQUIREMENTS_REVIEW_KEY_PATTERN
            if stage == "requirements"
            else DESIGN_REVIEW_KEY_PATTERN
        )
        if any(not pattern.fullmatch(item) for item in payload["finding_ids"]):
            raise ValueError("audit review contains an invalid finding ID")
    elif event in {"agent-decision-introduced", "agent-decision-revised"}:
        expected_semantic_hash = decision_semantic_sha256(
            {
                "authority": "agent",
                "kind": payload["decision_kind"],
                "summary": payload["summary"],
                "rationale": payload["rationale"],
                "alternatives": payload["alternatives"],
                "references": payload["references"],
            }
        )
        if payload["semantic_sha256"] != expected_semantic_hash:
            raise ValueError("audit decision semantic hash does not match its payload")
    elif event == "automatic-revision-started" and payload["attempt"] > 3:
        raise ValueError("audit automatic revision attempt exceeds the limit")
    elif event == "automatic-revision-limit-reached" and payload["attempt"] != 3:
        raise ValueError("audit automatic revision limit event requires attempt 3")
    elif event == "upstream-invalidated":
        if STAGES.index(payload["to_stage"]) >= STAGES.index(payload["from_stage"]):
            raise ValueError("audit upstream invalidation must return to an earlier stage")
    return payload


def _validate_audit_event_identity(
    event: dict[str, object], *, durable: bool
) -> tuple[str, str, str, str | None]:
    kind = require_non_empty_string(event["kind"], "audit event kind")
    name = require_non_empty_string(event["event"], "audit event name")
    stage = require_non_empty_string(event["stage"], "audit event stage")
    actor = require_non_empty_string(event["actor"], "audit event actor")
    if kind not in AUDIT_KINDS or name not in AUDIT_EVENTS_BY_KIND.get(kind, ()):
        raise ValueError("invalid audit event kind or name")
    if stage not in AUDIT_STAGES or actor not in AUDIT_ACTORS:
        raise ValueError("invalid audit event stage or actor")
    if actor in ROLES and ROLE_STAGES[actor] != stage:
        raise ValueError("audit role actor does not match event stage")

    if kind == "decision":
        if actor not in ROLES or stage == "workflow":
            raise ValueError("audit decision event requires a role actor and product stage")
    elif kind == "lifecycle" and actor != "router":
        raise ValueError("audit lifecycle event actor must be router")
    elif kind == "interaction":
        expected_actors: dict[str, tuple[str, ...]] = {
            "initial-request-captured": ("user",),
            "blocking-question": (*ROLES, "router"),
            "user-response": ("user",),
            "clarification-applied": ROLES,
            "user-question": ("user",),
            "agent-answer": ("router",),
            "stage-approved": ("user",),
            "revision-feedback-submitted": ("user",),
            "risk-accepted": ("user",),
            "specification-collision-selected": ("user",),
            "recovery-selected": ("user",),
            "flow-stopped": ("user",),
        }
        if actor not in expected_actors[name]:
            raise ValueError("invalid audit interaction actor")

    workflow_only = {
        "specification-created",
        "specification-collision-selected",
        "recovery-selected",
        "workflow-resumed",
        "workflow-completed",
    }
    if (name in workflow_only) != (stage == "workflow"):
        raise ValueError("audit event uses an invalid workflow stage")
    if name == "initial-request-captured" and stage != "idea":
        raise ValueError("initial request must use the idea stage")

    payload = validate_audit_payload(event["payload"], kind, name, actor, stage)
    run_id = event.get("run_id")
    router_question = (
        name == "blocking-question"
        and actor == "router"
        and payload["origin"] == "router"
    )
    run_required = kind == "decision" or name in {
        "clarification-applied",
        "role-run-reserved",
        "role-run-completed",
        "role-run-blocked",
        "role-run-failed",
        "role-run-interrupted",
        "executor-wait-started",
        "executor-wait-ended",
        "artifact-accepted",
        "review-accepted",
    } or (name == "blocking-question" and not router_question)
    if run_required:
        run_text = require_non_empty_string(run_id, "audit event run_id")
        match = AUDIT_RUN_ID_PATTERN.fullmatch(run_text)
        if (
            match is None
            or len(match.group("specification")) > 63
            or match.group("stage") != stage
        ):
            raise ValueError("audit event run_id does not match its stage")
        if actor in ROLES and match.group("role") != actor:
            raise ValueError("audit event run_id does not match its role actor")
        run_id = run_text
    elif run_id is not None:
        raise ValueError("audit event must not contain run_id")

    if name == "blocking-question":
        origin = payload["origin"]
        if origin == "router":
            if actor != "router" or run_id is not None:
                raise ValueError("router blocking question must not use a role run")
        else:
            expected_actor = (
                STAGE_OWNERS[stage]
                if origin == "author"
                else STAGE_REVIEWERS.get(stage)
            )
            if actor != expected_actor or run_id is None:
                raise ValueError("role blocking question actor does not match its origin")

    event_key = validate_audit_event_key(event["event_key"])
    if run_id is not None:
        if not event_key.startswith(f"run/{run_id}/"):
            raise ValueError("run audit event key must be namespaced by run_id")
    elif not event_key.startswith(("spec/", "interaction/", "workflow/", "checkpoint/")):
        raise ValueError("non-run audit event key uses an invalid namespace")

    return kind, name, actor, run_id


def validate_queued_audit_event(value: object) -> dict[str, object]:
    event = require_object(
        value,
        "queued audit event",
        {
            "event_key",
            "kind",
            "event",
            "stage",
            "actor",
            "related_events",
            "related_event_keys",
            "payload",
        },
        {"run_id"},
    )
    _validate_audit_event_identity(event, durable=False)
    related_events = event["related_events"]
    if not isinstance(related_events, list) or len(related_events) > AUDIT_MAX_REFERENCES_PER_DECISION:
        raise ValueError("queued audit related_events must be a bounded list")
    seen_ids: set[str] = set()
    for index, related in enumerate(related_events):
        parse_audit_event_id(related, f"queued audit related_events[{index}]")
        if related in seen_ids:
            raise ValueError("queued audit related_events contains a duplicate")
        seen_ids.add(related)
    related_keys = event["related_event_keys"]
    if not isinstance(related_keys, list) or len(related_keys) > AUDIT_MAX_REFERENCES_PER_DECISION:
        raise ValueError("queued audit related_event_keys must be a bounded list")
    seen_keys: set[str] = set()
    for index, related in enumerate(related_keys):
        key = validate_audit_event_key(
            related, f"queued audit related_event_keys[{index}]"
        )
        if key == event["event_key"] or key in seen_keys:
            raise ValueError("queued audit related_event_keys is cyclic or duplicated")
        seen_keys.add(key)
    return event


def validate_durable_audit_event(value: object) -> dict[str, object]:
    event = require_object(
        value,
        "durable audit event",
        {
            "event_id",
            "event_key",
            "recorded_at",
            "kind",
            "event",
            "stage",
            "actor",
            "related_events",
            "previous_event_sha256",
            "event_sha256",
            "payload",
        },
        {"run_id"},
    )
    _validate_audit_event_identity(event, durable=True)
    sequence = parse_audit_event_id(event["event_id"])
    validate_audit_recorded_at(event["recorded_at"])
    previous = event["previous_event_sha256"]
    if sequence == 1:
        if previous is not None:
            raise ValueError("first audit event must have a null previous hash")
    else:
        validate_audit_hash(previous, "audit previous event hash")
    validate_audit_hash(event["event_sha256"], "audit event hash")
    related_events = event["related_events"]
    if not isinstance(related_events, list) or len(related_events) > AUDIT_MAX_REFERENCES_PER_DECISION:
        raise ValueError("durable audit related_events must be a bounded list")
    seen: set[str] = set()
    for index, related in enumerate(related_events):
        related_sequence = parse_audit_event_id(
            related, f"durable audit related_events[{index}]"
        )
        if related_sequence >= sequence or related in seen:
            raise ValueError("durable audit related event must be unique and prior")
        seen.add(related)
    return event


def validate_audit_outbox(
    value: object,
    *,
    head_event_id: object,
    head_event_sha256: object,
    log_sha256: str,
    next_event_sequence: int,
) -> dict[str, object]:
    outbox = require_object(
        value,
        "audit outbox",
        {
            "transaction_id",
            "expected_head_event_id",
            "expected_head_event_sha256",
            "expected_log_sha256",
            "events",
            "decision_index_after",
        },
    )
    transaction_sequence = parse_audit_transaction_id(outbox["transaction_id"])
    if transaction_sequence != next_event_sequence:
        raise ValueError("audit transaction ID does not match next event sequence")
    if (
        outbox["expected_head_event_id"] != head_event_id
        or outbox["expected_head_event_sha256"] != head_event_sha256
        or outbox["expected_log_sha256"] != log_sha256
    ):
        raise ValueError("audit outbox was prepared against a different head")
    events = outbox["events"]
    if (
        not isinstance(events, list)
        or not events
        or len(events) > AUDIT_MAX_EVENTS_PER_TRANSACTION
    ):
        raise ValueError("audit outbox events must be a non-empty bounded list")
    seen_keys: set[str] = set()
    event_positions: dict[str, int] = {}
    previous_order: tuple[int, str, str, str] | None = None
    for index, event_value in enumerate(events):
        event = validate_queued_audit_event(event_value)
        key = str(event["event_key"])
        if key in seen_keys:
            raise ValueError("audit outbox contains a duplicate event key")
        order = (
            AUDIT_EVENT_ORDER[(str(event["kind"]), str(event["event"]))],
            str(event["kind"]),
            str(event["event"]),
            key,
        )
        if previous_order is not None and order < previous_order:
            raise ValueError("audit outbox events are not in canonical order")
        previous_order = order
        seen_keys.add(key)
        event_positions[key] = index
        for related_id in event["related_events"]:
            if parse_audit_event_id(related_id) >= next_event_sequence:
                raise ValueError("queued audit related event ID is not in the accepted head")
        for related_key in event["related_event_keys"]:
            if related_key not in event_positions:
                raise ValueError("queued audit related event key must refer to an earlier event")
    decision_index_after = validate_decision_index(outbox["decision_index_after"])
    final_sequence = next_event_sequence + len(events) - 1
    for entries in decision_index_after.values():
        for entry in entries.values():
            if parse_audit_event_id(entry["event_id"]) > final_sequence:
                raise ValueError("audit decision_index_after refers beyond its transaction")
    return outbox


def validate_audit_state(value: object, specification: str) -> dict[str, object]:
    if len(specification) > 63 or not SPEC_ID_PATTERN.fullmatch(specification):
        raise ValueError("invalid audit specification ID")
    audit = require_object(
        value,
        "state.audit",
        {
            "schema_version",
            "log_path",
            "log_size_bytes",
            "log_sha256",
            "head_event_id",
            "head_event_sha256",
            "next_event_sequence",
            "decision_index",
            "outbox",
        },
    )
    if type(audit["schema_version"]) is not int or audit["schema_version"] != AUDIT_SCHEMA_VERSION:
        raise ValueError("state.audit schema_version must be 1")
    expected_path = f"docs/changes/specs/{specification}/mem-log.md"
    if require_project_relative_path(audit["log_path"], "state.audit log_path") != expected_path:
        raise ValueError("state.audit log_path does not match the specification")
    log_size = audit["log_size_bytes"]
    if type(log_size) is not int or not 0 < log_size <= AUDIT_LOG_MAX_BYTES:
        raise ValueError("invalid state.audit log_size_bytes")
    log_hash = validate_audit_hash(audit["log_sha256"], "state.audit log hash")
    head_id = audit["head_event_id"]
    head_hash = audit["head_event_sha256"]
    sequence = audit["next_event_sequence"]
    if type(sequence) is not int or sequence < 1:
        raise ValueError("invalid state.audit next_event_sequence")
    if head_id is None:
        if head_hash is not None or sequence != 1:
            raise ValueError("empty audit head is inconsistent")
    else:
        head_sequence = parse_audit_event_id(head_id, "state.audit head_event_id")
        validate_audit_hash(head_hash, "state.audit head event hash")
        if sequence != head_sequence + 1:
            raise ValueError("state.audit next event sequence does not follow its head")
    validate_decision_index(audit["decision_index"])
    if head_id is None and audit["decision_index"]:
        raise ValueError("empty audit head must have an empty decision index")
    if head_id is not None:
        for entries in audit["decision_index"].values():
            for entry in entries.values():
                if parse_audit_event_id(entry["event_id"]) >= sequence:
                    raise ValueError("audit decision index refers beyond the current head")
    if audit["outbox"] is not None:
        validate_audit_outbox(
            audit["outbox"],
            head_event_id=head_id,
            head_event_sha256=head_hash,
            log_sha256=log_hash,
            next_event_sequence=sequence,
        )
    return audit


def require_empty_audit_outbox(
    state: dict[str, object], operation: str
) -> None:
    audit = state.get("audit")
    if not isinstance(audit, dict) or "outbox" not in audit:
        raise ValueError("workflow state is missing mandatory audit metadata")
    if audit["outbox"] is not None:
        raise ValueError(
            f"cannot continue {operation} while an audit outbox is pending; flush it first"
        )


def build_queued_audit_event(
    *,
    event_key: str,
    kind: str,
    event: str,
    stage: str,
    actor: str,
    payload: dict[str, object],
    related_events: list[object] | None = None,
    related_event_keys: list[object] | None = None,
    run_id: str | None = None,
) -> dict[str, object]:
    queued: dict[str, object] = {
        "event_key": event_key,
        "kind": kind,
        "event": event,
        "stage": stage,
        "actor": actor,
        "related_events": list(related_events or []),
        "related_event_keys": list(related_event_keys or []),
        "payload": copy.deepcopy(payload),
    }
    if run_id is not None:
        queued["run_id"] = run_id
    return validate_queued_audit_event(queued)


def _audit_event_sort_key(event: dict[str, object]) -> tuple[int, str, str, str]:
    return (
        AUDIT_EVENT_ORDER[(str(event["kind"]), str(event["event"]))],
        str(event["kind"]),
        str(event["event"]),
        str(event["event_key"]),
    )


def queue_audit_transaction(
    state: dict[str, object],
    specification: str,
    events: object,
    *,
    decision_index_after: object | None = None,
) -> dict[str, object]:
    validate_state_value(state, specification)
    require_empty_audit_outbox(state, "workflow mutation")
    if not isinstance(events, list) or not events:
        raise ValueError("audit transaction requires a non-empty event list")
    queued_events = [copy.deepcopy(validate_queued_audit_event(item)) for item in events]
    queued_events.sort(key=_audit_event_sort_key)
    audit = state["audit"]
    updated = copy.deepcopy(state)
    updated_audit = updated["audit"]
    next_sequence = int(audit["next_event_sequence"])
    updated_audit["outbox"] = {
        "transaction_id": f"audit-tx-{next_sequence:06d}",
        "expected_head_event_id": audit["head_event_id"],
        "expected_head_event_sha256": audit["head_event_sha256"],
        "expected_log_sha256": audit["log_sha256"],
        "events": queued_events,
        "decision_index_after": copy.deepcopy(
            audit["decision_index"]
            if decision_index_after is None
            else validate_decision_index(decision_index_after)
        ),
    }
    validate_state_value(updated, specification)
    return updated


def audit_event_type(event: dict[str, object]) -> str:
    return f"{event['kind']}/{event['event']}"


def validate_transition_event_batch(
    transition: str, events: object
) -> list[dict[str, object]]:
    definition = FEATURE_TRANSITION_TABLE.get(transition)
    if definition is None or not definition["audited"]:
        raise ValueError("transition does not define an audited event batch")
    if not isinstance(events, list) or not events:
        raise ValueError("audited transition requires a non-empty event batch")
    validated = [validate_queued_audit_event(event) for event in events]
    actual = [audit_event_type(event) for event in validated]
    allowed = set(definition["events"])
    if definition.get("dynamic_decisions"):
        allowed.update(
            {
                "decision/agent-decision-introduced",
                "decision/agent-decision-revised",
                "decision/agent-decision-retired",
            }
        )
    unexpected = [event for event in actual if event not in allowed]
    if unexpected:
        raise ValueError(
            f"transition {transition} contains an undeclared event: {unexpected[0]}"
        )
    required: set[str] = set()
    if transition == "owner-result-accepted":
        required = {
            "lifecycle/role-run-completed",
            "lifecycle/artifact-accepted",
        }
    elif transition == "reviewer-result-accepted":
        required = {
            "lifecycle/role-run-completed",
            "lifecycle/review-accepted",
        }
    elif transition == "initialization":
        required = set(definition["events"])
    elif definition["mode"] == "workflow-transition":
        required = set(definition["events"])
        if transition in {"approve-stage", "upstream-invalidation"}:
            required.discard("interaction/risk-accepted")
            required.discard("interaction/revision-feedback-submitted")
            required.discard("lifecycle/stage-entered")
            required.discard("lifecycle/workflow-completed")
        if transition == "checkpoint-commit-succeeded":
            required.discard("lifecycle/stage-entered")
            required.discard("lifecycle/workflow-completed")
    missing = sorted(required - set(actual))
    if missing:
        raise ValueError(
            f"transition {transition} is missing its declared event: {missing[0]}"
        )
    return validated


def prepare_role_decision_diff(
    role: str,
    decisions: object,
    decision_index: object,
    *,
    run_id: str,
    artifact_path: str,
    artifact_sha256: str,
    receipt_sha256: str,
    acceptance_event_key: str,
) -> dict[str, object]:
    snapshot = validate_decisions_snapshot(decisions, role)
    current_index = copy.deepcopy(validate_decision_index(decision_index))
    match = AUDIT_RUN_ID_PATTERN.fullmatch(
        require_non_empty_string(run_id, "decision diff run ID")
    )
    if match is None or match.group("role") != role:
        raise ValueError("decision diff run ID does not match its role")
    stage = match.group("stage")
    if stage != ROLE_STAGES[role]:
        raise ValueError("decision diff run stage does not match its role")
    artifact_path = require_project_relative_path(
        artifact_path, "decision diff artifact path"
    )
    artifact_sha256 = validate_audit_hash(
        artifact_sha256, "decision diff artifact hash"
    )
    receipt_sha256 = validate_audit_hash(
        receipt_sha256, "decision diff receipt hash"
    )
    acceptance_event_key = validate_audit_event_key(
        acceptance_event_key, "decision diff acceptance event key"
    )

    agent_snapshot = {
        str(decision["key"]): decision
        for decision in snapshot
        if decision["authority"] == "agent"
    }
    previous = current_index.get(role, {})
    events: list[dict[str, object]] = []
    unchanged: list[str] = []
    event_keys: set[str] = set()

    for key in sorted(agent_snapshot):
        decision = agent_snapshot[key]
        semantic_hash = decision_semantic_sha256(
            {field: decision[field] for field in DECISION_SEMANTIC_FIELDS}
        )
        prior = previous.get(key)
        if prior is not None and prior["semantic_sha256"] == semantic_hash:
            unchanged.append(key)
            continue
        event_name = (
            "agent-decision-introduced"
            if prior is None
            else "agent-decision-revised"
        )
        event_key = (
            f"run/{run_id}/decision/{decision_event_key_segment(key)}"
        )
        if event_key in event_keys:
            raise ValueError("decision keys produce a duplicate audit event key")
        event_keys.add(event_key)
        payload = {
            "key": key,
            "decision_kind": decision["kind"],
            "summary": decision["summary"],
            "rationale": decision["rationale"],
            "alternatives": copy.deepcopy(decision["alternatives"]),
            "references": list(decision["references"]),
            "artifact_path": artifact_path,
            "artifact_sha256": artifact_sha256,
            "receipt_sha256": receipt_sha256,
            "semantic_sha256": semantic_hash,
        }
        related_events: list[object] = []
        if prior is not None:
            payload["supersedes_event_id"] = prior["event_id"]
            related_events.append(prior["event_id"])
        events.append(
            build_queued_audit_event(
                event_key=event_key,
                kind="decision",
                event=event_name,
                stage=stage,
                actor=role,
                run_id=run_id,
                related_events=related_events,
                related_event_keys=[acceptance_event_key],
                payload=payload,
            )
        )

    for key in sorted(set(previous) - set(agent_snapshot)):
        prior = previous[key]
        event_key = (
            f"run/{run_id}/decision/{decision_event_key_segment(key)}"
        )
        if event_key in event_keys:
            raise ValueError("decision keys produce a duplicate audit event key")
        event_keys.add(event_key)
        events.append(
            build_queued_audit_event(
                event_key=event_key,
                kind="decision",
                event="agent-decision-retired",
                stage=stage,
                actor=role,
                run_id=run_id,
                related_events=[prior["event_id"]],
                related_event_keys=[acceptance_event_key],
                payload={
                    "key": key,
                    "retired_event_id": prior["event_id"],
                    "reason": AUDIT_DECISION_RETIREMENT_REASON,
                    "artifact_path": artifact_path,
                    "artifact_sha256": artifact_sha256,
                    "receipt_sha256": receipt_sha256,
                },
            )
        )
    return {
        "events": events,
        "unchanged_keys": unchanged,
    }


def decision_index_after_queued_events(
    decision_index: object,
    events: object,
    first_event_sequence: int,
) -> dict[str, object]:
    updated = copy.deepcopy(validate_decision_index(decision_index))
    if not isinstance(events, list):
        raise ValueError("queued events must be a list")
    if type(first_event_sequence) is not int or first_event_sequence < 1:
        raise ValueError("first queued event sequence must be positive")
    queued = [copy.deepcopy(validate_queued_audit_event(event)) for event in events]
    queued.sort(key=_audit_event_sort_key)
    for offset, event in enumerate(queued):
        if event["kind"] != "decision":
            continue
        role = str(event["actor"])
        payload = event["payload"]
        key = str(payload["key"])
        role_index = updated.setdefault(role, {})
        current = role_index.get(key)
        event_id = format_audit_event_id(first_event_sequence + offset)
        if event["event"] == "agent-decision-introduced":
            if current is not None:
                raise ValueError("queued audit introduces an active decision twice")
            role_index[key] = {
                "event_id": event_id,
                "semantic_sha256": payload["semantic_sha256"],
            }
        elif event["event"] == "agent-decision-revised":
            if current is None or payload["supersedes_event_id"] != current["event_id"]:
                raise ValueError("queued audit revision does not supersede its active decision")
            role_index[key] = {
                "event_id": event_id,
                "semantic_sha256": payload["semantic_sha256"],
            }
        else:
            if current is None or payload["retired_event_id"] != current["event_id"]:
                raise ValueError("queued audit retirement does not name its active decision")
            del role_index[key]
            if not role_index:
                del updated[role]
    return validate_decision_index(updated)


class _AuditMarkdownParser:
    """Small byte cursor for canonical audit Markdown."""

    def __init__(self, content: bytes) -> None:
        self.content = content
        self.position = 0

    def expect(self, expected: bytes, name: str) -> None:
        if not self.content.startswith(expected, self.position):
            raise ValueError(f"invalid audit Markdown {name}")
        self.position += len(expected)

    def line(self, name: str) -> bytes:
        end = self.content.find(b"\n", self.position)
        if end < 0:
            raise ValueError(f"truncated audit Markdown {name}")
        result = self.content[self.position : end]
        self.position = end + 1
        return result


def audit_bytes_sha256(content: bytes) -> str:
    if not isinstance(content, bytes):
        raise ValueError("audit content must be bytes")
    return f"sha256:{hashlib.sha256(content).hexdigest()}"


def _validate_audit_markdown_bytes(content: object, name: str) -> bytes:
    if not isinstance(content, bytes):
        raise ValueError(f"{name} must be bytes")
    if len(content) > AUDIT_LOG_MAX_BYTES:
        raise ValueError(f"{name} exceeds {AUDIT_LOG_MAX_BYTES} bytes")
    if not content:
        raise ValueError(f"{name} must not be empty")
    if content.startswith(b"\xef\xbb\xbf"):
        raise ValueError(f"{name} must not contain a UTF-8 BOM")
    try:
        content.decode("utf-8")
    except UnicodeDecodeError as error:
        raise ValueError(f"{name} must be canonical UTF-8") from error
    if not content.endswith(b"\n"):
        raise ValueError(f"{name} must end with a newline")
    return content


def _audit_header_value(
    specification: object, request_path: object, request_sha256: object
) -> dict[str, object]:
    spec = require_non_empty_string(specification, "audit header specification")
    if len(spec) > 63 or not SPEC_ID_PATTERN.fullmatch(spec):
        raise ValueError("invalid audit header specification")
    path = require_project_relative_path(request_path, "audit header request path")
    expected_path = f"docs/changes/specs/{spec}/request.md"
    if path != expected_path:
        raise ValueError("audit header request path does not match the specification")
    request_hash = validate_audit_hash(
        request_sha256, "audit header initial request hash"
    )
    return {
        "schema_version": AUDIT_SCHEMA_VERSION,
        "specification": spec,
        "request_path": path,
        "request_sha256": request_hash,
    }


def render_audit_header(
    specification: object, request_path: object, request_sha256: object
) -> bytes:
    header = _audit_header_value(specification, request_path, request_sha256)
    return (
        "# Stepan Feature Audit Log\n\n"
        f"- Schema version: `{header['schema_version']}`\n"
        f"- Specification: `{header['specification']}`\n"
        f"- Initial request: `{header['request_path']}`\n"
        f"- Initial request SHA-256: `{header['request_sha256']}`\n\n"
        f"{AUDIT_HEADER_NOTICE}"
    ).encode("utf-8")


def _parse_inline_code_value(line: bytes, prefix: bytes, name: str) -> str:
    if not line.startswith(prefix + b"`") or not line.endswith(b"`"):
        raise ValueError(f"invalid audit Markdown {name}")
    raw = line[len(prefix) + 1 : -1]
    if b"`" in raw:
        raise ValueError(f"invalid audit Markdown {name}")
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError as error:
        raise ValueError(f"invalid audit Markdown {name}") from error


def _parse_audit_header_prefix(
    parser: _AuditMarkdownParser,
) -> dict[str, object]:
    parser.expect(b"# Stepan Feature Audit Log\n\n", "header title")
    version_text = _parse_inline_code_value(
        parser.line("schema version"), b"- Schema version: ", "schema version"
    )
    if version_text != str(AUDIT_SCHEMA_VERSION):
        raise ValueError("unsupported audit log schema version")
    specification = _parse_inline_code_value(
        parser.line("specification"), b"- Specification: ", "specification"
    )
    request_path = _parse_inline_code_value(
        parser.line("initial request path"),
        b"- Initial request: ",
        "initial request path",
    )
    request_sha256 = _parse_inline_code_value(
        parser.line("initial request hash"),
        b"- Initial request SHA-256: ",
        "initial request hash",
    )
    parser.expect(b"\n" + AUDIT_HEADER_NOTICE.encode("utf-8"), "header notice")
    header = _audit_header_value(specification, request_path, request_sha256)
    canonical = render_audit_header(specification, request_path, request_sha256)
    if parser.content[: parser.position] != canonical:
        raise ValueError("non-canonical audit Markdown header")
    return header


def parse_audit_header(content: object) -> dict[str, object]:
    raw = _validate_audit_markdown_bytes(content, "audit header")
    parser = _AuditMarkdownParser(raw)
    result = _parse_audit_header_prefix(parser)
    if parser.position != len(raw):
        raise ValueError("audit header contains trailing content")
    return result


def _audit_event_title(event: str) -> str:
    return event.replace("-", " ").capitalize()


def _audit_payload_label(field: str) -> str:
    return field.replace("_", " ").capitalize()


def _audit_text_fence(content: bytes) -> bytes:
    longest = max((len(match.group(0)) for match in re.finditer(b"`+", content)), default=0)
    return b"`" * max(3, longest + 1)


def _render_audit_text_value(value: object) -> bytes:
    if not isinstance(value, str):
        raise ValueError("audit Markdown text value must be a string")
    try:
        raw = value.encode("utf-8")
    except UnicodeEncodeError as error:
        raise ValueError("audit Markdown text value must be UTF-8 encodable") from error
    fence = _audit_text_fence(raw)
    return (
        b"- Value encoding: UTF-8\n"
        + f"- UTF-8 bytes: `{len(raw)}`\n\n".encode("ascii")
        + fence
        + b"stepan-text\n"
        + raw
        + b"\n"
        + fence
        + b"\n\n"
    )


def _parse_non_negative_inline_integer(
    parser: _AuditMarkdownParser, prefix: bytes, name: str
) -> int:
    text = _parse_inline_code_value(parser.line(name), prefix, name)
    if not re.fullmatch(r"0|[1-9][0-9]*", text):
        raise ValueError(f"invalid audit Markdown {name}")
    return int(text)


def _parse_audit_text_value(parser: _AuditMarkdownParser, name: str) -> str:
    parser.expect(b"- Value encoding: UTF-8\n", f"{name} encoding")
    length = _parse_non_negative_inline_integer(
        parser, b"- UTF-8 bytes: ", f"{name} byte length"
    )
    parser.expect(b"\n", f"{name} preamble")
    opening = parser.line(f"{name} opening fence")
    match = re.fullmatch(rb"(`{3,})stepan-text", opening)
    if match is None:
        raise ValueError(f"invalid audit Markdown {name} opening fence")
    fence = match.group(1)
    end = parser.position + length
    if end > len(parser.content):
        raise ValueError(f"truncated audit Markdown {name}")
    raw = parser.content[parser.position : end]
    parser.position = end
    parser.expect(b"\n" + fence + b"\n\n", f"{name} closing fence")
    try:
        value = raw.decode("utf-8")
    except UnicodeDecodeError as error:
        raise ValueError(f"invalid UTF-8 in audit Markdown {name}") from error
    if _render_audit_text_value(value) != (
        b"- Value encoding: UTF-8\n"
        + f"- UTF-8 bytes: `{length}`\n\n".encode("ascii")
        + opening
        + b"\n"
        + raw
        + b"\n"
        + fence
        + b"\n\n"
    ):
        raise ValueError(f"non-canonical audit Markdown {name}")
    return value


def _render_audit_payload_value(rule: object, value: object) -> bytes:
    if rule in {"positive_int", "non_negative_int"}:
        return f"- Value: `{value}`\n\n".encode("ascii")
    if rule in {"nullable_text", "nullable_hash", "nullable_stage"} and value is None:
        return b"- Value: `null`\n\n"
    if rule in {"text_list", "project_path_list", "references"}:
        items = value
        result = f"- Items: `{len(items)}`\n\n".encode("ascii")
        for index, item in enumerate(items, start=1):
            result += f"##### Item {index}\n\n".encode("ascii")
            result += _render_audit_text_value(item)
        return result
    if rule == "alternatives":
        alternatives = value
        result = f"- Items: `{len(alternatives)}`\n\n".encode("ascii")
        for index, alternative in enumerate(alternatives, start=1):
            result += f"##### Alternative {index}\n\n".encode("ascii")
            result += b"###### Option\n\n" + _render_audit_text_value(alternative["option"])
            result += b"###### Rejected because\n\n" + _render_audit_text_value(
                alternative["rejected_because"]
            )
        return result
    return _render_audit_text_value(value)


def _parse_audit_payload_value(
    parser: _AuditMarkdownParser, rule: object, name: str
) -> object:
    if rule in {"positive_int", "non_negative_int"}:
        value = _parse_non_negative_inline_integer(parser, b"- Value: ", name)
        parser.expect(b"\n", f"{name} separator")
        return value
    if rule in {"nullable_text", "nullable_hash", "nullable_stage"} and parser.content.startswith(
        b"- Value: `null`\n\n", parser.position
    ):
        parser.position += len(b"- Value: `null`\n\n")
        return None
    if rule in {"text_list", "project_path_list", "references", "alternatives"}:
        count = _parse_non_negative_inline_integer(parser, b"- Items: ", f"{name} count")
        parser.expect(b"\n", f"{name} separator")
        values: list[object] = []
        for index in range(1, count + 1):
            if rule == "alternatives":
                parser.expect(
                    f"##### Alternative {index}\n\n".encode("ascii"),
                    f"{name} alternative heading",
                )
                parser.expect(b"###### Option\n\n", f"{name} option heading")
                option = _parse_audit_text_value(parser, f"{name} option")
                parser.expect(
                    b"###### Rejected because\n\n",
                    f"{name} rejection heading",
                )
                rejected = _parse_audit_text_value(parser, f"{name} rejection")
                values.append({"option": option, "rejected_because": rejected})
            else:
                parser.expect(
                    f"##### Item {index}\n\n".encode("ascii"),
                    f"{name} item heading",
                )
                values.append(_parse_audit_text_value(parser, f"{name} item"))
        return values
    return _parse_audit_text_value(parser, name)


def _render_audit_event_unchecked(
    event: dict[str, object], *, event_hash: str | None = None
) -> bytes:
    hash_value = event_hash if event_hash is not None else str(event["event_sha256"])
    run_id = event.get("run_id")
    related = event["related_events"]
    related_text = (
        "none" if not related else ", ".join(f"`{item}`" for item in related)
    )
    previous = event["previous_event_sha256"]
    previous_text = "none" if previous is None else f"`{previous}`"
    run_text = "none" if run_id is None else f"`{run_id}`"
    result = (
        f"## {event['event_id']} — {_audit_event_title(str(event['event']))}\n\n"
        f"- Recorded at: `{event['recorded_at']}`\n"
        f"- Stage: `{event['stage']}`\n"
        f"- Kind: `{event['kind']}`\n"
        f"- Event: `{event['event']}`\n"
        f"- Actor: `{event['actor']}`\n"
        f"- Event key: `{event['event_key']}`\n"
        f"- Run: {run_text}\n"
        f"- Related events: {related_text}\n"
        f"- Previous event SHA-256: {previous_text}\n\n"
        "### Payload\n\n"
    ).encode("utf-8")
    schema = AUDIT_EVENT_PAYLOAD_SCHEMAS[(str(event["kind"]), str(event["event"]))]
    payload = event["payload"]
    for field, rule in schema.items():
        result += f"#### {_audit_payload_label(field)} (`{field}`)\n\n".encode("utf-8")
        result += _render_audit_payload_value(rule, payload[field])
    result += (
        "### Integrity\n\n" f"- Event SHA-256: `{hash_value}`\n\n"
    ).encode("ascii")
    return result


def compute_audit_event_sha256(value: object) -> str:
    event = validate_durable_audit_event(value)
    canonical = _render_audit_event_unchecked(
        event, event_hash=AUDIT_EVENT_HASH_SENTINEL
    )
    return audit_bytes_sha256(canonical)


def seal_audit_event(value: object) -> dict[str, object]:
    if not isinstance(value, dict):
        raise ValueError("unsealed durable audit event must be an object")
    event = dict(value)
    event.setdefault("event_sha256", AUDIT_EVENT_HASH_SENTINEL)
    validate_durable_audit_event(event)
    event["event_sha256"] = compute_audit_event_sha256(event)
    return event


def render_audit_event(value: object) -> bytes:
    event = validate_durable_audit_event(value)
    expected = compute_audit_event_sha256(event)
    if event["event_sha256"] != expected:
        raise ValueError("audit event hash does not match its canonical Markdown")
    return _render_audit_event_unchecked(event)


def _parse_optional_inline_value(line: bytes, prefix: bytes, name: str) -> str | None:
    if line == prefix + b"none":
        return None
    return _parse_inline_code_value(line, prefix, name)


def _parse_related_events(line: bytes) -> list[object]:
    prefix = b"- Related events: "
    if line == prefix + b"none":
        return []
    if not line.startswith(prefix):
        raise ValueError("invalid audit Markdown related events")
    remainder = line[len(prefix) :]
    if not remainder:
        raise ValueError("invalid audit Markdown related events")
    result: list[object] = []
    for item in remainder.split(b", "):
        if len(item) < 3 or not item.startswith(b"`") or not item.endswith(b"`"):
            raise ValueError("invalid audit Markdown related events")
        try:
            result.append(item[1:-1].decode("ascii"))
        except UnicodeDecodeError as error:
            raise ValueError("invalid audit Markdown related events") from error
    return result


def _parse_audit_event_at(
    parser: _AuditMarkdownParser,
) -> dict[str, object]:
    start = parser.position
    heading = parser.line("event heading")
    try:
        heading_text = heading.decode("utf-8")
    except UnicodeDecodeError as error:
        raise ValueError("invalid audit Markdown event heading") from error
    heading_match = re.fullmatch(r"## (MEM-[0-9]{6,}) — (.+)", heading_text)
    if heading_match is None:
        raise ValueError("invalid audit Markdown event heading")
    event_id, title = heading_match.groups()
    parser.expect(b"\n", "event heading separator")
    recorded_at = _parse_inline_code_value(
        parser.line("recorded at"), b"- Recorded at: ", "recorded at"
    )
    stage = _parse_inline_code_value(parser.line("stage"), b"- Stage: ", "stage")
    kind = _parse_inline_code_value(parser.line("kind"), b"- Kind: ", "kind")
    event_name = _parse_inline_code_value(
        parser.line("event name"), b"- Event: ", "event name"
    )
    actor = _parse_inline_code_value(parser.line("actor"), b"- Actor: ", "actor")
    event_key = _parse_inline_code_value(
        parser.line("event key"), b"- Event key: ", "event key"
    )
    run_id = _parse_optional_inline_value(
        parser.line("run"), b"- Run: ", "run"
    )
    related_events = _parse_related_events(parser.line("related events"))
    previous_hash = _parse_optional_inline_value(
        parser.line("previous event hash"),
        b"- Previous event SHA-256: ",
        "previous event hash",
    )
    parser.expect(b"\n### Payload\n\n", "payload heading")
    schema = AUDIT_EVENT_PAYLOAD_SCHEMAS.get((kind, event_name))
    if schema is None:
        raise ValueError("unknown audit Markdown event schema")
    payload: dict[str, object] = {}
    for field, rule in schema.items():
        parser.expect(
            f"#### {_audit_payload_label(field)} (`{field}`)\n\n".encode("utf-8"),
            f"payload field {field}",
        )
        payload[field] = _parse_audit_payload_value(parser, rule, field)
    parser.expect(b"### Integrity\n\n", "integrity heading")
    event_hash = _parse_inline_code_value(
        parser.line("event hash"), b"- Event SHA-256: ", "event hash"
    )
    parser.expect(b"\n", "event separator")
    event: dict[str, object] = {
        "event_id": event_id,
        "event_key": event_key,
        "recorded_at": recorded_at,
        "kind": kind,
        "event": event_name,
        "stage": stage,
        "actor": actor,
        "related_events": related_events,
        "previous_event_sha256": previous_hash,
        "event_sha256": event_hash,
        "payload": payload,
    }
    if run_id is not None:
        event["run_id"] = run_id
    validate_durable_audit_event(event)
    if title != _audit_event_title(event_name):
        raise ValueError("audit Markdown event title does not match its event")
    if event_hash != compute_audit_event_sha256(event):
        raise ValueError("audit event hash does not match its canonical Markdown")
    if parser.content[start : parser.position] != _render_audit_event_unchecked(event):
        raise ValueError("non-canonical audit Markdown event")
    return event


def parse_audit_event(content: object) -> dict[str, object]:
    raw = _validate_audit_markdown_bytes(content, "audit event")
    parser = _AuditMarkdownParser(raw)
    event = _parse_audit_event_at(parser)
    if parser.position != len(raw):
        raise ValueError("audit event contains trailing content")
    return event


_AUDIT_EXPECTATION_UNSET = object()


def validate_audit_log(
    content: object,
    *,
    expected_log_size_bytes: object = _AUDIT_EXPECTATION_UNSET,
    expected_log_sha256: object = _AUDIT_EXPECTATION_UNSET,
    expected_head_event_id: object = _AUDIT_EXPECTATION_UNSET,
    expected_head_event_sha256: object = _AUDIT_EXPECTATION_UNSET,
    expected_next_event_sequence: object = _AUDIT_EXPECTATION_UNSET,
) -> dict[str, object]:
    raw = _validate_audit_markdown_bytes(content, "audit log")
    parser = _AuditMarkdownParser(raw)
    header = _parse_audit_header_prefix(parser)
    events: list[object] = []
    event_keys: set[str] = set()
    previous_hash: str | None = None
    sequence = 1
    while parser.position < len(raw):
        event = _parse_audit_event_at(parser)
        if parse_audit_event_id(event["event_id"]) != sequence:
            raise ValueError("audit log event IDs are not consecutive")
        if sequence == 1 and (
            event["kind"], event["event"]
        ) != ("interaction", "initial-request-captured"):
            raise ValueError("audit log first event must capture the initial request")
        if event["previous_event_sha256"] != previous_hash:
            raise ValueError("audit log previous-event hash chain is invalid")
        key = str(event["event_key"])
        if key in event_keys:
            raise ValueError("audit log contains a duplicate event key")
        event_keys.add(key)
        events.append(event)
        previous_hash = str(event["event_sha256"])
        sequence += 1
    log_hash = audit_bytes_sha256(raw)
    head = events[-1] if events else None
    head_id = head["event_id"] if head is not None else None
    head_hash = head["event_sha256"] if head is not None else None
    if expected_log_size_bytes is not _AUDIT_EXPECTATION_UNSET and (
        type(expected_log_size_bytes) is not int or expected_log_size_bytes != len(raw)
    ):
        raise ValueError("audit log byte length does not match state")
    if expected_log_sha256 is not _AUDIT_EXPECTATION_UNSET:
        validate_audit_hash(expected_log_sha256, "expected audit log hash")
        if expected_log_sha256 != log_hash:
            raise ValueError("audit log hash does not match state")
    if (
        expected_head_event_id is not _AUDIT_EXPECTATION_UNSET
        and expected_head_event_id != head_id
    ):
        raise ValueError("audit log head event ID does not match state")
    if (
        expected_head_event_sha256 is not _AUDIT_EXPECTATION_UNSET
        and expected_head_event_sha256 != head_hash
    ):
        raise ValueError("audit log head event hash does not match state")
    if expected_next_event_sequence is not _AUDIT_EXPECTATION_UNSET and (
        type(expected_next_event_sequence) is not int
        or expected_next_event_sequence != sequence
    ):
        raise ValueError("audit log next event sequence does not match state")
    return {
        "header": header,
        "events": events,
        "log_size_bytes": len(raw),
        "log_sha256": log_hash,
        "head_event_id": head_id,
        "head_event_sha256": head_hash,
        "next_event_sequence": sequence,
    }


def audit_decision_index(events: object) -> dict[str, object]:
    if not isinstance(events, list):
        raise ValueError("audit events must be a list")
    index: dict[str, dict[str, object]] = {}
    for event_value in events:
        event = validate_durable_audit_event(event_value)
        if event["kind"] != "decision":
            continue
        role = str(event["actor"])
        payload = event["payload"]
        key = str(payload["key"])
        role_index = index.setdefault(role, {})
        current = role_index.get(key)
        if event["event"] == "agent-decision-introduced":
            if current is not None:
                raise ValueError("audit log introduces an active decision twice")
            role_index[key] = {
                "event_id": event["event_id"],
                "semantic_sha256": payload["semantic_sha256"],
            }
        elif event["event"] == "agent-decision-revised":
            if current is None or payload["supersedes_event_id"] != current["event_id"]:
                raise ValueError("audit decision revision does not supersede its active event")
            role_index[key] = {
                "event_id": event["event_id"],
                "semantic_sha256": payload["semantic_sha256"],
            }
        else:
            if current is None or payload["retired_event_id"] != current["event_id"]:
                raise ValueError("audit decision retirement does not name its active event")
            del role_index[key]
            if not role_index:
                del index[role]
    validate_decision_index(index)
    return index


def render_audit_log(header: object, events: object) -> bytes:
    header_value = require_object(
        header,
        "audit header",
        {"schema_version", "specification", "request_path", "request_sha256"},
    )
    if header_value["schema_version"] != AUDIT_SCHEMA_VERSION:
        raise ValueError("unsupported audit log schema version")
    if not isinstance(events, list):
        raise ValueError("audit events must be a list")
    content = render_audit_header(
        header_value["specification"],
        header_value["request_path"],
        header_value["request_sha256"],
    ) + b"".join(render_audit_event(event) for event in events)
    validate_audit_log(content)
    return content


def validate_audit_log_extension(previous: object, candidate: object) -> dict[str, object]:
    previous_raw = _validate_audit_markdown_bytes(previous, "previous audit log")
    candidate_raw = _validate_audit_markdown_bytes(candidate, "candidate audit log")
    validate_audit_log(previous_raw)
    if len(candidate_raw) < len(previous_raw) or not candidate_raw.startswith(previous_raw):
        raise ValueError("candidate audit log does not preserve the accepted byte prefix")
    return validate_audit_log(candidate_raw)


def materialize_audit_outbox(
    accepted_log: dict[str, object],
    outbox: dict[str, object],
    recorded_at: str | list[str],
) -> list[dict[str, object]]:
    next_sequence = int(accepted_log["next_event_sequence"])
    validate_audit_outbox(
        outbox,
        head_event_id=accepted_log["head_event_id"],
        head_event_sha256=accepted_log["head_event_sha256"],
        log_sha256=accepted_log["log_sha256"],
        next_event_sequence=next_sequence,
    )
    events = outbox["events"]
    if isinstance(recorded_at, str):
        timestamps = [recorded_at] * len(events)
    elif isinstance(recorded_at, list) and len(recorded_at) == len(events):
        timestamps = list(recorded_at)
    else:
        raise ValueError("audit flush timestamps do not match the queued batch")
    for timestamp in timestamps:
        validate_audit_recorded_at(timestamp)

    accepted_keys = {
        str(event["event_key"]) for event in accepted_log["events"]
    }
    previous_hash = accepted_log["head_event_sha256"]
    batch_ids: dict[str, str] = {}
    durable_events: list[dict[str, object]] = []
    for offset, queued_value in enumerate(events):
        queued = validate_queued_audit_event(queued_value)
        key = str(queued["event_key"])
        if key in accepted_keys:
            raise ValueError("audit outbox event key conflicts with the accepted log")
        event_id = f"MEM-{next_sequence + offset:06d}"
        related = list(queued["related_events"])
        related.extend(batch_ids[str(item)] for item in queued["related_event_keys"])
        durable: dict[str, object] = {
            "event_id": event_id,
            "event_key": key,
            "recorded_at": timestamps[offset],
            "kind": queued["kind"],
            "event": queued["event"],
            "stage": queued["stage"],
            "actor": queued["actor"],
            "related_events": related,
            "previous_event_sha256": previous_hash,
            "payload": copy.deepcopy(queued["payload"]),
        }
        if "run_id" in queued:
            durable["run_id"] = queued["run_id"]
        durable = seal_audit_event(durable)
        durable_events.append(durable)
        batch_ids[key] = event_id
        previous_hash = durable["event_sha256"]
    return durable_events


def _yaml_scalar(value: object) -> str | None:
    if value is None:
        return "null"
    if value is True:
        return "true"
    if value is False:
        return "false"
    if type(value) is int:
        if value < 0:
            raise ValueError("generated YAML does not support negative integers")
        return str(value)
    if isinstance(value, str):
        return json.dumps(value, ensure_ascii=False)
    if value == []:
        return "[]"
    if value == {}:
        return "{}"
    return None


def _render_yaml_mapping_entries(
    entries: list[tuple[object, object]], indentation: int
) -> list[str]:
    lines: list[str] = []
    for key_value, value in entries:
        if not isinstance(key_value, str) or not key_value:
            raise ValueError("generated YAML contains an invalid mapping key")
        key = (
            key_value
            if re.fullmatch(r"[a-zA-Z][a-zA-Z0-9_.-]*", key_value)
            else json.dumps(key_value, ensure_ascii=False)
        )
        scalar = _yaml_scalar(value)
        prefix = " " * indentation + f"{key}:"
        if scalar is not None:
            lines.append(f"{prefix} {scalar}")
        else:
            lines.append(prefix)
            lines.extend(_render_yaml_node(value, indentation + 2))
    return lines


def _render_yaml_node(value: object, indentation: int) -> list[str]:
    if isinstance(value, dict) and value:
        return _render_yaml_mapping_entries(list(value.items()), indentation)
    if isinstance(value, list) and value:
        lines: list[str] = []
        for item in value:
            scalar = _yaml_scalar(item)
            if scalar is not None:
                lines.append(" " * indentation + f"- {scalar}")
                continue
            if not isinstance(item, dict) or not item:
                raise ValueError("generated YAML sequence item is unsupported")
            entries = list(item.items())
            first_scalar = next(
                (index for index, (_, child) in enumerate(entries) if _yaml_scalar(child) is not None),
                None,
            )
            if first_scalar is None:
                raise ValueError("generated YAML mapping sequence needs a scalar field")
            entries.insert(0, entries.pop(first_scalar))
            first_key, first_value = entries[0]
            if not isinstance(first_key, str) or not first_key:
                raise ValueError("generated YAML contains an invalid mapping key")
            rendered_first_key = (
                first_key
                if re.fullmatch(r"[a-zA-Z][a-zA-Z0-9_.-]*", first_key)
                else json.dumps(first_key, ensure_ascii=False)
            )
            lines.append(
                " " * indentation
                + f"- {rendered_first_key}: {_yaml_scalar(first_value)}"
            )
            lines.extend(_render_yaml_mapping_entries(entries[1:], indentation + 2))
        return lines
    raise ValueError("generated YAML contains an unsupported value")


def render_canonical_yaml_mapping(value: object) -> bytes:
    if not isinstance(value, dict) or not value:
        raise ValueError("generated YAML root must be a non-empty mapping")
    content = ("\n".join(_render_yaml_node(value, 0)) + "\n").encode("utf-8")
    parsed = parse_generated_mapping_yaml(content.decode("utf-8"))
    if parsed != value:
        raise ValueError("generated YAML does not round-trip")
    return content


def _canonical_initial_request(value: object) -> bytes:
    request = require_bounded_text(value, "initial request")
    try:
        content = canonicalize(request.encode("utf-8"))
    except UnicodeError as error:
        raise ValueError("initial request must be UTF-8") from error
    if len(content) > ARTIFACT_MAX_BYTES:
        raise ValueError(f"initial request exceeds {ARTIFACT_MAX_BYTES} bytes")
    return content


def _initial_feature_files(
    specification: str,
    initial_request: object,
    execution_value: object,
    recorded_at: str,
) -> tuple[tuple[tuple[PurePosixPath, bytes], ...], dict[str, object]]:
    if len(specification) > 63 or not SPEC_ID_PATTERN.fullmatch(specification):
        raise ValueError("invalid specification ID")
    validate_audit_recorded_at(recorded_at)
    execution = validate_execution_snapshot(execution_value)
    request_bytes = _canonical_initial_request(initial_request)
    request_text = request_bytes.decode("utf-8")
    specification_root = f"docs/changes/specs/{specification}"
    request_relative = f"{specification_root}/request.md"
    log_relative = f"{specification_root}/mem-log.md"
    state_relative = f"{specification_root}/state.yaml"
    request_hash = audit_bytes_sha256(request_bytes)
    header = {
        "schema_version": AUDIT_SCHEMA_VERSION,
        "specification": specification,
        "request_path": request_relative,
        "request_sha256": request_hash,
    }

    initial_request_event = seal_audit_event(
        {
            "event_id": "MEM-000001",
            "event_key": f"interaction/{specification}/initial-request",
            "recorded_at": recorded_at,
            "kind": "interaction",
            "event": "initial-request-captured",
            "stage": "idea",
            "actor": "user",
            "related_events": [],
            "previous_event_sha256": None,
            "payload": {
                "request_path": request_relative,
                "request_sha256": request_hash,
                "verbatim": request_text,
            },
        }
    )
    specification_created_event = seal_audit_event(
        {
            "event_id": "MEM-000002",
            "event_key": f"spec/{specification}/created",
            "recorded_at": recorded_at,
            "kind": "lifecycle",
            "event": "specification-created",
            "stage": "workflow",
            "actor": "router",
            "related_events": ["MEM-000001"],
            "previous_event_sha256": initial_request_event["event_sha256"],
            "payload": {
                "specification": specification,
                "request_path": request_relative,
                "request_sha256": request_hash,
                "log_path": log_relative,
            },
        }
    )
    stage_entered_event = seal_audit_event(
        {
            "event_id": "MEM-000003",
            "event_key": f"spec/{specification}/stage/idea/entered",
            "recorded_at": recorded_at,
            "kind": "lifecycle",
            "event": "stage-entered",
            "stage": "idea",
            "actor": "router",
            "related_events": ["MEM-000002"],
            "previous_event_sha256": specification_created_event["event_sha256"],
            "payload": {"from_stage": None, "reason": "initial"},
        }
    )
    events = [
        initial_request_event,
        specification_created_event,
        stage_entered_event,
    ]
    if tuple(audit_event_type(event) for event in events) != FEATURE_TRANSITION_TABLE[
        "initialization"
    ]["events"]:
        raise AssertionError("initialization batch diverges from the transition table")
    log_bytes = render_audit_log(header, events)
    parsed_log = validate_audit_log(log_bytes)
    audit = {
        "schema_version": AUDIT_SCHEMA_VERSION,
        "log_path": log_relative,
        "log_size_bytes": parsed_log["log_size_bytes"],
        "log_sha256": parsed_log["log_sha256"],
        "head_event_id": parsed_log["head_event_id"],
        "head_event_sha256": parsed_log["head_event_sha256"],
        "next_event_sequence": parsed_log["next_event_sequence"],
        "decision_index": {},
        "outbox": None,
    }
    state = {
        "schema_version": 1,
        "specification": specification,
        "created_at": recorded_at,
        "initial_request_sha256": request_hash,
        "stage": "idea",
        "status": "drafting",
        "automatic_revision_attempts": 0,
        "next_run_sequence": 1,
        "approvals": {},
        "clarifications": [],
        "pending": None,
        "active_run": None,
        "checkpoint_commit": None,
        "audit": audit,
        "execution": execution,
    }
    validate_state_value(state, specification)
    state_bytes = render_canonical_yaml_mapping(state)
    files = (
        (PurePosixPath(request_relative), request_bytes),
        (PurePosixPath(log_relative), log_bytes),
        (PurePosixPath(state_relative), state_bytes),
    )
    return files, state


def _current_audit_timestamp() -> str:
    return dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def initialize_feature_specification(
    project_root: Path,
    specification: str,
    initial_request: object,
    execution: object,
    *,
    recorded_at: str | None = None,
    _after_create: Callable[[str], None] | None = None,
) -> dict[str, object]:
    project_root = canonical_project_root(project_root)
    timestamp = recorded_at if recorded_at is not None else _current_audit_timestamp()
    files, expected_state = _initial_feature_files(
        specification, initial_request, execution, timestamp
    )
    specification_relative = PurePosixPath("docs/changes/specs") / specification
    specification_root = project_root.joinpath(*specification_relative.parts)
    current = project_root
    for part in specification_relative.parts:
        current = current / part
        if current.is_symlink():
            raise ValueError("feature specification path contains a symlink")
        if current.exists() and current != specification_root and not current.is_dir():
            raise ValueError("feature specification parent is not a directory")
    if specification_root.exists() or specification_root.is_symlink():
        raise ValueError(f"feature specification already exists: {specification}")

    created_files: list[tuple[Path, tuple[int, int]]] = []
    created_directories: list[tuple[Path, tuple[int, int]]] = []
    try:
        _ensure_init_parent(project_root, specification_root, created_directories)
        try:
            specification_root.mkdir()
        except FileExistsError as error:
            raise ValueError(
                f"feature specification appeared during initialization: {specification}"
            ) from error
        specification_stat = specification_root.stat()
        created_directories.append(
            (
                specification_root,
                (specification_stat.st_dev, specification_stat.st_ino),
            )
        )
        for relative, content in files:
            target = _validate_init_target(project_root, relative)
            _ensure_init_parent(project_root, target, created_directories)
            descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
            stat = os.fstat(descriptor)
            created_files.append((target, (stat.st_dev, stat.st_ino)))
            with os.fdopen(descriptor, "wb") as stream:
                stream.write(content)
                stream.flush()
                os.fsync(stream.fileno())
            if target.is_symlink() or target.read_bytes() != content:
                raise ValueError(f"could not verify created file: {relative}")
            if _after_create is not None:
                _after_create(relative.as_posix())
        state_relative = f"docs/changes/specs/{specification}/state.yaml"
        state_path = project_root.joinpath(*PurePosixPath(state_relative).parts)
        _, validated_state = validate_state_path(state_path, project_root, specification)
        if validated_state != expected_state:
            raise ValueError("initialized state differs from the prepared state")
    except (OSError, UnicodeError, ValueError):
        _rollback_init(created_files, created_directories)
        raise

    return {
        "schema_version": 1,
        "specification": specification,
        "created": [relative.as_posix() for relative, _ in files],
        "initial_request_sha256": expected_state["initial_request_sha256"],
        "audit": expected_state["audit"],
    }


def _file_identity(value: os.stat_result) -> tuple[int, int, int, int]:
    return (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns)


def _assert_file_unchanged(
    path: Path,
    expected_bytes: bytes,
    expected_stat: os.stat_result,
    operation: str,
) -> None:
    try:
        current_stat = path.stat()
        current_bytes = path.read_bytes()
    except OSError as error:
        raise ValueError(f"file changed while {operation}: {path}") from error
    if (
        path.is_symlink()
        or _file_identity(current_stat) != _file_identity(expected_stat)
        or current_bytes != expected_bytes
    ):
        raise ValueError(f"file changed while {operation}: {path}")


def _fsync_parent_directory(path: Path) -> None:
    if os.name == "nt":
        return
    descriptor = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def _atomic_replace_bytes(
    path: Path,
    content: bytes,
    *,
    expected_bytes: bytes,
    expected_stat: os.stat_result,
    operation: str,
    _before_replace: Callable[[], None] | None = None,
) -> None:
    descriptor, temporary_name = mkstemp(
        dir=path.parent, prefix=f".{path.name}.", suffix=".tmp"
    )
    temporary_path = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        if temporary_path.read_bytes() != content:
            raise ValueError(f"could not verify temporary file while {operation}")
        if _before_replace is not None:
            _before_replace()
        _assert_file_unchanged(path, expected_bytes, expected_stat, operation)
        os.chmod(temporary_path, expected_stat.st_mode)
        os.replace(temporary_path, path)
        _fsync_parent_directory(path)
    finally:
        if temporary_path.exists():
            temporary_path.unlink()


def flush_audit_outbox(
    state_path: Path,
    project_root: Path,
    specification: str,
    *,
    recorded_at: str | None = None,
    _before_log_replace: Callable[[], None] | None = None,
    _after_log_replace: Callable[[], None] | None = None,
    _before_state_replace: Callable[[], None] | None = None,
    _after_state_replace: Callable[[], None] | None = None,
) -> dict[str, object]:
    content, state = validate_state_path(
        state_path,
        project_root,
        specification,
        _allow_pending_audit_suffix=True,
    )
    state_stat = state_path.stat()
    state_bytes = state_path.read_bytes()
    if state_bytes != content.encode("utf-8"):
        raise ValueError("state changed while preparing audit flush")
    audit = state["audit"]
    outbox = audit["outbox"]
    if outbox is None:
        validate_state_path(state_path, project_root, specification)
        return {
            "schema_version": 1,
            "status": "no-op",
            "event_ids": [],
            "audit": audit,
        }

    project_root = canonical_project_root(project_root)
    log_path = resolve_project_file(
        project_root,
        audit["log_path"],
        "feature audit log",
        AUDIT_LOG_MAX_BYTES,
    )
    log_stat = log_path.stat()
    log_bytes = log_path.read_bytes()
    accepted_size = int(audit["log_size_bytes"])
    if len(log_bytes) < accepted_size:
        raise ValueError("audit log is shorter than the outbox expected prefix")
    accepted_bytes = log_bytes[:accepted_size]
    accepted_log = validate_audit_log(
        accepted_bytes,
        expected_log_size_bytes=accepted_size,
        expected_log_sha256=audit["log_sha256"],
        expected_head_event_id=audit["head_event_id"],
        expected_head_event_sha256=audit["head_event_sha256"],
        expected_next_event_sequence=audit["next_event_sequence"],
    )
    validate_audit_outbox(
        outbox,
        head_event_id=accepted_log["head_event_id"],
        head_event_sha256=accepted_log["head_event_sha256"],
        log_sha256=accepted_log["log_sha256"],
        next_event_sequence=accepted_log["next_event_sequence"],
    )

    recovering = len(log_bytes) > accepted_size
    observed_log: dict[str, object] | None = None
    if recovering:
        observed_log = validate_audit_log_extension(accepted_bytes, log_bytes)
        accepted_count = int(accepted_log["next_event_sequence"]) - 1
        observed_events = observed_log["events"][accepted_count:]
        if len(observed_events) != len(outbox["events"]):
            raise ValueError("audit recovery suffix does not match the queued batch length")
        timestamps: str | list[str] = [
            str(event["recorded_at"]) for event in observed_events
        ]
        if len(set(timestamps)) != 1:
            raise ValueError(
                "audit recovery suffix does not use the writer's single batch timestamp"
            )
    else:
        timestamp = recorded_at if recorded_at is not None else _current_audit_timestamp()
        validate_audit_recorded_at(timestamp)
        timestamps = timestamp

    durable_events = materialize_audit_outbox(accepted_log, outbox, timestamps)
    appended_bytes = b"".join(render_audit_event(event) for event in durable_events)
    candidate_bytes = accepted_bytes + appended_bytes
    if len(candidate_bytes) > AUDIT_LOG_MAX_BYTES:
        raise ValueError("audit flush would exceed the audit log size limit")
    candidate_log = validate_audit_log_extension(accepted_bytes, candidate_bytes)
    if audit_decision_index(candidate_log["events"]) != outbox["decision_index_after"]:
        raise ValueError("audit outbox decision_index_after does not match its events")
    if recovering:
        if log_bytes != candidate_bytes or observed_log != candidate_log:
            raise ValueError("audit recovery suffix conflicts with the queued transaction")
        flush_status = "recovered"
    else:
        _atomic_replace_bytes(
            log_path,
            candidate_bytes,
            expected_bytes=log_bytes,
            expected_stat=log_stat,
            operation="flushing the audit log",
            _before_replace=_before_log_replace,
        )
        flush_status = "flushed"
        if _after_log_replace is not None:
            _after_log_replace()

    durable_log_stat = log_path.stat()
    _assert_file_unchanged(
        log_path, candidate_bytes, durable_log_stat, "preparing audit state metadata"
    )
    updated_state = copy.deepcopy(state)
    updated_audit = updated_state["audit"]
    updated_audit.update(
        {
            "log_size_bytes": candidate_log["log_size_bytes"],
            "log_sha256": candidate_log["log_sha256"],
            "head_event_id": candidate_log["head_event_id"],
            "head_event_sha256": candidate_log["head_event_sha256"],
            "next_event_sequence": candidate_log["next_event_sequence"],
            "decision_index": copy.deepcopy(outbox["decision_index_after"]),
            "outbox": None,
        }
    )
    validate_state_value(updated_state, specification)
    updated_state_bytes = render_canonical_yaml_mapping(updated_state)

    def before_state_replace() -> None:
        if _before_state_replace is not None:
            _before_state_replace()
        _assert_file_unchanged(
            log_path,
            candidate_bytes,
            durable_log_stat,
            "updating audit state metadata",
        )

    _atomic_replace_bytes(
        state_path,
        updated_state_bytes,
        expected_bytes=state_bytes,
        expected_stat=state_stat,
        operation="updating audit state metadata",
        _before_replace=before_state_replace,
    )
    if _after_state_replace is not None:
        _after_state_replace()
    _, validated_state = validate_state_path(state_path, project_root, specification)
    if validated_state != updated_state or log_path.read_bytes() != candidate_bytes:
        raise ValueError("audit flush result changed before final validation")
    return {
        "schema_version": 1,
        "status": flush_status,
        "transaction_id": outbox["transaction_id"],
        "event_ids": [event["event_id"] for event in durable_events],
        "audit": validated_state["audit"],
    }


def validate_feature_initialization_input(value: object) -> dict[str, object]:
    initialization = require_object(
        value, "feature initialization", {"initial_request", "execution"}
    )
    _canonical_initial_request(initialization["initial_request"])
    validate_execution_snapshot(initialization["execution"])
    return initialization


def validate_executor_metadata(
    value: object,
    requested_model: str | None,
    requested_reasoning: str | None,
    enforce_requested: bool,
) -> None:
    executor = require_object(value, "executor", {"requested", "effective"})
    requested = require_object(
        executor["requested"], "executor.requested", {"model"}, {"reasoning"}
    )
    effective = require_object(
        executor["effective"], "executor.effective", {"model"}, {"reasoning"}
    )

    actual_requested_model = require_non_empty_string(
        requested["model"], "executor.requested.model"
    )
    if enforce_requested and actual_requested_model != requested_model:
        raise ValueError("executor.requested.model does not match configuration")

    actual_requested_reasoning = requested.get("reasoning")
    if not enforce_requested:
        if actual_requested_reasoning is not None:
            require_non_empty_string(
                actual_requested_reasoning, "executor.requested.reasoning"
            )
    elif requested_reasoning is None:
        if actual_requested_reasoning is not None:
            raise ValueError("unexpected executor.requested.reasoning")
    elif (
        require_non_empty_string(
            actual_requested_reasoning, "executor.requested.reasoning"
        )
        != requested_reasoning
    ):
        raise ValueError("executor.requested.reasoning does not match configuration")

    require_non_empty_string(effective["model"], "executor.effective.model")
    if "reasoning" in effective:
        require_non_empty_string(
            effective["reasoning"], "executor.effective.reasoning"
        )


def validate_receipt(
    value: object,
    expected_run_id: str,
    expected_output: str,
    adapter: str,
    requested_model: str | None = None,
    requested_reasoning: str | None = None,
) -> dict[str, object]:
    require_non_empty_string(expected_run_id, "expected run ID")
    expected_run = AUDIT_RUN_ID_PATTERN.fullmatch(expected_run_id)
    if expected_run is None:
        raise ValueError("receipt requires a canonical expected run ID")
    expected_role = expected_run.group("role")
    require_non_empty_string(expected_output, "expected output path")
    if adapter not in {"native", "mailbox"}:
        raise ValueError("invalid receipt adapter")
    if adapter == "native" and (
        requested_model is not None or requested_reasoning is not None
    ):
        raise ValueError("native receipt validation does not accept executor overrides")
    if adapter == "mailbox" and requested_model is None:
        raise ValueError("mailbox receipt validation requires requested model")

    receipt_optional = {"output", "executor", "question", "error", "decisions"}
    receipt = require_object(
        value,
        "receipt",
        {"schema_version", "run_id", "status"},
        receipt_optional,
    )
    if type(receipt["schema_version"]) is not int or receipt["schema_version"] != 1:
        raise ValueError("invalid receipt schema version")
    actual_run_id = require_non_empty_string(receipt["run_id"], "receipt.run_id")
    if actual_run_id != expected_run_id:
        raise ValueError("receipt run ID does not match active run")

    status = require_non_empty_string(receipt["status"], "receipt.status")
    if status == "completed":
        required = {"schema_version", "run_id", "status", "output", "decisions"}
        if adapter == "mailbox":
            required.add("executor")
            optional = set()
        else:
            optional = {"executor"}
        require_object(receipt, "completed receipt", required, optional)
        output = require_object(receipt["output"], "receipt.output", {"path", "sha256"})
        if (
            require_non_empty_string(output["path"], "receipt.output.path")
            != expected_output
        ):
            raise ValueError("receipt output path does not match active run")
        digest = require_non_empty_string(output["sha256"], "receipt.output.sha256")
        if not HASH_PATTERN.fullmatch(digest):
            raise ValueError("invalid receipt output hash")
        validate_decisions_snapshot(receipt["decisions"], expected_role)
        if adapter == "mailbox":
            validate_executor_metadata(
                receipt["executor"], requested_model, requested_reasoning, True
            )
        elif "executor" in receipt:
            validate_executor_metadata(receipt["executor"], None, None, False)
    elif status == "blocked":
        require_object(
            receipt,
            "blocked receipt",
            {"schema_version", "run_id", "status", "question"},
        )
        require_non_empty_string(receipt["question"], "receipt.question")
    elif status == "failed":
        require_object(
            receipt,
            "failed receipt",
            {"schema_version", "run_id", "status", "error"},
        )
        require_non_empty_string(receipt["error"], "receipt.error")
    else:
        raise ValueError("invalid receipt status")
    return receipt


def validate_router_result(value: object) -> dict[str, object]:
    result = require_object(
        value,
        "router result",
        {"schema_version", "message", "continuation"},
    )
    if type(result["schema_version"]) is not int or result["schema_version"] != 1:
        raise ValueError("invalid router result schema version")
    require_non_empty_string(result["message"], "router result message")

    continuation = result["continuation"]
    if continuation is None:
        return result
    continuation = require_object(
        continuation, "router continuation", {"kind"}, {"spec_id"}
    )
    kind = require_non_empty_string(continuation["kind"], "router continuation kind")
    if kind == "state":
        require_object(continuation, "state continuation", {"kind", "spec_id"})
        specification = require_non_empty_string(
            continuation["spec_id"], "router continuation spec_id"
        )
        if len(specification) > 63 or not SPEC_ID_PATTERN.fullmatch(specification):
            raise ValueError("invalid router continuation specification ID")
    elif kind == "pre-state":
        require_object(continuation, "pre-state continuation", {"kind"})
    else:
        raise ValueError("invalid router continuation kind")
    return result


def parse_json_receipt(content: str) -> object:
    if len(content.encode("utf-8")) > CONTROL_FILE_MAX_BYTES:
        raise ValueError(f"JSON control input exceeds {CONTROL_FILE_MAX_BYTES} bytes")
    try:
        return json.loads(content)
    except json.JSONDecodeError as error:
        raise ValueError("invalid JSON receipt") from error


def parse_feature_initialization_json(content: str) -> dict[str, object]:
    if len(content.encode("utf-8")) > ARTIFACT_MAX_BYTES:
        raise ValueError(
            f"feature initialization JSON exceeds {ARTIFACT_MAX_BYTES} bytes"
        )
    try:
        value = json.loads(content)
    except json.JSONDecodeError as error:
        raise ValueError("invalid feature initialization JSON") from error
    return validate_feature_initialization_input(value)


def canonicalize(content: bytes) -> bytes:
    text = content.decode("utf-8")
    if text.startswith("\ufeff"):
        text = text[1:]
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    return (text.rstrip("\n") + "\n").encode("utf-8")


def file_hash(path: Path) -> str:
    return f"sha256:{hashlib.sha256(canonicalize(path.read_bytes())).hexdigest()}"


def markdown_sections(content: str) -> dict[str, str]:
    normalized = content.replace("\r\n", "\n").replace("\r", "\n")
    headings = list(re.finditer(r"^## ([^\n]+)\n", normalized, flags=re.MULTILINE))
    sections: dict[str, str] = {}
    for index, match in enumerate(headings):
        name = match.group(1).strip()
        if name in sections:
            raise ValueError(f"duplicate Markdown section: {name}")
        end = headings[index + 1].start() if index + 1 < len(headings) else len(normalized)
        body = normalized[match.end() : end].strip()
        if not body:
            raise ValueError(f"empty Markdown section: {name}")
        sections[name] = body
    return sections


def requirement_traceability(content: str) -> set[str]:
    if not re.match(r"^# .+ Specification Delta\n", content):
        raise ValueError("requirements artifact has an invalid title")
    sections = markdown_sections(content)
    for required in {"Purpose", "Boundaries and assumptions"}:
        if required not in sections:
            raise ValueError(f"requirements artifact is missing section: {required}")
    delta_names = {
        "ADDED Requirements": "ADDED",
        "MODIFIED Requirements": "MODIFIED",
        "REMOVED Requirements": "REMOVED",
        "RENAMED Requirements": "RENAMED",
    }
    used_sections = [name for name in delta_names if name in sections]
    if not used_sections:
        raise ValueError("requirements artifact must contain a delta section")
    traces: set[str] = set()
    names: set[str] = set()
    for section_name in used_sections:
        operation = delta_names[section_name]
        body = sections[section_name]
        if operation in {"ADDED", "MODIFIED", "REMOVED"}:
            matches = list(
                re.finditer(
                    r"^### Requirement: (.+)\n", body, flags=re.MULTILINE
                )
            )
            if not matches:
                raise ValueError(f"empty requirements delta section: {section_name}")
            for index, match in enumerate(matches):
                name = match.group(1).strip()
                if not name or name in names:
                    raise ValueError("duplicate or empty requirement name")
                names.add(name)
                if operation in {"ADDED", "MODIFIED"} and len(name) >= 50:
                    raise ValueError("requirement name must be shorter than 50 characters")
                block_end = matches[index + 1].start() if index + 1 < len(matches) else len(body)
                block = body[match.end() : block_end]
                traces.add(f'{operation} Requirement "{name}"')
                if operation in {"ADDED", "MODIFIED"}:
                    shall_lines = re.findall(r"^.+ SHALL .+$", block, flags=re.MULTILINE)
                    scenarios = list(
                        re.finditer(r"^#### Scenario: .+\n", block, flags=re.MULTILINE)
                    )
                    if len(shall_lines) != 1 or not scenarios:
                        raise ValueError(f"invalid requirement structure: {name}")
                    for scenario_index, scenario in enumerate(scenarios):
                        scenario_end = (
                            scenarios[scenario_index + 1].start()
                            if scenario_index + 1 < len(scenarios)
                            else len(block)
                        )
                        scenario_body = block[scenario.end() : scenario_end]
                        if not re.search(r"^- \*\*WHEN\*\* .+$", scenario_body, re.MULTILINE):
                            raise ValueError(f"scenario is missing WHEN: {name}")
                        if not re.search(r"^- \*\*THEN\*\* .+$", scenario_body, re.MULTILINE):
                            raise ValueError(f"scenario is missing THEN: {name}")
                elif not re.search(r"^\*\*Reason\*\*: .+$", block, re.MULTILINE) or not re.search(
                    r"^\*\*Migration\*\*: .+$", block, re.MULTILINE
                ):
                    raise ValueError(f"removed requirement is incomplete: {name}")
        else:
            renames = re.findall(
                r"^- FROM: `### Requirement: ([^`]+)`\n- TO: `### Requirement: ([^`]+)`$",
                body,
                flags=re.MULTILINE,
            )
            if not renames:
                raise ValueError("empty or invalid RENAMED requirements section")
            for old_name, new_name in renames:
                trace = f'RENAMED Requirement "{old_name}" -> "{new_name}"'
                if trace in traces:
                    raise ValueError("duplicate renamed requirement")
                traces.add(trace)
    return traces


def design_identifiers(content: str, required_traces: set[str]) -> set[str]:
    if not content.startswith("# Design\n"):
        raise ValueError("design artifact has an invalid title")
    sections = markdown_sections(content)
    required_sections = {
        "Overview",
        "Decisions",
        "Affected components",
        "Constraints, risks and trade-offs",
        "Verification",
    }
    missing = required_sections - set(sections)
    if missing:
        raise ValueError("design artifact is missing sections: " + ", ".join(sorted(missing)))
    decisions = sections["Decisions"]
    matches = list(re.finditer(r"^### (DES-[0-9]{3}) — .+\n", decisions, re.MULTILINE))
    if not matches:
        raise ValueError("design artifact must contain at least one DES decision")
    identifiers: set[str] = set()
    covered: set[str] = set()
    for index, match in enumerate(matches):
        identifier = match.group(1)
        if identifier in identifiers:
            raise ValueError(f"duplicate design decision ID: {identifier}")
        identifiers.add(identifier)
        end = matches[index + 1].start() if index + 1 < len(matches) else len(decisions)
        block = decisions[match.end() : end]
        covers = re.search(r"^Covers: (.+)$", block, re.MULTILINE)
        if covers is None or not re.search(r"^Decision: .+$", block, re.MULTILINE) or not re.search(
            r"^Rationale: .+$", block, re.MULTILINE
        ):
            raise ValueError(f"incomplete design decision: {identifier}")
        for trace in required_traces:
            if trace in covers.group(1):
                covered.add(trace)
    uncovered = required_traces - covered
    if uncovered:
        raise ValueError("design does not cover: " + ", ".join(sorted(uncovered)))
    return identifiers


def validate_plan_structure(
    content: str, required_traces: set[str], design_ids: set[str]
) -> set[str]:
    if not content.startswith("# Plan\n"):
        raise ValueError("plan artifact has an invalid title")
    sections = markdown_sections(content)
    missing = {"Steps", "Final verification"} - set(sections)
    if missing:
        raise ValueError("plan artifact is missing sections: " + ", ".join(sorted(missing)))
    steps = sections["Steps"]
    matches = list(re.finditer(r"^### (STEP-[0-9]{3}) — .+\n", steps, re.MULTILINE))
    if not matches:
        raise ValueError("plan artifact must contain at least one STEP")
    identifiers: set[str] = set()
    covered_traces: set[str] = set()
    covered_design: set[str] = set()
    for index, match in enumerate(matches):
        identifier = match.group(1)
        if identifier in identifiers:
            raise ValueError(f"duplicate plan step ID: {identifier}")
        identifiers.add(identifier)
        end = matches[index + 1].start() if index + 1 < len(matches) else len(steps)
        block = steps[match.end() : end]
        covers = re.search(r"^Covers: (.+)$", block, re.MULTILINE)
        if covers is None or any(
            re.search(rf"^{field}: .+$", block, re.MULTILINE) is None
            for field in ("Outcome", "Changes", "Verification")
        ):
            raise ValueError(f"incomplete plan step: {identifier}")
        for trace in required_traces:
            if trace in covers.group(1):
                covered_traces.add(trace)
        for design_id in design_ids:
            if re.search(rf"\b{re.escape(design_id)}\b", covers.group(1)):
                covered_design.add(design_id)
    if required_traces - covered_traces:
        raise ValueError("plan does not cover every requirement delta")
    if design_ids - covered_design:
        raise ValueError("plan does not cover every design decision")
    return identifiers


def validate_artifact_path(
    kind: str,
    artifact_path: Path,
    requirements_path: Path | None = None,
    design_path: Path | None = None,
) -> dict[str, object]:
    content = read_bounded_utf8(artifact_path, f"{kind} artifact", ARTIFACT_MAX_BYTES)
    if content.startswith("\ufeff"):
        raise ValueError("artifact must not contain a byte-order mark")
    if kind == "requirements":
        traces = requirement_traceability(content)
        return {"kind": kind, "requirements": len(traces)}
    if kind == "design":
        if requirements_path is None:
            raise ValueError("design validation requires requirements")
        requirement_content = read_bounded_utf8(
            requirements_path, "requirements artifact", ARTIFACT_MAX_BYTES
        )
        traces = requirement_traceability(requirement_content)
        identifiers = design_identifiers(content, traces)
        return {"kind": kind, "decisions": len(identifiers)}
    if kind == "plan":
        if requirements_path is None or design_path is None:
            raise ValueError("plan validation requires requirements and design")
        requirement_content = read_bounded_utf8(
            requirements_path, "requirements artifact", ARTIFACT_MAX_BYTES
        )
        design_content = read_bounded_utf8(design_path, "design artifact", ARTIFACT_MAX_BYTES)
        traces = requirement_traceability(requirement_content)
        design_ids = design_identifiers(design_content, traces)
        identifiers = validate_plan_structure(content, traces, design_ids)
        return {"kind": kind, "steps": len(identifiers)}
    raise ValueError("unsupported artifact kind")


def validate_review_value(
    value: object,
    stage: str,
    expected_inputs: list[dict[str, str]] | None,
) -> dict[str, object]:
    if stage not in {"requirements", "design"}:
        raise ValueError("unsupported review stage")
    review = require_object(
        value, "review", {"schema_version", "stage", "inputs", "verdict", "findings"}
    )
    if type(review["schema_version"]) is not int or review["schema_version"] != 2:
        raise ValueError("review schema_version must be 2")
    if review["stage"] != stage:
        raise ValueError("review stage does not match")
    inputs = review["inputs"]
    if not isinstance(inputs, list):
        raise ValueError("review inputs must be a list")
    normalized_inputs: list[dict[str, str]] = []
    for value in inputs:
        item = require_object(value, "review input", {"path", "sha256"})
        path = require_non_empty_string(item["path"], "review input path")
        digest = require_non_empty_string(item["sha256"], "review input hash")
        if not HASH_PATTERN.fullmatch(digest):
            raise ValueError("invalid review input hash")
        normalized_inputs.append({"path": path, "sha256": digest})
    if expected_inputs is not None and normalized_inputs != expected_inputs:
        raise ValueError("review inputs do not match the role manifest")
    if review["verdict"] not in {"pass", "changes-required"}:
        raise ValueError("invalid review verdict")
    findings = review["findings"]
    if not isinstance(findings, list):
        raise ValueError("review findings must be a list")
    prefix = "REQ-R-" if stage == "requirements" else "DES-R-"
    seen_ids: set[str] = set()
    blocking = False
    for value in findings:
        finding = require_object(
            value,
            "review finding",
            {"id", "severity", "resolution", "references", "problem", "recommendation"},
        )
        identifier = require_non_empty_string(finding["id"], "finding ID")
        if not re.fullmatch(re.escape(prefix) + r"[0-9]{3}", identifier) or identifier in seen_ids:
            raise ValueError("invalid or duplicate review finding ID")
        seen_ids.add(identifier)
        severity = finding["severity"]
        resolution = finding["resolution"]
        if severity not in {"blocking", "advisory"}:
            raise ValueError("invalid finding severity")
        if resolution not in {"author-revision", "user-decision", "none"}:
            raise ValueError("invalid finding resolution")
        if severity == "advisory" and resolution != "none":
            raise ValueError("advisory finding must use resolution none")
        if severity == "blocking" and resolution not in {
            "author-revision",
            "user-decision",
        }:
            raise ValueError(
                "blocking finding must use author-revision or user-decision"
            )
        references = finding["references"]
        if not isinstance(references, list) or not references or any(
            not isinstance(reference, str) or not reference.strip()
            for reference in references
        ):
            raise ValueError("finding references must be a non-empty list of strings")
        require_non_empty_string(finding["problem"], "finding problem")
        require_non_empty_string(finding["recommendation"], "finding recommendation")
        blocking = blocking or severity == "blocking"
    if review["verdict"] == "pass" and blocking:
        raise ValueError("passing review must not contain blocking findings")
    if review["verdict"] == "changes-required" and not blocking:
        raise ValueError("changes-required review must contain a blocking finding")
    return review


def parse_input_hash(argument: str) -> dict[str, str]:
    path, separator, digest = argument.partition("=")
    if not separator or not path or not HASH_PATTERN.fullmatch(digest):
        raise ValueError("review input must use <path>=sha256:<digest>")
    relative = PurePosixPath(path)
    if (
        relative.is_absolute()
        or relative.as_posix() != path
        or any(part in {"", ".", ".."} for part in relative.parts)
    ):
        raise ValueError("review input path must be normalized and project-relative")
    return {"path": path, "sha256": digest}


def validate_review_path(
    review_path: Path, stage: str, input_arguments: list[str]
) -> dict[str, object]:
    content = read_bounded_utf8(review_path, "review", CONTROL_FILE_MAX_BYTES)
    expected_inputs = [parse_input_hash(argument) for argument in input_arguments]
    if len({item["path"] for item in expected_inputs}) != len(expected_inputs):
        raise ValueError("duplicate expected review input")
    return validate_review_value(parse_strict_yaml(content, "review"), stage, expected_inputs)


def validate_role_decision_coverage(
    decisions: object,
    role: str,
    *,
    artifact_references: object | None = None,
    review: object | None = None,
    declared_source_events: object | None = None,
) -> dict[str, object]:
    snapshot = validate_decisions_snapshot(decisions, role)
    if artifact_references is None:
        references: set[str] = set()
    elif isinstance(artifact_references, set) and all(
        isinstance(item, str) and item for item in artifact_references
    ):
        references = set(artifact_references)
    else:
        raise ValueError("artifact references must be a set of non-empty strings")

    source_events: dict[str, dict[str, object]] = {}
    if declared_source_events is not None:
        if not isinstance(declared_source_events, dict):
            raise ValueError("declared source events must be an event-key mapping")
        for key, event_value in declared_source_events.items():
            event = validate_durable_audit_event(event_value)
            if key != event["event_key"]:
                raise ValueError("declared source event key does not match its event")
            if event["actor"] != "user" or event["event"] not in AUDIT_USER_INPUT_EVENTS:
                raise ValueError("declared source event is not a user-input event")
            source_events[str(key)] = event

    source_event_ids: dict[str, str] = {}
    for decision in snapshot:
        if decision["authority"] != "user":
            continue
        for source_key in decision["source_event_keys"]:
            source = source_events.get(str(source_key))
            if source is None:
                raise ValueError(
                    "user-authority decision names an undeclared user-input event"
                )
            source_event_ids[str(source_key)] = str(source["event_id"])

    decision_by_key = {str(decision["key"]): decision for decision in snapshot}
    if role == "requirements-author":
        for decision in snapshot:
            if decision["key"] not in references or any(
                reference not in references for reference in decision["references"]
            ):
                raise ValueError(
                    "requirements decision key or reference is absent from requirements.md"
                )
    elif role == "design-author":
        if set(decision_by_key) != references:
            raise ValueError("design decisions must cover every DES-* exactly once")
        if any(
            reference not in references
            for decision in snapshot
            for reference in decision["references"]
        ):
            raise ValueError("design decision reference is absent from design.md")
    elif role == "planner":
        if any(
            decision["key"] not in references
            or any(reference not in references for reference in decision["references"])
            for decision in snapshot
        ):
            raise ValueError("plan decision key or reference is absent from plan.md")
    elif role in {"requirements-reviewer", "specification-reviewer"}:
        if review is None:
            raise ValueError("review decision coverage requires a validated review")
        expected_stage = (
            "requirements" if role == "requirements-reviewer" else "design"
        )
        review_value = validate_review_value(review, expected_stage, None)
        findings = {str(item["id"]): item for item in review_value["findings"]}
        expected_keys = {"VERDICT", *findings}
        if set(decision_by_key) != expected_keys:
            raise ValueError(
                "review decisions must include one verdict and every finding ID"
            )
        verdict_decision = decision_by_key["VERDICT"]
        if verdict_decision["kind"] != "verdict":
            raise ValueError("review VERDICT decision must use kind verdict")
        finding_reference_universe = {
            reference
            for finding in findings.values()
            for reference in finding["references"]
        }
        if any(
            reference not in finding_reference_universe
            for reference in verdict_decision["references"]
        ):
            raise ValueError("review verdict contains an unknown finding reference")
        for identifier, finding in findings.items():
            decision = decision_by_key[identifier]
            if decision["kind"] != "finding" or decision["references"] != finding["references"]:
                raise ValueError(
                    "review finding decision does not match its validated finding"
                )

    return {
        "decisions": snapshot,
        "source_event_ids": source_event_ids,
    }


def expected_role_input_paths(
    state: dict[str, object], specification: str, role: str, purpose: str
) -> list[str]:
    if role not in ROLES:
        raise ValueError("invalid role")
    if purpose not in PURPOSES:
        raise ValueError("invalid run purpose")
    validate_state_value(state, specification)
    specification_root = f"docs/changes/specs/{specification}"
    required_inputs = {
        "idea-author": [f"{specification_root}/request.md"],
        "requirements-author": [
            f"{specification_root}/request.md",
            f"{specification_root}/idea.md",
        ],
        "requirements-reviewer": [
            f"{specification_root}/request.md",
            f"{specification_root}/idea.md",
            f"{specification_root}/requirements.md",
        ],
        "design-author": [
            f"{specification_root}/request.md",
            f"{specification_root}/idea.md",
            f"{specification_root}/requirements.md",
        ],
        "specification-reviewer": [
            f"{specification_root}/request.md",
            f"{specification_root}/idea.md",
            f"{specification_root}/requirements.md",
            f"{specification_root}/design.md",
        ],
        "planner": [
            f"{specification_root}/idea.md",
            f"{specification_root}/requirements.md",
            f"{specification_root}/design.md",
        ],
    }[role]
    if purpose == "revise":
        artifact = ROLE_OWNED_ARTIFACTS.get(role)
        if artifact is None:
            raise ValueError("only artifact-owning roles may use revise purpose")
        required_inputs.append(f"{specification_root}/{artifact}")
    profile_name = state["execution"]["bindings"][role]
    project_inputs = state["execution"]["profiles"][profile_name]["project_inputs"]
    return required_inputs + [str(item["path"]) for item in project_inputs]


def build_role_manifest_inputs(
    state: dict[str, object],
    project_root: Path,
    specification: str,
    role: str,
    purpose: str,
) -> list[dict[str, str]]:
    project_root = canonical_project_root(project_root)
    inputs: list[dict[str, str]] = []
    for relative in expected_role_input_paths(state, specification, role, purpose):
        path = resolve_project_file(
            project_root, relative, "role manifest input", PROJECT_INPUT_MAX_BYTES
        )
        inputs.append({"path": relative, "sha256": file_hash(path)})
    return inputs


def _validate_active_completed_receipt(
    state: dict[str, object], receipt_value: object
) -> dict[str, object]:
    active_run = state.get("active_run")
    if not isinstance(active_run, dict):
        raise ValueError("completed role-result acceptance requires an active run")
    adapter = str(active_run["adapter"])
    adapter_class = "mailbox" if adapter == "mailbox" else "native"
    profile = state["execution"]["profiles"][active_run["executor"]]
    requested_model = str(profile["model"]) if adapter_class == "mailbox" else None
    requested_reasoning = (
        profile.get("reasoning") if adapter_class == "mailbox" else None
    )
    receipt = validate_receipt(
        receipt_value,
        str(active_run["run_id"]),
        str(active_run["output"]),
        adapter_class,
        requested_model,
        requested_reasoning,
    )
    if receipt["status"] != "completed":
        raise ValueError("role-result acceptance requires a completed receipt")
    return receipt


def _declared_user_source_events(
    state: dict[str, object], durable_events: object
) -> dict[str, dict[str, object]]:
    if not isinstance(durable_events, list):
        raise ValueError("durable audit events must be a list")
    declared_answers = {
        str(item["answer"])
        for item in state["clarifications"]
    }
    pending = state["pending"]
    if isinstance(pending, dict) and pending["response"] is not None:
        declared_answers.add(str(pending["response"]))
    result: dict[str, dict[str, object]] = {}
    for event_value in durable_events:
        event = validate_durable_audit_event(event_value)
        if event["actor"] != "user" or event["event"] not in AUDIT_USER_INPUT_EVENTS:
            continue
        if event["event"] == "user-response" and event["payload"]["verbatim"] not in declared_answers:
            continue
        result[str(event["event_key"])] = event
    return result


def _validate_role_result_files(
    project_root: Path,
    specification: str,
    state: dict[str, object],
    receipt: dict[str, object],
    input_arguments: list[str],
) -> tuple[set[str], dict[str, object] | None, list[Path]]:
    active_run = state["active_run"]
    assert isinstance(active_run, dict)
    role = str(active_run["role"])
    purpose = str(active_run["purpose"])
    expected_paths = expected_role_input_paths(state, specification, role, purpose)
    inputs = [parse_input_hash(argument) for argument in input_arguments]
    if [str(item["path"]) for item in inputs] != expected_paths:
        raise ValueError("role-result inputs do not match the role manifest order")
    if len({str(item["path"]) for item in inputs}) != len(inputs):
        raise ValueError("role-result inputs contain a duplicate path")

    revision_artifact = ROLE_OWNED_ARTIFACTS.get(role)
    replaced_revision_input = (
        f"docs/changes/specs/{specification}/{revision_artifact}"
        if purpose == "revise" and revision_artifact is not None
        else None
    )
    if (
        replaced_revision_input is not None
        and active_run["output"] != replaced_revision_input
    ):
        raise ValueError("revision output is not the canonical role-owned artifact")

    observed_paths: list[Path] = []
    for item in inputs:
        if item["path"] == replaced_revision_input:
            # The manifest validator pinned and read this pre-revision input. By
            # result acceptance the executor has atomically replaced that same
            # path, so only the receipt's output hash can describe its bytes.
            continue
        path = resolve_project_file(
            project_root,
            str(item["path"]),
            "role-result input",
            PROJECT_INPUT_MAX_BYTES,
        )
        if file_hash(path) != item["sha256"]:
            raise ValueError(f"role-result input changed: {item['path']}")
        observed_paths.append(path)

    output = receipt["output"]
    output_path = resolve_project_file(
        project_root,
        str(output["path"]),
        "role-result output",
        ARTIFACT_MAX_BYTES,
    )
    if file_hash(output_path) != output["sha256"]:
        raise ValueError("role-result output hash does not match its receipt")
    observed_paths.append(output_path)

    artifact_references: set[str] = set()
    review: dict[str, object] | None = None
    if role == "requirements-author":
        content = read_bounded_utf8(
            output_path, "requirements artifact", ARTIFACT_MAX_BYTES
        )
        if content.startswith("\ufeff"):
            raise ValueError("artifact must not contain a byte-order mark")
        artifact_references = requirement_traceability(content)
    elif role == "design-author":
        content = read_bounded_utf8(output_path, "design artifact", ARTIFACT_MAX_BYTES)
        if content.startswith("\ufeff"):
            raise ValueError("artifact must not contain a byte-order mark")
        requirements_path = resolve_project_file(
            project_root,
            f"docs/changes/specs/{specification}/requirements.md",
            "requirements artifact",
            ARTIFACT_MAX_BYTES,
        )
        traces = requirement_traceability(
            read_bounded_utf8(
                requirements_path, "requirements artifact", ARTIFACT_MAX_BYTES
            )
        )
        artifact_references = design_identifiers(content, traces)
    elif role == "planner":
        content = read_bounded_utf8(output_path, "plan artifact", ARTIFACT_MAX_BYTES)
        if content.startswith("\ufeff"):
            raise ValueError("artifact must not contain a byte-order mark")
        requirements_path = resolve_project_file(
            project_root,
            f"docs/changes/specs/{specification}/requirements.md",
            "requirements artifact",
            ARTIFACT_MAX_BYTES,
        )
        design_path = resolve_project_file(
            project_root,
            f"docs/changes/specs/{specification}/design.md",
            "design artifact",
            ARTIFACT_MAX_BYTES,
        )
        traces = requirement_traceability(
            read_bounded_utf8(
                requirements_path, "requirements artifact", ARTIFACT_MAX_BYTES
            )
        )
        design_ids = design_identifiers(
            read_bounded_utf8(design_path, "design artifact", ARTIFACT_MAX_BYTES),
            traces,
        )
        artifact_references = validate_plan_structure(content, traces, design_ids)
    elif role in {"requirements-reviewer", "specification-reviewer"}:
        review_stage = (
            "requirements" if role == "requirements-reviewer" else "design"
        )
        review = validate_review_value(
            parse_strict_yaml(
                read_bounded_utf8(output_path, "review", CONTROL_FILE_MAX_BYTES),
                "review",
            ),
            review_stage,
            inputs,
        )
    else:
        content = read_bounded_utf8(output_path, "idea artifact", ARTIFACT_MAX_BYTES)
        if content.startswith("\ufeff"):
            raise ValueError("artifact must not contain a byte-order mark")
    return artifact_references, review, observed_paths


def prepare_completed_role_result_state(
    state: dict[str, object],
    specification: str,
    receipt_value: object,
    *,
    artifact_references: object,
    review: object | None,
    durable_events: object,
    review_question: object | None = None,
) -> tuple[dict[str, object], dict[str, object]]:
    validate_state_value(state, specification)
    require_empty_audit_outbox(state, "role-result acceptance")
    if state["status"] == "waiting-executor":
        raise ValueError(
            "waiting-executor must be ended before completed role-result acceptance"
        )
    receipt = _validate_active_completed_receipt(state, receipt_value)
    active_run = state["active_run"]
    assert isinstance(active_run, dict)
    run_id = str(active_run["run_id"])
    role = str(active_run["role"])
    stage = str(active_run["stage"])
    output = receipt["output"]
    artifact_path = str(output["path"])
    artifact_sha256 = str(output["sha256"])
    receipt_sha256 = canonical_receipt_sha256(receipt)

    if not isinstance(durable_events, list):
        raise ValueError("durable audit events must be a list")
    durable_by_key: dict[str, dict[str, object]] = {}
    for event_value in durable_events:
        event = validate_durable_audit_event(event_value)
        key = str(event["event_key"])
        if key in durable_by_key:
            raise ValueError("durable audit log contains a duplicate event key")
        durable_by_key[key] = event
    reservation_key = f"run/{run_id}/reserved"
    reservation = durable_by_key.get(reservation_key)
    if reservation is None or reservation["event"] != "role-run-reserved":
        raise ValueError("active run has no durable reservation event")
    if active_run["adapter"] == "mailbox":
        wait_events = sorted(
            (
                event
                for event in durable_events
                if event.get("run_id") == run_id
                and event["event"] in {"executor-wait-started", "executor-wait-ended"}
            ),
            key=lambda event: parse_audit_event_id(event["event_id"]),
        )
        if wait_events and (
            wait_events[-1]["event"] != "executor-wait-ended"
            or wait_events[-1]["payload"]["outcome"] != "completed"
        ):
            raise ValueError(
                "mailbox result acceptance requires a durable completed wait outcome"
            )

    declared_sources = _declared_user_source_events(state, durable_events)
    coverage = validate_role_decision_coverage(
        receipt["decisions"],
        role,
        artifact_references=artifact_references,
        review=review,
        declared_source_events=declared_sources,
    )
    completion_key = f"run/{run_id}/completed"
    acceptance_name = (
        "review-accepted"
        if role in {"requirements-reviewer", "specification-reviewer"}
        else "artifact-accepted"
    )
    acceptance_key = f"run/{run_id}/{acceptance_name}"
    executor = receipt.get("executor")
    effective = executor["effective"] if isinstance(executor, dict) else {}
    events = [
        build_queued_audit_event(
            event_key=completion_key,
            kind="lifecycle",
            event="role-run-completed",
            stage=stage,
            actor="router",
            run_id=run_id,
            related_events=[reservation["event_id"]],
            payload={
                "receipt_sha256": receipt_sha256,
                "effective_model": effective.get("model", "unavailable"),
                "effective_reasoning": effective.get("reasoning", "unavailable"),
            },
        )
    ]
    acceptance_payload: dict[str, object]
    if acceptance_name == "review-accepted":
        assert isinstance(review, dict)
        acceptance_payload = {
            "path": artifact_path,
            "sha256": artifact_sha256,
            "receipt_sha256": receipt_sha256,
            "verdict": review["verdict"],
            "finding_ids": [finding["id"] for finding in review["findings"]],
        }
    else:
        acceptance_payload = {
            "path": artifact_path,
            "sha256": artifact_sha256,
            "receipt_sha256": receipt_sha256,
        }
    events.append(
        build_queued_audit_event(
            event_key=acceptance_key,
            kind="lifecycle",
            event=acceptance_name,
            stage=stage,
            actor="router",
            run_id=run_id,
            related_event_keys=[completion_key],
            payload=acceptance_payload,
        )
    )

    source_event_ids = coverage["source_event_ids"]
    for decision in sorted(
        (
            decision
            for decision in coverage["decisions"]
            if decision["authority"] == "user"
        ),
        key=lambda item: str(item["key"]),
    ):
        key = str(decision["key"])
        events.append(
            build_queued_audit_event(
                event_key=(
                    f"run/{run_id}/normalization/{decision_event_key_segment(key)}"
                ),
                kind="interaction",
                event="clarification-applied",
                stage=stage,
                actor=role,
                run_id=run_id,
                related_events=[
                    source_event_ids[str(source_key)]
                    for source_key in decision["source_event_keys"]
                ],
                related_event_keys=[acceptance_key],
                payload={
                    "key": key,
                    "decision_kind": decision["kind"],
                    "summary": decision["summary"],
                    "rationale": decision["rationale"],
                    "alternatives": copy.deepcopy(decision["alternatives"]),
                    "references": list(decision["references"]),
                    "artifact_path": artifact_path,
                    "artifact_sha256": artifact_sha256,
                    "receipt_sha256": receipt_sha256,
                },
            )
        )

    diff = prepare_role_decision_diff(
        role,
        coverage["decisions"],
        state["audit"]["decision_index"],
        run_id=run_id,
        artifact_path=artifact_path,
        artifact_sha256=artifact_sha256,
        receipt_sha256=receipt_sha256,
        acceptance_event_key=acceptance_key,
    )
    events.extend(diff["events"])

    updated = copy.deepcopy(state)
    updated["active_run"] = None
    pending = updated["pending"]
    if role in STAGE_OWNERS.values():
        if review_question is not None:
            raise ValueError("owner result does not accept a review question")
        if pending is not None:
            if pending["response"] is None:
                raise ValueError("owner result cannot clear an unanswered pending item")
            if pending["kind"] == "clarification":
                updated["clarifications"].append(
                    {
                        "stage": pending["stage"],
                        "origin": pending["origin"],
                        "question": pending["request"],
                        "answer": pending["response"],
                    }
                )
            updated["pending"] = None
        updated["status"] = (
            "reviewing" if stage in STAGE_REVIEWERS else "awaiting-approval"
        )
        transition_name = "owner-result-accepted"
    else:
        if not isinstance(review, dict):
            raise ValueError("reviewer completion requires a validated review")
        transition_name = "reviewer-result-accepted"
        if review["verdict"] == "pass":
            if review_question is not None:
                raise ValueError("passing review does not accept a blocking question")
            updated["status"] = "awaiting-approval"
        else:
            blocking = [
                finding
                for finding in review["findings"]
                if finding["severity"] == "blocking"
            ]
            user_decisions = [
                finding
                for finding in blocking
                if finding["resolution"] == "user-decision"
            ]
            if user_decisions:
                question = require_bounded_text(
                    review_question, "review user-decision question"
                )
                updated["status"] = "awaiting-decision"
                updated["pending"] = {
                    "kind": "clarification",
                    "origin": "review",
                    "stage": stage,
                    "request": question,
                    "response": None,
                    "resume_purpose": "revise",
                }
                events.append(
                    build_queued_audit_event(
                        event_key=f"run/{run_id}/blocking-question",
                        kind="interaction",
                        event="blocking-question",
                        stage=stage,
                        actor=role,
                        run_id=run_id,
                        related_event_keys=[completion_key],
                        payload={
                            "question": question,
                            "origin": "review",
                            "resume_purpose": "revise",
                        },
                    )
                )
            else:
                attempt = int(updated["automatic_revision_attempts"]) + 1
                if attempt > 3:
                    raise ValueError("automatic revision limit was already reached")
                updated["automatic_revision_attempts"] = attempt
                reason = "Blocking review findings: " + ", ".join(
                    str(finding["id"]) for finding in blocking
                )
                if attempt < 3:
                    if review_question is not None:
                        raise ValueError(
                            "automatic revision before the limit does not accept a question"
                        )
                    updated["status"] = "revising"
                    events.append(
                        build_queued_audit_event(
                            event_key=(
                                f"workflow/{specification}/automatic-revision/"
                                f"{run_id}/{attempt}"
                            ),
                            kind="lifecycle",
                            event="automatic-revision-started",
                            stage=stage,
                            actor="router",
                            related_event_keys=[acceptance_key],
                            payload={"attempt": attempt, "reason": reason},
                        )
                    )
                else:
                    question = require_bounded_text(
                        review_question, "automatic revision limit question"
                    )
                    updated["status"] = "awaiting-decision"
                    updated["pending"] = {
                        "kind": "revision",
                        "origin": "router",
                        "stage": stage,
                        "request": question,
                        "response": None,
                        "resume_purpose": "revise",
                    }
                    events.extend(
                        [
                            build_queued_audit_event(
                                event_key=(
                                    f"workflow/{specification}/revision-limit/{run_id}"
                                ),
                                kind="lifecycle",
                                event="automatic-revision-limit-reached",
                                stage=stage,
                                actor="router",
                                related_event_keys=[completion_key],
                                payload={"attempt": 3, "reason": reason},
                            ),
                            build_queued_audit_event(
                                event_key=(
                                    f"interaction/{specification}/revision-limit/{run_id}/question"
                                ),
                                kind="interaction",
                                event="blocking-question",
                                stage=stage,
                                actor="router",
                                related_event_keys=[completion_key],
                                payload={
                                    "question": question,
                                    "origin": "router",
                                    "resume_purpose": "revise",
                                },
                            ),
                        ]
                    )
    validate_transition_event_batch(transition_name, events)
    keys = [str(event["event_key"]) for event in events]
    if len(set(keys)) != len(keys):
        raise ValueError("role-result batch contains a duplicate event key")
    conflicts = sorted(set(keys) & set(durable_by_key))
    if conflicts:
        raise ValueError(
            "role-result event key conflicts with the durable log: " + conflicts[0]
        )
    decision_index_after = decision_index_after_queued_events(
        state["audit"]["decision_index"],
        events,
        int(state["audit"]["next_event_sequence"]),
    )
    updated = queue_audit_transaction(
        updated,
        specification,
        events,
        decision_index_after=decision_index_after,
    )
    result = {
        "run_id": run_id,
        "receipt_sha256": receipt_sha256,
        "transaction_id": updated["audit"]["outbox"]["transaction_id"],
        "event_keys": [
            str(event["event_key"])
            for event in updated["audit"]["outbox"]["events"]
        ],
        "unchanged_decision_keys": diff["unchanged_keys"],
    }
    return updated, result


def accept_completed_role_result_in_state(
    state_path: Path,
    project_root: Path,
    specification: str,
    receipt_value: object,
    input_arguments: list[str],
    review_question: object | None = None,
) -> dict[str, object]:
    project_root = canonical_project_root(project_root)
    state_content, state = validate_state_path(
        state_path, project_root, specification
    )
    require_empty_audit_outbox(state, "role-result acceptance")
    if state["status"] == "waiting-executor":
        raise ValueError(
            "waiting-executor must be ended before completed role-result acceptance"
        )
    receipt = _validate_active_completed_receipt(state, receipt_value)
    artifact_references, review, observed_paths = _validate_role_result_files(
        project_root,
        specification,
        state,
        receipt,
        input_arguments,
    )
    audit = state["audit"]
    log_path = resolve_project_file(
        project_root,
        str(audit["log_path"]),
        "feature audit log",
        AUDIT_LOG_MAX_BYTES,
    )
    log_bytes = log_path.read_bytes()
    log_stat = log_path.stat()
    parsed_log = validate_audit_log(
        log_bytes,
        expected_log_size_bytes=audit["log_size_bytes"],
        expected_log_sha256=audit["log_sha256"],
        expected_head_event_id=audit["head_event_id"],
        expected_head_event_sha256=audit["head_event_sha256"],
        expected_next_event_sequence=audit["next_event_sequence"],
    )
    updated, result = prepare_completed_role_result_state(
        state,
        specification,
        receipt,
        artifact_references=artifact_references,
        review=review,
        durable_events=parsed_log["events"],
        review_question=review_question,
    )
    if parse_strict_yaml(state_content, "state") != state:
        raise ValueError("state content does not match its validated value")
    initial_stat = state_path.stat()
    initial_bytes = state_path.read_bytes()
    if initial_bytes != state_content.encode("utf-8"):
        raise ValueError("state changed while accepting role result")
    observed = {
        path: (path.read_bytes(), path.stat())
        for path in {*observed_paths}
    }
    confirmed_references, confirmed_review, _ = _validate_role_result_files(
        project_root,
        specification,
        state,
        receipt,
        input_arguments,
    )
    if confirmed_references != artifact_references or confirmed_review != review:
        raise ValueError("role-result files changed while being validated")

    def verify_inputs_and_log() -> None:
        for path, (content, stat) in observed.items():
            _assert_file_unchanged(path, content, stat, "accepting role result")
        _assert_file_unchanged(
            log_path, log_bytes, log_stat, "accepting role result"
        )

    _atomic_replace_bytes(
        state_path,
        render_canonical_yaml_mapping(updated),
        expected_bytes=initial_bytes,
        expected_stat=initial_stat,
        operation="accepting role result",
        _before_replace=verify_inputs_and_log,
    )
    _, persisted = validate_state_path(state_path, project_root, specification)
    if persisted != updated:
        raise ValueError("persisted role-result state does not match the prepared state")
    return result


def _audit_events_for_state(
    project_root: Path, state: dict[str, object]
) -> tuple[Path, bytes, os.stat_result, list[dict[str, object]]]:
    audit = state["audit"]
    log_path = resolve_project_file(
        project_root,
        str(audit["log_path"]),
        "feature audit log",
        AUDIT_LOG_MAX_BYTES,
    )
    log_bytes = log_path.read_bytes()
    log_stat = log_path.stat()
    parsed = validate_audit_log(
        log_bytes,
        expected_log_size_bytes=audit["log_size_bytes"],
        expected_log_sha256=audit["log_sha256"],
        expected_head_event_id=audit["head_event_id"],
        expected_head_event_sha256=audit["head_event_sha256"],
        expected_next_event_sequence=audit["next_event_sequence"],
    )
    return log_path, log_bytes, log_stat, parsed["events"]


def _audit_event_by_key(
    events: list[dict[str, object]], key: str
) -> dict[str, object] | None:
    matching = [event for event in events if event["event_key"] == key]
    if len(matching) > 1:
        raise ValueError("durable audit log contains a duplicate event key")
    return matching[0] if matching else None


def _transition_token(value: object, name: str) -> str:
    token = require_non_empty_string(value, name)
    if len(token) > 128 or not re.fullmatch(r"[a-z0-9][A-Za-z0-9._:-]*", token):
        raise ValueError(f"invalid {name}")
    return token


def _active_run_status(active_run: dict[str, object]) -> str:
    return {
        "draft": "drafting",
        "revise": "revising",
        "review": "reviewing",
    }[str(active_run["purpose"])]


def _transition_anchor_key(
    transition: str, specification: str, details: object
) -> str:
    if not isinstance(details, dict):
        raise ValueError("workflow transition input must be an object")
    if transition in {"role-blocked", "role-failed"}:
        receipt = details.get("receipt")
        if not isinstance(receipt, dict):
            raise ValueError("role outcome requires a receipt")
        run_id = require_non_empty_string(receipt.get("run_id"), "receipt run ID")
        suffix = "blocked" if transition == "role-blocked" else "failed"
        return f"run/{run_id}/{suffix}"
    if transition == "approve-stage":
        stage = require_non_empty_string(details.get("stage"), "approval stage")
        return f"checkpoint/{specification}/stage/{stage}/approved"
    if transition in {"checkpoint-commit-succeeded", "checkpoint-commit-failed"}:
        selection = validate_audit_event_key(
            details.get("selection_event_key"), "commit selection event key"
        )
        if transition == "checkpoint-commit-succeeded":
            return f"{selection}/commit-completed"
        attempt = _transition_token(details.get("attempt_id"), "commit attempt ID")
        return f"{selection}/commit-failed/{attempt}"
    if transition in {
        "mailbox-wait-started",
        "mailbox-wait-completed",
        "mailbox-wait-timeout",
        "mailbox-response-malformed",
        "mailbox-wait-interrupted",
    }:
        run_id = require_non_empty_string(details.get("run_id"), "mailbox run ID")
        wait_id = _transition_token(details.get("wait_id"), "mailbox wait ID")
        suffix = "started" if transition == "mailbox-wait-started" else "ended"
        return f"run/{run_id}/wait/{wait_id}/{suffix}"
    if transition == "native-run-interrupted":
        run_id = require_non_empty_string(details.get("run_id"), "native run ID")
        return f"run/{run_id}/interrupted"
    operation_id = _transition_token(details.get("operation_id"), "operation ID")
    if transition == "upstream-invalidation" and details.get("verbatim") is None:
        return f"workflow/{specification}/invalidation/{operation_id}"
    namespace = {
        "pending-answer": "answer",
        "collision-selection": "collision",
        "user-revision": "revision",
        "artifact-question": "question",
        "flow-stop": "stop",
        "flow-resume": "resume",
        "native-run-recovery": "native-recovery",
        "upstream-invalidation": "upstream-invalidation",
        "trusted-integrity-pause": "integrity-pause",
    }.get(transition)
    if namespace is None:
        raise ValueError("transition has no executable anchor")
    return f"interaction/{specification}/{namespace}/{operation_id}"


def _validate_replayed_transition(
    transition: str,
    details: dict[str, object],
    anchor: dict[str, object],
) -> None:
    expected_types = set(FEATURE_TRANSITION_TABLE[transition]["events"])
    if audit_event_type(anchor) not in expected_types:
        raise ValueError("transition anchor conflicts with its declared event type")
    payload = anchor["payload"]
    if transition in {"role-blocked", "role-failed"}:
        receipt = details["receipt"]
        if payload["receipt_sha256"] != canonical_receipt_sha256(receipt):
            raise ValueError("replayed role outcome conflicts with durable evidence")
    elif transition == "pending-answer" and payload["verbatim"] != details.get("verbatim"):
        raise ValueError("replayed pending answer conflicts with durable evidence")
    elif transition == "collision-selection" and (
        payload["choice"] != details.get("choice")
        or payload["verbatim"] != details.get("verbatim")
    ):
        raise ValueError("replayed collision selection conflicts with durable evidence")
    elif transition == "user-revision" and payload["verbatim"] != details.get("verbatim"):
        raise ValueError("replayed revision conflicts with durable evidence")
    elif transition == "artifact-question" and payload["verbatim"] != details.get("question"):
        raise ValueError("replayed artifact question conflicts with durable evidence")
    elif transition == "flow-stop" and payload["verbatim"] != details.get("verbatim"):
        raise ValueError("replayed stop conflicts with durable evidence")
    elif transition in {"flow-resume", "native-run-recovery"} and (
        payload["choice"] != details.get("choice")
        or payload["verbatim"] != details.get("verbatim")
    ):
        raise ValueError("replayed recovery conflicts with durable evidence")
    elif transition == "mailbox-wait-started" and payload["wait_seconds"] != details.get("wait_seconds"):
        raise ValueError("replayed mailbox wait conflicts with durable evidence")
    elif transition.startswith("mailbox-") and transition != "mailbox-wait-started":
        expected_outcome = {
            "mailbox-wait-completed": "completed",
            "mailbox-wait-timeout": "timeout",
            "mailbox-response-malformed": "malformed-response",
            "mailbox-wait-interrupted": "interrupted",
        }[transition]
        if payload != {"outcome": expected_outcome, "detail": details.get("detail")}:
            raise ValueError("replayed mailbox outcome conflicts with durable evidence")
    elif transition == "native-run-interrupted" and payload["reason"] != details.get("reason"):
        raise ValueError("replayed interruption conflicts with durable evidence")
    elif transition == "approve-stage" and (
        payload["action"] != details.get("action")
        or payload["verbatim"] != details.get("verbatim")
    ):
        raise ValueError("replayed approval conflicts with durable evidence")
    elif transition == "checkpoint-commit-failed" and payload["error"] != details.get("error"):
        raise ValueError("replayed commit failure conflicts with durable evidence")
    elif transition == "upstream-invalidation":
        if anchor["event"] == "revision-feedback-submitted":
            conflict = payload["verbatim"] != details.get("verbatim")
        else:
            conflict = (
                payload["to_stage"] != details.get("to_stage")
                or payload["reason"] != details.get("reason")
            )
        if conflict:
            raise ValueError("replayed upstream invalidation conflicts with durable evidence")
    elif transition == "trusted-integrity-pause" and payload["question"] != details.get("question"):
        raise ValueError("replayed integrity pause conflicts with durable evidence")


def _verify_role_output_unchanged(
    project_root: Path,
    active_run: dict[str, object],
    expected_sha256: object,
) -> list[Path]:
    relative = str(active_run["output"])
    path = project_root.joinpath(*PurePosixPath(relative).parts)
    if expected_sha256 is None:
        if path.exists():
            raise ValueError("blocked or failed draft changed its declared output")
        return []
    digest = validate_audit_hash(expected_sha256, "pre-run output hash")
    resolved = resolve_project_file(
        project_root,
        relative,
        "pre-run output",
        ARTIFACT_MAX_BYTES,
    )
    if file_hash(resolved) != digest:
        raise ValueError("blocked or failed run changed its declared output")
    return [resolved]


def _approval_evidence(
    project_root: Path,
    specification: str,
    stage: str,
    durable_events: list[dict[str, object]],
) -> tuple[str, str, str | None, dict[str, object] | None, list[Path], str]:
    artifact_relative = f"docs/changes/specs/{specification}/{stage}.md"
    artifact = resolve_project_file(
        project_root, artifact_relative, f"{stage} artifact", ARTIFACT_MAX_BYTES
    )
    artifact_sha256 = file_hash(artifact)
    artifact_acceptances = [
        event
        for event in durable_events
        if event["event"] == "artifact-accepted"
        and event["stage"] == stage
        and event["payload"]["path"] == artifact_relative
        and event["payload"]["sha256"] == artifact_sha256
    ]
    if not artifact_acceptances:
        raise ValueError("approval artifact has no durable acceptance evidence")
    related_event_id = str(artifact_acceptances[-1]["event_id"])
    observed = [artifact]
    if stage not in {"requirements", "design"}:
        return artifact_relative, artifact_sha256, None, None, observed, related_event_id
    review_relative = f"docs/changes/specs/{specification}/review/{stage}.yaml"
    review_path = resolve_project_file(
        project_root, review_relative, f"{stage} review", CONTROL_FILE_MAX_BYTES
    )
    review_sha256 = file_hash(review_path)
    review_value = validate_review_value(
        parse_strict_yaml(
            read_bounded_utf8(review_path, f"{stage} review", CONTROL_FILE_MAX_BYTES),
            f"{stage} review",
        ),
        stage,
        None,
    )
    review_acceptances = [
        event
        for event in durable_events
        if event["event"] == "review-accepted"
        and event["stage"] == stage
        and event["payload"]["path"] == review_relative
        and event["payload"]["sha256"] == review_sha256
    ]
    if not review_acceptances:
        raise ValueError("approval review has no durable acceptance evidence")
    observed.append(review_path)
    return (
        artifact_relative,
        artifact_sha256,
        review_sha256,
        review_value,
        observed,
        str(review_acceptances[-1]["event_id"]),
    )


def _apply_approved_checkpoint(
    state: dict[str, object],
    stage: str,
    artifact_sha256: str,
    review_sha256: str | None,
    accepted_risks: list[str],
) -> dict[str, object]:
    updated = copy.deepcopy(state)
    updated["approvals"][stage] = {
        "artifact_sha256": artifact_sha256,
        "review_sha256": review_sha256,
        "accepted_risks": list(accepted_risks),
    }
    updated["automatic_revision_attempts"] = 0
    updated["checkpoint_commit"] = None
    if stage == "plan":
        updated["status"] = "approved"
    else:
        updated["stage"] = STAGES[STAGES.index(stage) + 1]
        updated["status"] = "drafting"
    return updated


def _prepare_workflow_transition(
    transition: str,
    details_value: object,
    state: dict[str, object],
    specification: str,
    project_root: Path,
    durable_events: list[dict[str, object]],
) -> tuple[dict[str, object], list[dict[str, object]], str, list[Path]]:
    details = details_value if isinstance(details_value, dict) else None
    if details is None:
        raise ValueError("workflow transition input must be an object")
    anchor = _transition_anchor_key(transition, specification, details)
    handler = FEATURE_TRANSITION_TABLE[transition]["handler"]
    updated = copy.deepcopy(state)
    events: list[dict[str, object]] = []
    observed: list[Path] = []

    if handler == "role-outcome":
        outcome = require_object(
            details,
            "role outcome transition",
            {"receipt", "output_sha256_before"},
        )
        active_run = state["active_run"]
        if not isinstance(active_run, dict):
            raise ValueError("role outcome requires an active run")
        profile = state["execution"]["profiles"][active_run["executor"]]
        adapter = "mailbox" if active_run["adapter"] == "mailbox" else "native"
        receipt = validate_receipt(
            outcome["receipt"],
            str(active_run["run_id"]),
            str(active_run["output"]),
            adapter,
            profile.get("model") if adapter == "mailbox" else None,
            profile.get("reasoning") if adapter == "mailbox" else None,
        )
        expected_status = "blocked" if transition == "role-blocked" else "failed"
        if receipt["status"] != expected_status:
            raise ValueError("receipt status does not match role outcome transition")
        observed.extend(
            _verify_role_output_unchanged(
                project_root, active_run, outcome["output_sha256_before"]
            )
        )
        run_id = str(active_run["run_id"])
        reservation = _audit_event_by_key(durable_events, f"run/{run_id}/reserved")
        if reservation is None:
            raise ValueError("role outcome has no durable reservation")
        receipt_sha256 = canonical_receipt_sha256(receipt)
        event_name = "role-run-blocked" if expected_status == "blocked" else "role-run-failed"
        payload = (
            {"receipt_sha256": receipt_sha256}
            if expected_status == "blocked"
            else {"receipt_sha256": receipt_sha256, "error": receipt["error"]}
        )
        events.append(
            build_queued_audit_event(
                event_key=anchor,
                kind="lifecycle",
                event=event_name,
                stage=str(active_run["stage"]),
                actor="router",
                run_id=run_id,
                related_events=[reservation["event_id"]],
                payload=payload,
            )
        )
        updated["active_run"] = None
        if expected_status == "blocked":
            question_key = f"run/{run_id}/blocking-question"
            events.append(
                build_queued_audit_event(
                    event_key=question_key,
                    kind="interaction",
                    event="blocking-question",
                    stage=str(active_run["stage"]),
                    actor=str(active_run["role"]),
                    run_id=run_id,
                    related_event_keys=[anchor],
                    payload={
                        "question": receipt["question"],
                        "origin": "author",
                        "resume_purpose": active_run["purpose"],
                    },
                )
            )
            updated["status"] = "awaiting-decision"
            updated["pending"] = {
                "kind": "clarification",
                "origin": "author",
                "stage": active_run["stage"],
                "request": receipt["question"],
                "response": None,
                "resume_purpose": active_run["purpose"],
            }
        else:
            updated["status"] = _active_run_status(active_run)

    elif handler == "collision-selection":
        collision = require_object(
            details, "collision selection", {"operation_id", "choice", "verbatim"}
        )
        choice = require_non_empty_string(collision["choice"], "collision choice")
        if choice != specification:
            raise ValueError("collision choice must identify the loaded specification")
        verbatim = collision["verbatim"]
        if verbatim is not None:
            require_bounded_text(verbatim, "collision selection verbatim")
        events.append(
            build_queued_audit_event(
                event_key=anchor,
                kind="interaction",
                event="specification-collision-selected",
                stage="workflow",
                actor="user",
                payload={"choice": choice, "verbatim": verbatim},
            )
        )

    elif handler == "pending-answer":
        answer = require_object(details, "pending answer", {"operation_id", "verbatim"})
        pending = state["pending"]
        if not isinstance(pending, dict) or pending["response"] is not None:
            raise ValueError("pending answer requires one unanswered question")
        verbatim = require_bounded_text(answer["verbatim"], "pending answer verbatim")
        updated["pending"]["response"] = verbatim
        updated["status"] = (
            "drafting" if pending["resume_purpose"] == "draft" else "revising"
        )
        if pending["origin"] in {"review", "router"}:
            updated["automatic_revision_attempts"] = 0
        question_events = [
            event
            for event in durable_events
            if event["event"] == "blocking-question"
            and event["stage"] == pending["stage"]
            and event["payload"]["question"] == pending["request"]
        ]
        if not question_events:
            raise ValueError("pending question has no durable audit evidence")
        events.append(
            build_queued_audit_event(
                event_key=anchor,
                kind="interaction",
                event="user-response",
                stage=str(pending["stage"]),
                actor="user",
                related_events=[question_events[-1]["event_id"]],
                payload={"verbatim": verbatim},
            )
        )

    elif handler == "user-revision":
        revision = require_object(
            details, "user revision", {"operation_id", "verbatim"}
        )
        if state["status"] not in {"awaiting-approval", "awaiting-decision"}:
            raise ValueError("user revision requires a user checkpoint")
        if state["active_run"] is not None or state["checkpoint_commit"] is not None:
            raise ValueError("user revision requires an idle checkpoint")
        verbatim = require_bounded_text(revision["verbatim"], "revision feedback")
        updated["status"] = "revising"
        updated["automatic_revision_attempts"] = 0
        if updated["pending"] is None:
            updated["pending"] = {
                "kind": "revision",
                "origin": "router",
                "stage": state["stage"],
                "request": USER_REVISION_PENDING_REQUEST,
                "response": verbatim,
                "resume_purpose": "revise",
            }
        else:
            updated["pending"] = {
                **updated["pending"],
                "kind": "revision",
                "origin": "router",
                "response": verbatim,
                "resume_purpose": "revise",
            }
        events.append(
            build_queued_audit_event(
                event_key=anchor,
                kind="interaction",
                event="revision-feedback-submitted",
                stage=str(state["stage"]),
                actor="user",
                payload={"verbatim": verbatim},
            )
        )

    elif handler == "approve-stage":
        approval = require_object(
            details,
            "stage approval",
            {"stage", "action", "verbatim", "accepted_risks"},
        )
        stage = require_non_empty_string(approval["stage"], "approval stage")
        if stage != state["stage"] or state["status"] != "awaiting-approval":
            raise ValueError("approval does not match the current checkpoint")
        if state["pending"] is not None or state["active_run"] is not None:
            raise ValueError("approval requires an idle checkpoint")
        if state["checkpoint_commit"] is not None:
            raise ValueError("checkpoint commit selection is already pending")
        action = require_non_empty_string(approval["action"], "approval action")
        if action not in APPROVAL_ACTIONS:
            raise ValueError("invalid approval action")
        verbatim = approval["verbatim"]
        if verbatim is not None:
            require_bounded_text(verbatim, "approval verbatim")
        risks = validate_audit_string_list(
            approval["accepted_risks"],
            "accepted risks",
            AUDIT_MAX_REFERENCES_PER_DECISION,
        )
        if risks != sorted(set(risks)):
            raise ValueError("accepted risks must be sorted and unique")
        if risks and action != "continue":
            raise ValueError("risk acceptance advances without commit selection")
        (
            artifact_path,
            artifact_sha256,
            review_sha256,
            review,
            approval_paths,
            acceptance_event_id,
        ) = _approval_evidence(
            project_root, specification, stage, durable_events
        )
        observed.extend(approval_paths)
        if review is None:
            if risks:
                raise ValueError("unreviewed stage cannot accept review findings")
        else:
            blocking = [
                finding
                for finding in review["findings"]
                if finding["severity"] == "blocking"
            ]
            user_decisions = [
                finding for finding in blocking if finding["resolution"] == "user-decision"
            ]
            if user_decisions:
                raise ValueError("approval cannot bypass a user-decision finding")
            required_risks = sorted(
                str(finding["id"])
                for finding in blocking
                if finding["resolution"] == "author-revision"
            )
            if review["verdict"] == "pass" and risks:
                raise ValueError("passing review has no blocking risk to accept")
            if review["verdict"] == "changes-required" and risks != required_risks:
                raise ValueError("accepted risks must cover every blocking finding")
        commit_message = (
            f"stepan({specification}): approve {stage}"
            if action == "continue-and-commit"
            else None
        )
        risk_key = f"{anchor}/risks"
        if risks:
            events.append(
                build_queued_audit_event(
                    event_key=risk_key,
                    kind="interaction",
                    event="risk-accepted",
                    stage=stage,
                    actor="user",
                    related_events=[acceptance_event_id],
                    payload={"risks": risks, "verbatim": verbatim},
                )
            )
        events.append(
            build_queued_audit_event(
                event_key=anchor,
                kind="interaction",
                event="stage-approved",
                stage=stage,
                actor="user",
                related_events=[acceptance_event_id],
                related_event_keys=[risk_key] if risks else [],
                payload={
                    "action": action,
                    "verbatim": verbatim,
                    "artifact_path": artifact_path,
                    "artifact_sha256": artifact_sha256,
                    "review_sha256": review_sha256,
                    "commit_message": commit_message,
                },
            )
        )
        if action == "continue-and-commit":
            updated["checkpoint_commit"] = {
                "stage": stage,
                "action": action,
                "commit_message": commit_message,
                "selection_event_key": anchor,
                "artifact_path": artifact_path,
                "artifact_sha256": artifact_sha256,
                "review_sha256": review_sha256,
                "accepted_risks": risks,
                "verbatim": verbatim,
            }
        else:
            updated = _apply_approved_checkpoint(
                updated, stage, artifact_sha256, review_sha256, risks
            )
            if stage == "plan":
                events.append(
                    build_queued_audit_event(
                        event_key=f"workflow/{specification}/completed",
                        kind="lifecycle",
                        event="workflow-completed",
                        stage="workflow",
                        actor="router",
                        related_event_keys=[anchor],
                        payload={"plan_path": artifact_path, "plan_sha256": artifact_sha256},
                    )
                )
            else:
                events.append(
                    build_queued_audit_event(
                        event_key=(
                            f"spec/{specification}/stage/{updated['stage']}/entered/"
                            f"after-{stage}-approval"
                        ),
                        kind="lifecycle",
                        event="stage-entered",
                        stage=str(updated["stage"]),
                        actor="router",
                        related_event_keys=[anchor],
                        payload={"from_stage": stage, "reason": "approval"},
                    )
                )

    elif handler == "checkpoint-outcome":
        required = (
            {"selection_event_key"}
            if transition == "checkpoint-commit-succeeded"
            else {"selection_event_key", "attempt_id", "error"}
        )
        outcome = require_object(details, "checkpoint commit outcome", required)
        checkpoint = state["checkpoint_commit"]
        if not isinstance(checkpoint, dict):
            raise ValueError("checkpoint commit outcome requires a pending selection")
        if outcome["selection_event_key"] != checkpoint["selection_event_key"]:
            raise ValueError("checkpoint commit outcome does not match its selection")
        selection = _audit_event_by_key(durable_events, str(checkpoint["selection_event_key"]))
        if selection is None or selection["event"] != "stage-approved":
            raise ValueError("checkpoint commit selection is not durable")
        stage = str(checkpoint["stage"])
        event_name = (
            "checkpoint-commit-completed"
            if transition == "checkpoint-commit-succeeded"
            else "checkpoint-commit-failed"
        )
        payload = {
            "commit_message": checkpoint["commit_message"],
            "audit_event_id": selection["event_id"],
        }
        if transition == "checkpoint-commit-failed":
            payload["error"] = require_bounded_text(outcome["error"], "commit error")
        events.append(
            build_queued_audit_event(
                event_key=anchor,
                kind="lifecycle",
                event=event_name,
                stage=stage,
                actor="router",
                related_events=[selection["event_id"]],
                payload=payload,
            )
        )
        if transition == "checkpoint-commit-succeeded":
            updated = _apply_approved_checkpoint(
                updated,
                stage,
                str(checkpoint["artifact_sha256"]),
                checkpoint["review_sha256"],
                list(checkpoint["accepted_risks"]),
            )
            if stage == "plan":
                events.append(
                    build_queued_audit_event(
                        event_key=f"workflow/{specification}/completed",
                        kind="lifecycle",
                        event="workflow-completed",
                        stage="workflow",
                        actor="router",
                        related_event_keys=[anchor],
                        payload={
                            "plan_path": checkpoint["artifact_path"],
                            "plan_sha256": checkpoint["artifact_sha256"],
                        },
                    )
                )
            else:
                events.append(
                    build_queued_audit_event(
                        event_key=(
                            f"spec/{specification}/stage/{updated['stage']}/entered/"
                            f"after-{stage}-commit"
                        ),
                        kind="lifecycle",
                        event="stage-entered",
                        stage=str(updated["stage"]),
                        actor="router",
                        related_event_keys=[anchor],
                        payload={"from_stage": stage, "reason": "approval"},
                    )
                )

    elif handler == "artifact-question":
        question = require_object(
            details, "artifact question", {"operation_id", "question", "answer"}
        )
        if state["status"] != "awaiting-approval" or state["pending"] is not None:
            raise ValueError("artifact question requires an approval checkpoint")
        question_text = require_bounded_text(question["question"], "artifact question")
        answer_text = require_bounded_text(question["answer"], "artifact answer")
        answer_key = f"{anchor}/answer"
        events.extend(
            [
                build_queued_audit_event(
                    event_key=anchor,
                    kind="interaction",
                    event="user-question",
                    stage=str(state["stage"]),
                    actor="user",
                    payload={"verbatim": question_text},
                ),
                build_queued_audit_event(
                    event_key=answer_key,
                    kind="interaction",
                    event="agent-answer",
                    stage=str(state["stage"]),
                    actor="router",
                    related_event_keys=[anchor],
                    payload={"answer": answer_text},
                ),
            ]
        )

    elif handler == "flow-stop":
        stop = require_object(details, "flow stop", {"operation_id", "verbatim"})
        verbatim = stop["verbatim"]
        if verbatim is not None:
            require_bounded_text(verbatim, "stop verbatim")
        events.append(
            build_queued_audit_event(
                event_key=anchor,
                kind="interaction",
                event="flow-stopped",
                stage=str(state["stage"]),
                actor="user",
                payload={"verbatim": verbatim},
            )
        )

    elif handler == "flow-resume":
        resume = require_object(
            details,
            "workflow resume",
            {"operation_id", "choice", "verbatim", "reason"},
        )
        choice = require_bounded_text(resume["choice"], "recovery choice")
        reason = require_bounded_text(resume["reason"], "resume reason")
        verbatim = resume["verbatim"]
        if verbatim is not None:
            require_bounded_text(verbatim, "recovery verbatim")
        if transition == "flow-resume":
            if choice != "continue":
                raise ValueError("flow resume choice must be continue")
            if not any(event["event"] == "flow-stopped" for event in durable_events):
                raise ValueError("flow resume has no durable stop evidence")
        else:
            if choice not in {"continue-existing", "abandon-and-retry"}:
                raise ValueError("invalid native recovery choice")
            active_run = state["active_run"]
            if not isinstance(active_run, dict) or active_run["adapter"] == "mailbox":
                raise ValueError("native recovery requires an active native run")
            interruption = _audit_event_by_key(
                durable_events, f"run/{active_run['run_id']}/interrupted"
            )
            if interruption is None:
                raise ValueError("native recovery has no durable interruption evidence")
            if choice == "abandon-and-retry":
                updated["active_run"] = None
        resumed_key = f"workflow/{specification}/resume/{resume['operation_id']}"
        events.extend(
            [
                build_queued_audit_event(
                    event_key=anchor,
                    kind="interaction",
                    event="recovery-selected",
                    stage="workflow",
                    actor="user",
                    payload={"choice": choice, "verbatim": verbatim},
                ),
                build_queued_audit_event(
                    event_key=resumed_key,
                    kind="lifecycle",
                    event="workflow-resumed",
                    stage="workflow",
                    actor="router",
                    related_event_keys=[anchor],
                    payload={"reason": reason},
                ),
            ]
        )

    elif handler == "executor-wait":
        if transition == "mailbox-wait-started":
            wait = require_object(
                details, "mailbox wait", {"run_id", "wait_id", "wait_seconds"}
            )
        else:
            wait = require_object(
                details, "mailbox wait outcome", {"run_id", "wait_id", "detail"}
            )
        active_run = state["active_run"]
        if (
            not isinstance(active_run, dict)
            or active_run["adapter"] != "mailbox"
            or wait["run_id"] != active_run["run_id"]
        ):
            raise ValueError("mailbox transition does not match the active mailbox run")
        if transition == "mailbox-wait-started":
            if state["status"] not in {_active_run_status(active_run), "waiting-executor"}:
                raise ValueError("mailbox wait cannot start from current status")
            seconds = wait["wait_seconds"]
            if type(seconds) is not int or seconds < 0:
                raise ValueError("mailbox wait seconds must be non-negative")
            updated["status"] = "waiting-executor"
            events.append(
                build_queued_audit_event(
                    event_key=anchor,
                    kind="lifecycle",
                    event="executor-wait-started",
                    stage=str(active_run["stage"]),
                    actor="router",
                    run_id=str(active_run["run_id"]),
                    payload={"wait_seconds": seconds},
                )
            )
        else:
            if state["status"] != "waiting-executor":
                raise ValueError("mailbox wait outcome requires waiting-executor")
            started_key = anchor.removesuffix("/ended") + "/started"
            started = _audit_event_by_key(durable_events, started_key)
            if started is None or started["event"] != "executor-wait-started":
                raise ValueError("mailbox wait outcome has no matching durable start")
            outcome_name = {
                "mailbox-wait-completed": "completed",
                "mailbox-wait-timeout": "timeout",
                "mailbox-response-malformed": "malformed-response",
                "mailbox-wait-interrupted": "interrupted",
            }[transition]
            detail = wait["detail"]
            if detail is not None:
                require_bounded_text(detail, "mailbox wait detail")
            if outcome_name != "timeout":
                updated["status"] = _active_run_status(active_run)
            events.append(
                build_queued_audit_event(
                    event_key=anchor,
                    kind="lifecycle",
                    event="executor-wait-ended",
                    stage=str(active_run["stage"]),
                    actor="router",
                    run_id=str(active_run["run_id"]),
                    related_events=[started["event_id"]],
                    payload={"outcome": outcome_name, "detail": detail},
                )
            )

    elif handler == "native-interruption":
        interruption = require_object(
            details, "native interruption", {"run_id", "reason"}
        )
        active_run = state["active_run"]
        if (
            not isinstance(active_run, dict)
            or active_run["adapter"] == "mailbox"
            or interruption["run_id"] != active_run["run_id"]
        ):
            raise ValueError("native interruption does not match the active run")
        reason = require_bounded_text(interruption["reason"], "interruption reason")
        reservation = _audit_event_by_key(
            durable_events, f"run/{active_run['run_id']}/reserved"
        )
        if reservation is None:
            raise ValueError("native interruption has no durable reservation")
        events.append(
            build_queued_audit_event(
                event_key=anchor,
                kind="lifecycle",
                event="role-run-interrupted",
                stage=str(active_run["stage"]),
                actor="router",
                run_id=str(active_run["run_id"]),
                related_events=[reservation["event_id"]],
                payload={"reason": reason},
            )
        )

    elif handler == "upstream-invalidation":
        invalidation = require_object(
            details,
            "upstream invalidation",
            {"operation_id", "to_stage", "reason", "verbatim"},
        )
        if state["active_run"] is not None or state["checkpoint_commit"] is not None:
            raise ValueError("upstream invalidation requires an idle checkpoint")
        from_stage = str(state["stage"])
        to_stage = require_non_empty_string(invalidation["to_stage"], "return stage")
        if to_stage not in STAGES or STAGES.index(to_stage) >= STAGES.index(from_stage):
            raise ValueError("upstream invalidation must return to an earlier stage")
        reason = require_bounded_text(invalidation["reason"], "invalidation reason")
        verbatim = invalidation["verbatim"]
        if verbatim is not None:
            require_bounded_text(verbatim, "invalidation verbatim")
            events.append(
                build_queued_audit_event(
                    event_key=anchor,
                    kind="interaction",
                    event="revision-feedback-submitted",
                    stage=from_stage,
                    actor="user",
                    payload={"verbatim": verbatim},
                )
            )
        invalidated_key = f"workflow/{specification}/invalidation/{invalidation['operation_id']}"
        entered_key = f"spec/{specification}/stage/{to_stage}/entered/{invalidation['operation_id']}"
        events.extend(
            [
                build_queued_audit_event(
                    event_key=invalidated_key,
                    kind="lifecycle",
                    event="upstream-invalidated",
                    stage=from_stage,
                    actor="router",
                    related_event_keys=[anchor] if verbatim is not None else [],
                    payload={"from_stage": from_stage, "to_stage": to_stage, "reason": reason},
                ),
                build_queued_audit_event(
                    event_key=entered_key,
                    kind="lifecycle",
                    event="stage-entered",
                    stage=to_stage,
                    actor="router",
                    related_event_keys=[invalidated_key],
                    payload={"from_stage": from_stage, "reason": "upstream-return"},
                ),
            ]
        )
        updated["approvals"] = {
            stage: approval
            for stage, approval in updated["approvals"].items()
            if STAGES.index(stage) < STAGES.index(to_stage)
        }
        updated["stage"] = to_stage
        updated["status"] = "revising"
        updated["pending"] = None
        updated["automatic_revision_attempts"] = 0

    elif handler == "trusted-integrity-pause":
        failure = require_object(
            details,
            "trusted integrity pause",
            {"operation_id", "problem", "affected_paths", "question"},
        )
        problem = require_bounded_text(failure["problem"], "integrity problem")
        paths = validate_audit_string_list(
            failure["affected_paths"],
            "integrity affected paths",
            AUDIT_MAX_REFERENCES_PER_DECISION,
            allow_empty=False,
            paths=True,
        )
        question = require_bounded_text(failure["question"], "integrity question")
        failure_key = f"workflow/{specification}/integrity/{failure['operation_id']}"
        events.extend(
            [
                build_queued_audit_event(
                    event_key=failure_key,
                    kind="lifecycle",
                    event="workflow-integrity-failure",
                    stage=str(state["stage"]),
                    actor="router",
                    payload={"problem": problem, "affected_paths": paths},
                ),
                build_queued_audit_event(
                    event_key=anchor,
                    kind="interaction",
                    event="blocking-question",
                    stage=str(state["stage"]),
                    actor="router",
                    related_event_keys=[failure_key],
                    payload={
                        "question": question,
                        "origin": "router",
                        "resume_purpose": "revise",
                    },
                ),
            ]
        )
        updated["active_run"] = None
        updated["checkpoint_commit"] = None
        updated["status"] = "awaiting-decision"
        updated["pending"] = {
            "kind": "clarification",
            "origin": "router",
            "stage": state["stage"],
            "request": question,
            "response": None,
            "resume_purpose": "revise",
        }
    else:
        raise ValueError("unsupported workflow transition")

    validate_transition_event_batch(transition, events)
    updated = queue_audit_transaction(updated, specification, events)
    return updated, events, anchor, observed


def apply_workflow_transition_in_state(
    state_path: Path,
    project_root: Path,
    specification: str,
    transition: str,
    details_value: object,
) -> dict[str, object]:
    definition = FEATURE_TRANSITION_TABLE.get(transition)
    if definition is None:
        raise ValueError("unknown workflow transition")
    if definition["mode"] == "pre-state-observation":
        raise ValueError("pre-state outcome does not accept a state path")
    project_root = canonical_project_root(project_root)
    if transition == "status":
        # Status itself is intentionally unlogged, but it may complete a valid
        # interrupted transaction before observing product state.
        flush_audit_outbox(state_path, project_root, specification)
    state_content, state = validate_state_path(state_path, project_root, specification)
    if definition["mode"] == "observation":
        require_object(details_value, "workflow observation", set())
        return {
            "schema_version": 1,
            "transition": transition,
            "stage": state["stage"],
            "status": state["status"],
            "pending": state["pending"] is not None,
        }
    if definition["mode"] == "hard-stop":
        if transition == "untrusted-audit-integrity-failure":
            raise ValueError("untrusted audit integrity requires recovery outside the flow")
        require_object(details_value, "hard-stop observation", set())
        return {"schema_version": 1, "transition": transition, "status": "preserved"}
    if definition["mode"] != "workflow-transition":
        raise ValueError("transition is implemented by its specialized command")
    require_empty_audit_outbox(state, transition)
    log_path, log_bytes, log_stat, durable_events = _audit_events_for_state(
        project_root, state
    )
    details = details_value if isinstance(details_value, dict) else {}
    anchor_key = _transition_anchor_key(transition, specification, details)
    durable_anchor = _audit_event_by_key(durable_events, anchor_key)
    if durable_anchor is not None:
        _validate_replayed_transition(transition, details, durable_anchor)
        return {"schema_version": 1, "transition": transition, "status": "already-applied"}
    updated, _, prepared_anchor, observed_paths = _prepare_workflow_transition(
        transition,
        details_value,
        state,
        specification,
        project_root,
        durable_events,
    )
    if prepared_anchor != anchor_key:
        raise AssertionError("workflow transition anchor changed while preparing")
    initial_bytes = state_path.read_bytes()
    initial_stat = state_path.stat()
    if initial_bytes != state_content.encode("utf-8"):
        raise ValueError("state changed while preparing workflow transition")
    observed = {path: (path.read_bytes(), path.stat()) for path in observed_paths}

    def verify_transition_inputs() -> None:
        _assert_file_unchanged(
            log_path, log_bytes, log_stat, "applying workflow transition"
        )
        for path, (content, stat) in observed.items():
            _assert_file_unchanged(
                path, content, stat, "applying workflow transition"
            )

    _atomic_replace_bytes(
        state_path,
        render_canonical_yaml_mapping(updated),
        expected_bytes=initial_bytes,
        expected_stat=initial_stat,
        operation=f"applying workflow transition {transition}",
        _before_replace=verify_transition_inputs,
    )
    _, persisted = validate_state_path(state_path, project_root, specification)
    if persisted != updated:
        raise ValueError("persisted workflow transition does not match its prepared state")
    return {"schema_version": 1, "transition": transition, "status": "queued"}


def _run_git(
    project_root: Path,
    arguments: list[str],
    *,
    index_path: Path | None = None,
) -> subprocess.CompletedProcess[bytes]:
    environment = os.environ.copy()
    environment.update(
        {
            "GIT_EDITOR": "true",
            "GIT_OPTIONAL_LOCKS": "0",
            "GIT_TERMINAL_PROMPT": "0",
            "LC_ALL": "C",
            "LANG": "C",
        }
    )
    if index_path is not None:
        environment["GIT_INDEX_FILE"] = str(index_path)
    result = subprocess.run(
        ["git", "-C", str(project_root), *arguments],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=environment,
        check=False,
    )
    if len(result.stdout) > CONTROL_FILE_MAX_BYTES or len(result.stderr) > CONTROL_FILE_MAX_BYTES:
        raise ValueError("Git command output exceeds the control-file limit")
    return result


def _checked_git_output(
    project_root: Path,
    arguments: list[str],
    operation: str,
    *,
    index_path: Path | None = None,
) -> str:
    result = _run_git(project_root, arguments, index_path=index_path)
    if result.returncode != 0:
        raise ValueError(f"Git failed while {operation}")
    try:
        return result.stdout.decode("utf-8").strip()
    except UnicodeError as error:
        raise ValueError(f"Git returned non-UTF-8 output while {operation}") from error


def _checkpoint_git_repository(project_root: Path) -> tuple[str | None, Path]:
    root = _checked_git_output(
        project_root, ["rev-parse", "--show-toplevel"], "locating the repository"
    )
    try:
        repository_root = Path(root).resolve(strict=True)
    except OSError as error:
        raise ValueError("Git returned an invalid repository root") from error
    if repository_root != project_root:
        raise ValueError("checkpoint commit project root must be the Git worktree root")
    if _checked_git_output(
        project_root, ["rev-parse", "--is-bare-repository"], "checking the repository"
    ) != "false":
        raise ValueError("checkpoint commit requires a non-bare Git worktree")
    head_result = _run_git(project_root, ["rev-parse", "--verify", "HEAD"])
    if head_result.returncode == 0:
        try:
            head = head_result.stdout.decode("ascii").strip()
        except UnicodeError as error:
            raise ValueError("Git returned an invalid HEAD identity") from error
        if not re.fullmatch(r"[0-9a-f]{40,64}", head):
            raise ValueError("Git returned an invalid HEAD identity")
    else:
        symbolic = _run_git(project_root, ["symbolic-ref", "-q", "HEAD"])
        if symbolic.returncode != 0:
            raise ValueError("checkpoint commit requires a valid Git HEAD")
        head = None
    index_name = _checked_git_output(
        project_root, ["rev-parse", "--git-path", "index"], "locating the Git index"
    )
    index_path = Path(index_name)
    if not index_path.is_absolute():
        index_path = project_root / index_path
    index_path = index_path.resolve(strict=False)
    if index_path.is_symlink() or not index_path.is_file():
        raise ValueError("checkpoint commit requires a regular existing Git index")
    return head, index_path


def _git_index_snapshot(index_path: Path) -> tuple[bytes, os.stat_result]:
    if index_path.is_symlink() or not index_path.is_file():
        raise ValueError("the real Git index is unavailable")
    content = index_path.read_bytes()
    if len(content) > PROJECT_INPUT_MAX_BYTES:
        raise ValueError("the real Git index exceeds the safety limit")
    return content, index_path.stat()


def _restore_router_staging(
    index_path: Path,
    original_bytes: bytes,
    staged_bytes: bytes,
    staged_stat: os.stat_result,
) -> bool:
    try:
        _assert_file_unchanged(
            index_path,
            staged_bytes,
            staged_stat,
            "checking router-created Git staging",
        )
    except ValueError:
        return False
    _atomic_replace_bytes(
        index_path,
        original_bytes,
        expected_bytes=staged_bytes,
        expected_stat=staged_stat,
        operation="removing router-created Git staging after failure",
    )
    return True


def _staged_semantic_snapshot(
    project_root: Path,
    *,
    exclude_scope: str | None = None,
    index_path: Path | None = None,
) -> bytes:
    pathspecs = [":(top)**"]
    if exclude_scope is not None:
        pathspecs.append(f":(exclude,top){exclude_scope}")
    result = _run_git(
        project_root,
        [
            "diff",
            "--cached",
            "--binary",
            "--full-index",
            "--no-ext-diff",
            "--no-renames",
            "--",
            *pathspecs,
        ],
        index_path=index_path,
    )
    if result.returncode != 0:
        raise ValueError("Git could not inspect the staged changes")
    if len(result.stdout) > PROJECT_INPUT_MAX_BYTES:
        raise ValueError("staged changes exceed the safety limit")
    return result.stdout


def _scope_has_staged_changes(project_root: Path, scope: str) -> bool:
    result = _run_git(project_root, ["diff", "--cached", "--quiet", "--", scope])
    if result.returncode not in {0, 1}:
        raise ValueError("Git could not inspect staged specification changes")
    return result.returncode == 1


def _current_git_head(project_root: Path) -> str | None:
    result = _run_git(project_root, ["rev-parse", "--verify", "HEAD"])
    if result.returncode != 0:
        return None
    try:
        head = result.stdout.decode("ascii").strip()
    except UnicodeError as error:
        raise ValueError("Git returned an invalid HEAD identity") from error
    if not re.fullmatch(r"[0-9a-f]{40,64}", head):
        raise ValueError("Git returned an invalid HEAD identity")
    return head


def _prepare_checkpoint_tree(
    project_root: Path,
    head: str | None,
    scope: str,
    index_path: Path,
) -> str:
    if head is None:
        _checked_git_output(
            project_root,
            ["read-tree", "--empty"],
            "initializing the private checkpoint index",
            index_path=index_path,
        )
    else:
        _checked_git_output(
            project_root,
            ["read-tree", head],
            "initializing the private checkpoint index",
            index_path=index_path,
        )
    _checked_git_output(
        project_root,
        ["add", "-A", "--", scope],
        "staging the specification in the private checkpoint index",
        index_path=index_path,
    )
    tree = _checked_git_output(
        project_root,
        ["write-tree"],
        "writing the private checkpoint tree",
        index_path=index_path,
    )
    if not re.fullmatch(r"[0-9a-f]{40,64}", tree):
        raise ValueError("Git returned an invalid checkpoint tree identity")
    return tree


def _checkpoint_commit_message(
    project_root: Path, commit: str
) -> str:
    result = _run_git(project_root, ["show", "-s", "--format=%B", commit])
    if result.returncode != 0:
        raise ValueError("Git could not read the checkpoint commit message")
    try:
        return result.stdout.decode("utf-8").replace("\r\n", "\n")
    except UnicodeError as error:
        raise ValueError("checkpoint commit message is not UTF-8") from error


def _checkpoint_message_has_link(message: str, subject: str, event_id: str) -> bool:
    normalized = message.rstrip("\n")
    if not normalized or normalized.split("\n", 1)[0] != subject or "\r" in normalized:
        return False
    final_block = normalized.rsplit("\n\n", 1)[-1].split("\n")
    links = []
    for line in final_block:
        match = re.fullmatch(r"Stepan-Audit-Event:[ \t]*(MEM-[0-9]{6,})[ \t]*", line)
        if match is not None:
            links.append(match.group(1))
    return links == [event_id]


def _linked_checkpoint_commits(
    project_root: Path, subject: str, event_id: str
) -> list[str]:
    trailer = f"Stepan-Audit-Event: {event_id}"
    result = _run_git(
        project_root,
        [
            "log",
            "--all",
            "--fixed-strings",
            f"--grep={trailer}",
            "--format=%H",
            "--max-count=257",
        ],
    )
    if result.returncode != 0:
        raise ValueError("Git could not search checkpoint commit history")
    try:
        candidates = [line for line in result.stdout.decode("ascii").splitlines() if line]
    except UnicodeError as error:
        raise ValueError("Git returned invalid checkpoint history") from error
    if len(candidates) > 256:
        raise ValueError("too many checkpoint linkage candidates")
    matching = [
        commit
        for commit in candidates
        if _checkpoint_message_has_link(
            _checkpoint_commit_message(project_root, commit), subject, event_id
        )
    ]
    if len(set(matching)) != len(matching):
        raise ValueError("Git returned duplicate checkpoint commit candidates")
    return matching


def _verify_checkpoint_commit(
    project_root: Path,
    commit: str,
    *,
    expected_parent: str | None,
    expected_tree: str,
    subject: str,
    event_id: str,
) -> None:
    if _current_git_head(project_root) != commit:
        raise ValueError("checkpoint commit is not the current Git HEAD")
    tree = _checked_git_output(
        project_root,
        ["show", "-s", "--format=%T", commit],
        "verifying checkpoint commit contents",
    )
    if tree != expected_tree:
        raise ValueError("checkpoint commit does not contain the exact specification tree")
    parents = _checked_git_output(
        project_root,
        ["show", "-s", "--format=%P", commit],
        "verifying checkpoint commit parent",
    ).split()
    expected_parents = [] if expected_parent is None else [expected_parent]
    if parents != expected_parents:
        raise ValueError("Git HEAD changed while creating the checkpoint commit")
    if not _checkpoint_message_has_link(
        _checkpoint_commit_message(project_root, commit), subject, event_id
    ):
        raise ValueError("checkpoint commit message or audit trailer is invalid")


def _checkpoint_attempt_id(
    durable_events: list[dict[str, object]], selection_event_key: str
) -> str:
    prefix = f"{selection_event_key}/commit-failed/attempt-"
    attempts: list[int] = []
    for event in durable_events:
        key = str(event["event_key"])
        if not key.startswith(prefix):
            continue
        suffix = key.removeprefix(prefix)
        if not re.fullmatch(r"[1-9][0-9]*", suffix):
            raise ValueError("durable checkpoint failure has an invalid attempt identity")
        attempts.append(int(suffix))
    if attempts and sorted(attempts) != list(range(1, max(attempts) + 1)):
        raise ValueError("durable checkpoint failure attempts are not consecutive")
    return f"attempt-{max(attempts, default=0) + 1}"


def _flush_checkpoint_outcome_if_pending(
    state_path: Path, project_root: Path, specification: str
) -> str | None:
    _, state = validate_state_path(
        state_path,
        project_root,
        specification,
        _allow_pending_audit_suffix=True,
    )
    outbox = state["audit"]["outbox"]
    if outbox is None:
        return None
    event_names = {str(event["event"]) for event in outbox["events"]}
    if "checkpoint-commit-completed" in event_names:
        outcome = "completed"
    elif "checkpoint-commit-failed" in event_names:
        outcome = "failed"
    else:
        raise ValueError("checkpoint commit is blocked by an unrelated audit outbox")
    flush_audit_outbox(state_path, project_root, specification)
    return outcome


def _record_checkpoint_commit_failure(
    state_path: Path,
    project_root: Path,
    specification: str,
    selection_event_key: str,
    error: str,
) -> dict[str, object]:
    _, trusted = validate_state_path(state_path, project_root, specification)
    require_empty_audit_outbox(trusted, "checkpoint commit failure recording")
    checkpoint = trusted["checkpoint_commit"]
    if not isinstance(checkpoint, dict) or checkpoint["selection_event_key"] != selection_event_key:
        raise ValueError("checkpoint intent changed before commit failure could be recorded")
    _, _, _, events = _audit_events_for_state(project_root, trusted)
    attempt_id = _checkpoint_attempt_id(events, selection_event_key)
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "checkpoint-commit-failed",
        {
            "selection_event_key": selection_event_key,
            "attempt_id": attempt_id,
            "error": error,
        },
    )
    flush_audit_outbox(state_path, project_root, specification)
    return {"schema_version": 1, "status": "failed", "error": error}


def _complete_checkpoint_commit(
    state_path: Path,
    project_root: Path,
    specification: str,
    selection_event_key: str,
    commit: str,
    status: str,
) -> dict[str, object]:
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "checkpoint-commit-succeeded",
        {"selection_event_key": selection_event_key},
    )
    flush_audit_outbox(state_path, project_root, specification)
    return {
        "schema_version": 1,
        "status": status,
        "commit": commit,
    }


def checkpoint_commit_in_state(
    state_path: Path,
    project_root: Path,
    specification: str,
    *,
    _after_commit: Callable[[str], None] | None = None,
) -> dict[str, object]:
    project_root = canonical_project_root(project_root)
    pending_outcome = _flush_checkpoint_outcome_if_pending(
        state_path, project_root, specification
    )
    if pending_outcome == "completed":
        return {"schema_version": 1, "status": "recovered-outcome"}
    if pending_outcome == "failed":
        return {"schema_version": 1, "status": "recovered-failure"}

    state_content, state = validate_state_path(state_path, project_root, specification)
    require_empty_audit_outbox(state, "checkpoint commit")
    checkpoint = state["checkpoint_commit"]
    if not isinstance(checkpoint, dict):
        raise ValueError("state has no pending checkpoint commit")
    selection_event_key = str(checkpoint["selection_event_key"])
    _, _, _, durable_events = _audit_events_for_state(project_root, state)
    selection = _audit_event_by_key(durable_events, selection_event_key)
    if selection is None or selection["event"] != "stage-approved":
        raise ValueError("checkpoint commit selection event is not durable")
    event_id = str(selection["event_id"])
    subject = str(checkpoint["commit_message"])
    scope = f"docs/changes/specs/{specification}"
    specification_root = project_root.joinpath(*PurePosixPath(scope).parts)
    if specification_root.is_symlink() or not specification_root.is_dir():
        raise ValueError("checkpoint specification directory is unavailable")

    head_before, real_index_path = _checkpoint_git_repository(project_root)
    if _scope_has_staged_changes(project_root, scope):
        raise ValueError(
            "checkpoint commit refuses pre-existing staged changes inside the specification"
        )
    staged_outside_before = _staged_semantic_snapshot(
        project_root, exclude_scope=scope
    )
    staged_all_before = _staged_semantic_snapshot(project_root)
    real_index_before, _ = _git_index_snapshot(real_index_path)
    initial_state_bytes = state_path.read_bytes()
    if initial_state_bytes != state_content.encode("utf-8"):
        raise ValueError("state changed while preparing checkpoint commit")

    with TemporaryDirectory(prefix="stepan-checkpoint-index-") as temporary_name:
        private_index = Path(temporary_name) / "index"
        try:
            expected_tree = _prepare_checkpoint_tree(
                project_root, head_before, scope, private_index
            )
        except ValueError:
            return _record_checkpoint_commit_failure(
                state_path,
                project_root,
                specification,
                selection_event_key,
                "Git could not prepare the isolated specification tree.",
            )
        if _staged_semantic_snapshot(project_root) != staged_all_before:
            return _record_checkpoint_commit_failure(
                state_path,
                project_root,
                specification,
                selection_event_key,
                "The Git index changed while preparing the checkpoint commit.",
            )
        linked = _linked_checkpoint_commits(project_root, subject, event_id)
        if linked:
            if len(linked) != 1 or linked[0] != head_before:
                raise ValueError(
                    "checkpoint-linked commit exists in history but is not the unique current HEAD"
                )
            recovered_parents = _checked_git_output(
                project_root,
                ["show", "-s", "--format=%P", linked[0]],
                "reading recovered checkpoint parent",
            ).split()
            if len(recovered_parents) > 1:
                raise ValueError("checkpoint-linked HEAD must not be a merge commit")
            _verify_checkpoint_commit(
                project_root,
                linked[0],
                expected_parent=recovered_parents[0] if recovered_parents else None,
                expected_tree=expected_tree,
                subject=subject,
                event_id=event_id,
            )
            if (
                _staged_semantic_snapshot(project_root, exclude_scope=scope)
                != staged_outside_before
                or _scope_has_staged_changes(project_root, scope)
            ):
                raise ValueError("the Git index changed during checkpoint recovery")
            recovered_content, recovered_state = validate_state_path(
                state_path, project_root, specification
            )
            if recovered_content != state_content or recovered_state != state:
                raise ValueError("feature state changed during checkpoint recovery")
            return _complete_checkpoint_commit(
                state_path,
                project_root,
                specification,
                selection_event_key,
                linked[0],
                "recovered",
            )

        if _current_git_head(project_root) != head_before:
            raise ValueError("Git HEAD changed while preparing checkpoint commit")
        current_content, current_state = validate_state_path(
            state_path, project_root, specification
        )
        if current_content != state_content or current_state != state:
            raise ValueError("feature state changed while preparing checkpoint commit")
        stage_result = _run_git(project_root, ["add", "-A", "--", scope])
        if stage_result.returncode != 0:
            if _git_index_snapshot(real_index_path)[0] != real_index_before:
                return _record_checkpoint_commit_failure(
                    state_path,
                    project_root,
                    specification,
                    selection_event_key,
                    "The real Git index changed while staging the specification failed.",
                )
            return _record_checkpoint_commit_failure(
                state_path,
                project_root,
                specification,
                selection_event_key,
                f"Git specification staging failed with exit status {stage_result.returncode}.",
            )
        staged_index_bytes, staged_index_stat = _git_index_snapshot(real_index_path)
        if (
            _staged_semantic_snapshot(project_root, exclude_scope=scope)
            != staged_outside_before
        ):
            restored = _restore_router_staging(
                real_index_path,
                real_index_before,
                staged_index_bytes,
                staged_index_stat,
            )
            return _record_checkpoint_commit_failure(
                state_path,
                project_root,
                specification,
                selection_event_key,
                (
                    "Git changed staged paths outside the specification while preparing the commit."
                    if restored
                    else "The real Git index changed concurrently while preparing the commit."
                ),
            )
        if _current_git_head(project_root) != head_before:
            _restore_router_staging(
                real_index_path,
                real_index_before,
                staged_index_bytes,
                staged_index_stat,
            )
            raise ValueError("Git HEAD changed while staging the checkpoint commit")

        def fail_before_commit(error: str) -> dict[str, object]:
            restored = _restore_router_staging(
                real_index_path,
                real_index_before,
                staged_index_bytes,
                staged_index_stat,
            )
            return _record_checkpoint_commit_failure(
                state_path,
                project_root,
                specification,
                selection_event_key,
                (
                    error
                    if restored
                    else "A Git hook changed the real index while preparing the checkpoint commit."
                ),
            )

        def guarded_before_commit(operation: Callable[[], object]) -> object:
            try:
                return operation()
            except (OSError, UnicodeError, ValueError):
                _restore_router_staging(
                    real_index_path,
                    real_index_before,
                    staged_index_bytes,
                    staged_index_stat,
                )
                raise

        message_path = Path(temporary_name) / "commit-message.txt"
        message_path.write_bytes(
            (
                f"{subject}\n\nStepan-Audit-Event: {event_id}\n"
            ).encode("utf-8")
        )
        pre_commit = guarded_before_commit(
            lambda: _run_git(
                project_root,
                ["hook", "run", "--ignore-missing", "pre-commit"],
                index_path=private_index,
            )
        )
        assert isinstance(pre_commit, subprocess.CompletedProcess)
        if pre_commit.returncode != 0:
            return fail_before_commit(
                f"Git pre-commit hook failed with exit status {pre_commit.returncode}."
            )
        if guarded_before_commit(
            lambda: _staged_semantic_snapshot(
                project_root,
                exclude_scope=scope,
                index_path=private_index,
            )
        ):
            return fail_before_commit(
                "Git pre-commit hook staged a path outside the specification."
            )
        verification_index = Path(temporary_name) / "verification-index"
        hook_tree = guarded_before_commit(
            lambda: _prepare_checkpoint_tree(
                project_root, head_before, scope, verification_index
            )
        )
        if hook_tree != expected_tree:
            return fail_before_commit(
                "A Git hook changed the specification selected for the checkpoint."
            )
        prepare_message = guarded_before_commit(
            lambda: _run_git(
                project_root,
                [
                    "hook",
                    "run",
                    "--ignore-missing",
                    "prepare-commit-msg",
                    "--",
                    str(message_path),
                    "message",
                ],
                index_path=private_index,
            )
        )
        assert isinstance(prepare_message, subprocess.CompletedProcess)
        if prepare_message.returncode != 0:
            return fail_before_commit(
                f"Git prepare-commit-msg hook failed with exit status {prepare_message.returncode}."
            )
        commit_message_hook = guarded_before_commit(
            lambda: _run_git(
                project_root,
                [
                    "hook",
                    "run",
                    "--ignore-missing",
                    "commit-msg",
                    "--",
                    str(message_path),
                ],
                index_path=private_index,
            )
        )
        assert isinstance(commit_message_hook, subprocess.CompletedProcess)
        if commit_message_hook.returncode != 0:
            return fail_before_commit(
                f"Git commit-msg hook failed with exit status {commit_message_hook.returncode}."
            )
        message = guarded_before_commit(
            lambda: read_bounded_utf8(
                message_path, "checkpoint commit message", CONTROL_FILE_MAX_BYTES
            )
        )
        assert isinstance(message, str)
        if not _checkpoint_message_has_link(message, subject, event_id):
            return fail_before_commit(
                "Git commit hooks produced an invalid checkpoint subject or audit trailer."
            )
        final_hook_tree = guarded_before_commit(
            lambda: _prepare_checkpoint_tree(
                project_root, head_before, scope, verification_index
            )
        )
        if final_hook_tree != expected_tree:
            return fail_before_commit(
                "A Git commit-message hook changed the selected specification."
            )
        if guarded_before_commit(
            lambda: _staged_semantic_snapshot(
                project_root,
                exclude_scope=scope,
                index_path=private_index,
            )
        ):
            return fail_before_commit(
                "A Git commit-message hook staged a path outside the specification."
            )
        if guarded_before_commit(lambda: _current_git_head(project_root)) != head_before:
            return fail_before_commit(
                "Git HEAD changed while checkpoint commit hooks were running."
            )
        hook_state_value = guarded_before_commit(
            lambda: validate_state_path(state_path, project_root, specification)
        )
        assert isinstance(hook_state_value, tuple)
        hook_state_content, hook_state = hook_state_value
        if hook_state_content != state_content or hook_state != state:
            _restore_router_staging(
                real_index_path,
                real_index_before,
                staged_index_bytes,
                staged_index_stat,
            )
            raise ValueError("feature state changed while checkpoint commit hooks were running")
        empty_hooks = Path(temporary_name) / "empty-hooks"
        empty_hooks.mkdir()
        commit_result = _run_git(
            project_root,
            [
                "-c",
                f"core.hooksPath={empty_hooks}",
                "commit",
                "--quiet",
                "--only",
                "--allow-empty",
                "--no-verify",
                "--cleanup=verbatim",
                "--file",
                str(message_path),
                "--",
                scope,
            ],
        )
        if commit_result.returncode != 0:
            restored = _restore_router_staging(
                real_index_path,
                real_index_before,
                staged_index_bytes,
                staged_index_stat,
            )
            error = (
                "A Git hook changed the real index while the checkpoint commit failed."
                if not restored
                else f"Git checkpoint commit failed with exit status {commit_result.returncode}."
            )
            return _record_checkpoint_commit_failure(
                state_path,
                project_root,
                specification,
                selection_event_key,
                error,
            )
        commit = _current_git_head(project_root)
        if commit is None:
            return _record_checkpoint_commit_failure(
                state_path,
                project_root,
                specification,
                selection_event_key,
                "Git reported success without creating a checkpoint commit.",
            )
        post_commit_index = Path(temporary_name) / "post-commit-index"
        _checked_git_output(
            project_root,
            ["read-tree", commit],
            "preparing the post-commit hook index",
            index_path=post_commit_index,
        )
        _run_git(
            project_root,
            ["hook", "run", "--ignore-missing", "post-commit"],
            index_path=post_commit_index,
        )
        if _after_commit is not None:
            _after_commit(commit)
        try:
            _verify_checkpoint_commit(
                project_root,
                commit,
                expected_parent=head_before,
                expected_tree=expected_tree,
                subject=subject,
                event_id=event_id,
            )
        except ValueError as error:
            return _record_checkpoint_commit_failure(
                state_path,
                project_root,
                specification,
                selection_event_key,
                str(error),
            )
        if (
            _staged_semantic_snapshot(project_root, exclude_scope=scope)
            != staged_outside_before
            or _scope_has_staged_changes(project_root, scope)
        ):
            return _record_checkpoint_commit_failure(
                state_path,
                project_root,
                specification,
                selection_event_key,
                "The Git index changed while creating the checkpoint commit.",
            )
        final_content, final_state = validate_state_path(
            state_path, project_root, specification
        )
        if final_content != state_content or final_state != state:
            raise ValueError("feature state changed while creating checkpoint commit")
        linked_after = _linked_checkpoint_commits(project_root, subject, event_id)
        if linked_after != [commit]:
            raise ValueError("checkpoint commit linkage is not unique at Git HEAD")
        return _complete_checkpoint_commit(
            state_path,
            project_root,
            specification,
            selection_event_key,
            commit,
            "committed",
        )


def validate_role_manifest(
    value: object,
    skill_root: Path,
    project_root: Path,
    adapter_class: str,
    state: dict[str, object],
) -> dict[str, object]:
    require_empty_audit_outbox(state, "role dispatch")
    if adapter_class not in {"native", "mailbox"}:
        raise ValueError("invalid role manifest adapter class")
    required = {
        "schema_version",
        "run_id",
        "spec_id",
        "stage",
        "role",
        "purpose",
        "project_root",
        "skill_root",
        "brief",
        "resources",
        "inputs",
        "clarifications",
        "output",
        "allowed_write",
    }
    if adapter_class == "mailbox":
        required.add("executor")
    manifest = require_object(value, "role manifest", required, {"feedback"})
    if type(manifest["schema_version"]) is not int or manifest["schema_version"] != 2:
        raise ValueError("role manifest schema_version must be 2")
    specification = require_non_empty_string(manifest["spec_id"], "manifest spec_id")
    stage = require_non_empty_string(manifest["stage"], "manifest stage")
    role = require_non_empty_string(manifest["role"], "manifest role")
    purpose = require_non_empty_string(manifest["purpose"], "manifest purpose")
    if (
        not SPEC_ID_PATTERN.fullmatch(specification)
        or stage not in STAGES
        or role not in ROLES
        or purpose not in PURPOSES
    ):
        raise ValueError("invalid role manifest identity")
    run_id = require_non_empty_string(manifest["run_id"], "manifest run_id")
    match = re.fullmatch(
        rf"{re.escape(specification)}--{re.escape(stage)}--{re.escape(role)}--([1-9][0-9]*)",
        run_id,
    )
    if match is None:
        raise ValueError("role manifest run ID is inconsistent")
    active_run = state.get("active_run")
    if not isinstance(active_run, dict) or any(
        active_run[field] != manifest[field]
        for field in ("run_id", "stage", "role", "purpose", "output")
    ):
        raise ValueError("role manifest does not match the active run")
    active_is_mailbox = active_run["adapter"] == "mailbox"
    if (adapter_class == "mailbox") != active_is_mailbox:
        raise ValueError("role manifest adapter class does not match the active run")

    skill_root = skill_root.resolve(strict=True)
    project_root = canonical_project_root(project_root)
    if Path(require_non_empty_string(manifest["skill_root"], "manifest skill_root")) != skill_root:
        raise ValueError("role manifest skill root does not match")
    if Path(require_non_empty_string(manifest["project_root"], "manifest project_root")) != project_root:
        raise ValueError("role manifest project root does not match")
    resource_manifest = role_resource_manifest(skill_root, role)
    if manifest["brief"] != resource_manifest["brief"]:
        raise ValueError("role manifest brief does not match role")
    if manifest["resources"] != resource_manifest["resources"]:
        raise ValueError("role manifest resources do not match role brief order")

    inputs = manifest["inputs"]
    if not isinstance(inputs, list):
        raise ValueError("role manifest inputs must be a list")
    seen_inputs: set[str] = set()
    for value in inputs:
        item = require_object(value, "role manifest input", {"path", "sha256"})
        path = require_non_empty_string(item["path"], "role manifest input path")
        digest = require_non_empty_string(item["sha256"], "role manifest input hash")
        if path in seen_inputs or not HASH_PATTERN.fullmatch(digest):
            raise ValueError("invalid or duplicate role manifest input")
        seen_inputs.add(path)

    specification_root = f"docs/changes/specs/{specification}"
    expected_input_paths = expected_role_input_paths(
        state, specification, role, purpose
    )
    if [item["path"] for item in inputs] != expected_input_paths:
        raise ValueError("role manifest inputs do not match required and pinned inputs")
    expected_inputs = build_role_manifest_inputs(
        state, project_root, specification, role, purpose
    )
    for item, expected in zip(inputs, expected_inputs, strict=True):
        if item["sha256"] != expected["sha256"]:
            raise ValueError(
                f"role manifest input hash does not match: {item['path']}"
            )

    clarifications = manifest["clarifications"]
    if clarifications != state["clarifications"]:
        raise ValueError("role manifest clarification history does not match state")

    output = require_non_empty_string(manifest["output"], "role manifest output")
    if output != expected_output_path(specification, stage, role):
        raise ValueError("role manifest output does not match role and stage")
    if manifest["allowed_write"] != output:
        raise ValueError("role manifest allowed_write must equal output")

    feedback = manifest.get("feedback")
    if feedback is not None:
        feedback = require_object(
            feedback,
            "role manifest feedback",
            set(),
            {
                "previous_review",
                "unresolved_finding_ids",
                "pending_response",
                "revision_request",
            },
        )
        if not feedback:
            raise ValueError("role manifest feedback must not be empty")
        if "previous_review" in feedback:
            if stage not in {"requirements", "design"}:
                raise ValueError("only reviewed stages may receive previous review feedback")
            previous = require_object(
                feedback["previous_review"], "previous review", {"path", "sha256"}
            )
            path = require_non_empty_string(previous["path"], "previous review path")
            digest = require_non_empty_string(previous["sha256"], "previous review hash")
            expected_previous_path = f"{specification_root}/review/{stage}.yaml"
            if path != expected_previous_path:
                raise ValueError("previous review path does not match the active stage")
            resolved = resolve_project_file(
                project_root, path, "previous review", CONTROL_FILE_MAX_BYTES
            )
            if not HASH_PATTERN.fullmatch(digest) or file_hash(resolved) != digest:
                raise ValueError("previous review hash does not match")
            previous_review = validate_review_value(
                parse_strict_yaml(
                    read_bounded_utf8(
                        resolved, "previous review", CONTROL_FILE_MAX_BYTES
                    ),
                    "previous review",
                ),
                stage,
                None,
            )
            ids = feedback.get("unresolved_finding_ids")
            if not isinstance(ids, list) or any(
                not isinstance(identifier, str) or not identifier.strip() for identifier in ids
            ):
                raise ValueError("previous review requires unresolved finding IDs")
            expected_ids = [
                finding["id"]
                for finding in previous_review["findings"]
                if finding["severity"] == "blocking"
            ]
            if ids != expected_ids:
                raise ValueError(
                    "unresolved finding IDs must preserve previous blocking findings"
                )
        elif "unresolved_finding_ids" in feedback:
            raise ValueError("unresolved finding IDs require a previous review")
        for field in ("pending_response", "revision_request"):
            if field in feedback:
                require_non_empty_string(feedback[field], f"feedback {field}")
    pending = state["pending"]
    if pending is not None and pending["response"] is not None:
        if feedback is None or feedback.get("pending_response") != pending["response"]:
            raise ValueError("role manifest omits the persisted pending response")
        if pending["kind"] == "revision":
            if feedback.get("revision_request") != pending["response"]:
                raise ValueError("role manifest revision request does not match state")
        elif feedback is not None and "revision_request" in feedback:
            raise ValueError("role manifest has an unexpected revision request")
    elif feedback is not None and "revision_request" in feedback:
        raise ValueError("role manifest revision request has no primary state source")
    previous_review_required = purpose == "revise" and stage in {
        "requirements",
        "design",
    }
    if purpose == "review":
        existing_review = project_root.joinpath(*PurePosixPath(output).parts)
        previous_review_required = existing_review.is_file()
    if previous_review_required and (
        feedback is None or "previous_review" not in feedback
    ):
        raise ValueError("revision or re-review requires previous review feedback")

    if adapter_class == "mailbox":
        executor = require_object(
            manifest["executor"], "role manifest executor", {"model"}, {"reasoning"}
        )
        profile_name = state["execution"]["bindings"][role]
        profile = state["execution"]["profiles"][profile_name]
        if (
            require_non_empty_string(executor["model"], "role manifest executor model")
            != profile["model"]
        ):
            raise ValueError("role manifest executor model does not match the pinned profile")
        expected_reasoning = profile.get("reasoning")
        if expected_reasoning is None:
            if "reasoning" in executor:
                raise ValueError("role manifest executor has unexpected reasoning")
        elif (
            require_non_empty_string(
                executor.get("reasoning"), "role manifest executor reasoning"
            )
            != expected_reasoning
        ):
            raise ValueError(
                "role manifest executor reasoning does not match the pinned profile"
            )
    return manifest


def self_test_execution_snapshot() -> dict[str, object]:
    return {
        "source": "project",
        "config_sha256": "sha256:" + "c" * 64,
        "adapters": {"codex": {"kind": "codex"}},
        "profiles": {
            "orchestrator": {
                "adapter": "codex",
                "agent": "stepan_orchestrator",
                "project_inputs": [],
            },
            "author": {
                "adapter": "codex",
                "agent": "stepan_author",
                "project_inputs": [],
            },
        },
        "bindings": {
            "router": "orchestrator",
            "idea-author": "author",
            "requirements-author": "author",
            "requirements-reviewer": "author",
            "design-author": "author",
            "specification-reviewer": "author",
            "planner": "author",
        },
    }


def self_test_state_content(
    request_hash: str,
    idea_hash: str = "sha256:" + "d" * 64,
    audit: dict[str, object] | None = None,
) -> str:
    audit_value = audit or {
        "schema_version": 1,
        "log_path": "docs/changes/specs/export-data/mem-log.md",
        "log_size_bytes": 256,
        "log_sha256": "sha256:" + "a" * 64,
        "head_event_id": "MEM-000003",
        "head_event_sha256": "sha256:" + "b" * 64,
        "next_event_sequence": 4,
        "decision_index": {},
        "outbox": None,
    }
    state = {
        "schema_version": 1,
        "specification": "export-data",
        "created_at": "2026-08-15T12:00:00Z",
        "initial_request_sha256": request_hash,
        "stage": "requirements",
        "status": "drafting",
        "automatic_revision_attempts": 0,
        "next_run_sequence": 2,
        "approvals": {
            "idea": {
                "artifact_sha256": idea_hash,
                "review_sha256": None,
                "accepted_risks": [],
            }
        },
        "clarifications": [],
        "pending": None,
        "active_run": None,
        "checkpoint_commit": None,
        "audit": audit_value,
        "execution": self_test_execution_snapshot(),
    }
    return render_canonical_yaml_mapping(state).decode("utf-8")


def _self_test_expect_value_error(operation: Callable[[], object], message: str) -> None:
    try:
        operation()
    except ValueError:
        return
    raise AssertionError(message)


def self_test_feature_initialization(temporary_root: Path) -> None:
    project_root = temporary_root / "feature-initialization"
    project_root.mkdir()
    request = (
        "Экспорт истории\r\n"
        "с `кодом` и Markdown fence:\r\n"
        "```markdown\r\nпример\r\n```\r\n\r\n"
    )
    timestamp = "2026-08-23T12:34:56Z"
    execution = self_test_execution_snapshot()
    result = initialize_feature_specification(
        project_root,
        "unicode-export",
        request,
        execution,
        recorded_at=timestamp,
    )
    specification_root = project_root / "docs/changes/specs/unicode-export"
    request_path = specification_root / "request.md"
    log_path = specification_root / "mem-log.md"
    state_path = specification_root / "state.yaml"
    expected_request = canonicalize(request.encode("utf-8"))
    assert request_path.read_bytes() == expected_request
    assert result["created"] == [
        "docs/changes/specs/unicode-export/request.md",
        "docs/changes/specs/unicode-export/mem-log.md",
        "docs/changes/specs/unicode-export/state.yaml",
    ]
    _, state = validate_state_path(state_path, project_root, "unicode-export")
    assert state["audit"] == result["audit"]
    assert state_path.read_bytes() == render_canonical_yaml_mapping(state)
    parsed_log = validate_audit_log(log_path.read_bytes())
    assert [event["event"] for event in parsed_log["events"]] == [
        "initial-request-captured",
        "specification-created",
        "stage-entered",
    ]
    assert [event["event_id"] for event in parsed_log["events"]] == [
        "MEM-000001",
        "MEM-000002",
        "MEM-000003",
    ]
    assert parsed_log["events"][0]["payload"]["verbatim"].encode("utf-8") == expected_request
    assert state["audit"]["head_event_id"] == "MEM-000003"
    assert state["audit"]["next_event_sequence"] == 4
    assert state["audit"]["decision_index"] == {}
    assert state["audit"]["outbox"] is None

    original_state_bytes = state_path.read_bytes()
    original_log_bytes = log_path.read_bytes()

    def expect_invalid_state(candidate: dict[str, object], message: str) -> None:
        state_path.write_bytes(render_canonical_yaml_mapping(candidate))
        try:
            _self_test_expect_value_error(
                lambda: validate_state_path(
                    state_path, project_root, "unicode-export"
                ),
                message,
            )
        finally:
            state_path.write_bytes(original_state_bytes)

    wrong_request_hash = json.loads(json.dumps(state))
    wrong_request_hash["initial_request_sha256"] = "sha256:" + "f" * 64
    expect_invalid_state(wrong_request_hash, "accepted a wrong initial request hash")

    wrong_log_path = json.loads(json.dumps(state))
    wrong_log_path["audit"]["log_path"] = (
        "docs/changes/specs/unicode-export/other-log.md"
    )
    expect_invalid_state(wrong_log_path, "accepted a non-canonical audit log path")

    for field, invalid_value in (
        ("log_size_bytes", state["audit"]["log_size_bytes"] + 1),
        ("log_sha256", "sha256:" + "f" * 64),
        ("head_event_id", "MEM-000002"),
        ("head_event_sha256", "sha256:" + "f" * 64),
        ("next_event_sequence", 5),
    ):
        candidate = json.loads(json.dumps(state))
        candidate["audit"][field] = invalid_value
        expect_invalid_state(candidate, f"accepted mismatched audit {field}")

    wrong_index = json.loads(json.dumps(state))
    wrong_index["audit"]["decision_index"] = {
        "design-author": {
            "DES-001": {
                "event_id": "MEM-000003",
                "semantic_sha256": "sha256:" + "d" * 64,
            }
        }
    }
    expect_invalid_state(wrong_index, "accepted a decision index absent from the log")

    def state_for_log(candidate_log: bytes) -> dict[str, object]:
        log = validate_audit_log(candidate_log)
        candidate = json.loads(json.dumps(state))
        candidate["audit"].update(
            {
                "log_size_bytes": log["log_size_bytes"],
                "log_sha256": log["log_sha256"],
                "head_event_id": log["head_event_id"],
                "head_event_sha256": log["head_event_sha256"],
                "next_event_sequence": log["next_event_sequence"],
            }
        )
        return candidate

    wrong_header = dict(parsed_log["header"])
    wrong_header["request_sha256"] = "sha256:" + "f" * 64
    wrong_header_log = render_audit_log(wrong_header, parsed_log["events"])
    log_path.write_bytes(wrong_header_log)
    try:
        expect_invalid_state(
            state_for_log(wrong_header_log),
            "accepted an audit header with a wrong request hash",
        )
    finally:
        log_path.write_bytes(original_log_bytes)

    altered_events = json.loads(json.dumps(parsed_log["events"]))
    altered_events[0]["payload"]["verbatim"] = "Другой запрос.\n"
    previous_hash: str | None = None
    for sequence, event in enumerate(altered_events, 1):
        event["event_id"] = format_audit_event_id(sequence)
        event["previous_event_sha256"] = previous_hash
        event["event_sha256"] = AUDIT_EVENT_HASH_SENTINEL
        sealed = seal_audit_event(event)
        event.update(sealed)
        previous_hash = str(event["event_sha256"])
    altered_log = render_audit_log(parsed_log["header"], altered_events)
    log_path.write_bytes(altered_log)
    try:
        expect_invalid_state(
            state_for_log(altered_log),
            "accepted a first event whose request differs from request.md",
        )
    finally:
        log_path.write_bytes(original_log_bytes)

    request_path.write_bytes(expected_request.replace(b"\n", b"\r\n"))
    try:
        _self_test_expect_value_error(
            lambda: validate_state_path(state_path, project_root, "unicode-export"),
            "accepted non-canonical request.md newlines",
        )
    finally:
        request_path.write_bytes(expected_request)

    before_collision = {
        path.name: path.read_bytes() for path in (request_path, log_path, state_path)
    }
    _self_test_expect_value_error(
        lambda: initialize_feature_specification(
            project_root,
            "unicode-export",
            "replacement",
            execution,
            recorded_at=timestamp,
        ),
        "initialized over an existing feature specification",
    )
    assert before_collision == {
        path.name: path.read_bytes() for path in (request_path, log_path, state_path)
    }

    occupied_root = temporary_root / "feature-initialization-occupied"
    occupied_path = occupied_root / "docs/changes/specs/occupied/request.md"
    occupied_path.parent.mkdir(parents=True)
    occupied_path.write_bytes(b"existing\n")
    _self_test_expect_value_error(
        lambda: initialize_feature_specification(
            occupied_root,
            "occupied",
            "new request",
            execution,
            recorded_at=timestamp,
        ),
        "initialized over an existing request.md",
    )
    assert occupied_path.read_bytes() == b"existing\n"

    rollback_root = temporary_root / "feature-initialization-rollback"
    marker_path = rollback_root / "docs/keep.txt"
    marker_path.parent.mkdir(parents=True)
    marker_path.write_bytes(b"keep\n")

    def fail_after_log(path: str) -> None:
        if path.endswith("/mem-log.md"):
            raise OSError("simulated initialization failure")

    try:
        initialize_feature_specification(
            rollback_root,
            "rollback",
            "rollback request",
            execution,
            recorded_at=timestamp,
            _after_create=fail_after_log,
        )
    except OSError:
        pass
    else:
        raise AssertionError("partial initialization did not fail")
    assert marker_path.read_bytes() == b"keep\n"
    assert not (rollback_root / "docs/changes/specs/rollback").exists()

    valid_input = {
        "initial_request": "request",
        "execution": execution,
    }
    assert validate_feature_initialization_input(valid_input) == valid_input
    _self_test_expect_value_error(
        lambda: validate_feature_initialization_input({**valid_input, "extra": True}),
        "accepted an unknown feature initialization field",
    )


def self_test_audit_outbox(temporary_root: Path) -> None:
    timestamp = "2026-08-23T13:00:00Z"

    def initialize(label: str) -> tuple[Path, Path, Path, str]:
        project_root = temporary_root / f"audit-outbox-{label}"
        project_root.mkdir()
        result = initialize_feature_specification(
            project_root,
            "audit-flow",
            "Record every durable decision.",
            self_test_execution_snapshot(),
            recorded_at="2026-08-23T12:00:00Z",
        )
        specification_root = project_root / "docs/changes/specs/audit-flow"
        return (
            project_root,
            specification_root / "state.yaml",
            specification_root / "mem-log.md",
            str(result["initial_request_sha256"]),
        )

    def queue_reservation(
        label: str,
    ) -> tuple[Path, Path, Path, dict[str, object], bytes]:
        project_root, state_path, log_path, request_hash = initialize(label)
        prefix = log_path.read_bytes()
        reserve_run_in_state(
            state_path,
            project_root,
            "audit-flow",
            "idea",
            "idea-author",
            "draft",
            "author",
            "codex",
            "docs/changes/specs/audit-flow/idea.md",
            request_hash,
        )
        _, state = validate_state_path(state_path, project_root, "audit-flow")
        assert state["active_run"]["run_id"] == "audit-flow--idea--idea-author--1"
        assert state["next_run_sequence"] == 2
        outbox = state["audit"]["outbox"]
        assert outbox["transaction_id"] == "audit-tx-000004"
        assert len(outbox["events"]) == 1
        event = outbox["events"][0]
        assert event["event_key"] == "run/audit-flow--idea--idea-author--1/reserved"
        assert event["event"] == "role-run-reserved"
        assert event["related_events"] == ["MEM-000003"]
        assert event["payload"] == {
            "purpose": "draft",
            "profile": "author",
            "adapter": "codex",
            "configured_agent": "stepan_author",
            "requested_model": None,
            "requested_reasoning": None,
            "output_path": "docs/changes/specs/audit-flow/idea.md",
        }
        assert log_path.read_bytes() == prefix
        return project_root, state_path, log_path, state, prefix

    fabricated_root, fabricated_state_path, _, request_hash = initialize(
        "fabricated-active-run"
    )
    _, fabricated_state = validate_state_path(
        fabricated_state_path, fabricated_root, "audit-flow"
    )
    fabricated_state["next_run_sequence"] = 2
    fabricated_state["active_run"] = {
        "run_id": "audit-flow--idea--idea-author--1",
        "sequence": 1,
        "stage": "idea",
        "role": "idea-author",
        "purpose": "draft",
        "executor": "author",
        "adapter": "codex",
        "output": "docs/changes/specs/audit-flow/idea.md",
        "request_sha256": request_hash,
    }
    fabricated_state_path.write_bytes(render_canonical_yaml_mapping(fabricated_state))
    try:
        validate_state_path(fabricated_state_path, fabricated_root, "audit-flow")
    except ValueError as error:
        assert "no unique queued or durable reservation event" in str(error)
    else:
        raise AssertionError("accepted an active run without reservation evidence")

    project_root, state_path, log_path, queued_state, prefix = queue_reservation(
        "normal"
    )
    try:
        validate_role_manifest(
            {},
            Path(__file__).resolve().parent.parent,
            project_root,
            "native",
            queued_state,
        )
    except ValueError as error:
        assert "outbox is pending" in str(error)
    else:
        raise AssertionError("allowed role dispatch before the reservation audit flush")
    result = flush_audit_outbox(
        state_path, project_root, "audit-flow", recorded_at=timestamp
    )
    assert result["status"] == "flushed"
    assert result["event_ids"] == ["MEM-000004"]
    durable_bytes = log_path.read_bytes()
    assert durable_bytes.startswith(prefix)
    parsed = validate_audit_log(durable_bytes)
    assert len(parsed["events"]) == 4
    assert parsed["events"][-1]["recorded_at"] == timestamp
    _, flushed_state = validate_state_path(state_path, project_root, "audit-flow")
    assert flushed_state["audit"]["outbox"] is None
    assert flushed_state["audit"]["head_event_id"] == "MEM-000004"
    state_after_flush = state_path.read_bytes()
    repeated = flush_audit_outbox(state_path, project_root, "audit-flow")
    assert repeated["status"] == "no-op"
    assert repeated["event_ids"] == []
    assert log_path.read_bytes() == durable_bytes
    assert state_path.read_bytes() == state_after_flush
    mismatched_state = copy.deepcopy(flushed_state)
    mismatched_state["execution"]["profiles"]["author"]["agent"] = "changed-author"
    state_path.write_bytes(render_canonical_yaml_mapping(mismatched_state))
    try:
        validate_state_path(state_path, project_root, "audit-flow")
    except ValueError as error:
        assert "reservation event does not match state" in str(error)
    else:
        raise AssertionError("accepted mismatched durable reservation provenance")

    def crash() -> None:
        raise RuntimeError("simulated crash")

    project_root, state_path, log_path, _, prefix = queue_reservation("temp")
    try:
        flush_audit_outbox(
            state_path,
            project_root,
            "audit-flow",
            recorded_at=timestamp,
            _before_log_replace=crash,
        )
    except RuntimeError:
        pass
    else:
        raise AssertionError("did not stop at the pre-log-replacement crash boundary")
    assert log_path.read_bytes() == prefix
    _, still_queued = validate_state_path(state_path, project_root, "audit-flow")
    assert still_queued["audit"]["outbox"] is not None
    assert not list(log_path.parent.glob(".mem-log.md.*.tmp"))
    assert flush_audit_outbox(
        state_path, project_root, "audit-flow", recorded_at=timestamp
    )["status"] == "flushed"


def self_test_role_decisions(temporary_root: Path) -> None:
    def decision(
        key: str,
        *,
        authority: str = "agent",
        kind: str = "technical",
        summary: str = "Use an asynchronous export job.",
        rationale: str = "Large exports may exceed the request lifetime.",
        references: list[str] | None = None,
        source_event_keys: list[str] | None = None,
    ) -> dict[str, object]:
        return {
            "key": key,
            "authority": authority,
            "kind": kind,
            "summary": summary,
            "rationale": rationale,
            "alternatives": [
                {
                    "option": "Generate synchronously.",
                    "rejected_because": "It may time out.",
                }
            ],
            "references": [key] if references is None else references,
            "source_event_keys": [] if source_event_keys is None else source_event_keys,
        }

    design_one = decision("DES-001")
    validate_role_decision_coverage(
        [design_one], "design-author", artifact_references={"DES-001"}
    )
    for invalid, message in (
        ({**design_one, "authority": "router"}, "accepted invalid authority"),
        ({**design_one, "rationale": ""}, "accepted empty rationale"),
    ):
        _self_test_expect_value_error(
            lambda invalid=invalid: validate_decisions_snapshot(
                [invalid], "design-author"
            ),
            message,
        )
    _self_test_expect_value_error(
        lambda: validate_decisions_snapshot([design_one, design_one], "design-author"),
        "accepted duplicate decision keys",
    )
    _self_test_expect_value_error(
        lambda: validate_decisions_snapshot(
            [
                {
                    **design_one,
                    "alternatives": [{"option": "Generate synchronously."}],
                }
            ],
            "design-author",
        ),
        "accepted an alternative without a rejection reason",
    )
    _self_test_expect_value_error(
        lambda: validate_role_decision_coverage(
            [design_one],
            "design-author",
            artifact_references={"DES-001", "DES-002"},
        ),
        "accepted a design snapshot missing DES-* coverage",
    )
    _self_test_expect_value_error(
        lambda: validate_role_decision_coverage(
            [{**design_one, "references": ["DES-999"]}],
            "design-author",
            artifact_references={"DES-001"},
        ),
        "accepted an invalid design reference",
    )

    review = {
        "schema_version": 2,
        "stage": "requirements",
        "inputs": [],
        "verdict": "pass",
        "findings": [
            {
                "id": "REQ-R-001",
                "severity": "advisory",
                "resolution": "none",
                "references": ['ADDED Requirement "Export history"'],
                "problem": "Clarify the retention boundary.",
                "recommendation": "Name the retention period.",
            }
        ],
    }
    verdict = decision(
        "VERDICT", kind="verdict", references=[]
    )
    finding = decision(
        "REQ-R-001",
        kind="finding",
        references=['ADDED Requirement "Export history"'],
    )
    validate_role_decision_coverage(
        [verdict, finding], "requirements-reviewer", review=review
    )
    _self_test_expect_value_error(
        lambda: validate_role_decision_coverage(
            [verdict], "requirements-reviewer", review=review
        ),
        "accepted a review snapshot missing a finding",
    )
    _self_test_expect_value_error(
        lambda: validate_role_decision_coverage(
            [verdict, {**finding, "references": ['ADDED Requirement "Other"']}],
            "requirements-reviewer",
            review=review,
        ),
        "accepted a review finding reference mismatch",
    )

    run_id = "export-data--design--design-author--2"
    acceptance_key = f"run/{run_id}/artifact-accepted"
    artifact_hash = "sha256:" + "a" * 64
    receipt_hash = "sha256:" + "b" * 64
    introduced = prepare_role_decision_diff(
        "design-author",
        [design_one],
        {},
        run_id=run_id,
        artifact_path="docs/changes/specs/export-data/design.md",
        artifact_sha256=artifact_hash,
        receipt_sha256=receipt_hash,
        acceptance_event_key=acceptance_key,
    )
    assert [event["event"] for event in introduced["events"]] == [
        "agent-decision-introduced"
    ]
    introduced_index = decision_index_after_queued_events(
        {}, introduced["events"], 10
    )
    semantic_hash = decision_semantic_sha256(
        {field: design_one[field] for field in DECISION_SEMANTIC_FIELDS}
    )
    assert introduced_index == {
        "design-author": {
            "DES-001": {
                "event_id": "MEM-000010",
                "semantic_sha256": semantic_hash,
            }
        }
    }
    unchanged = prepare_role_decision_diff(
        "design-author",
        [design_one],
        introduced_index,
        run_id=run_id,
        artifact_path="docs/changes/specs/export-data/design.md",
        artifact_sha256=artifact_hash,
        receipt_sha256=receipt_hash,
        acceptance_event_key=acceptance_key,
    )
    assert unchanged == {"events": [], "unchanged_keys": ["DES-001"]}
    revised_decision = {**design_one, "summary": "Queue export jobs."}
    revised = prepare_role_decision_diff(
        "design-author",
        [revised_decision],
        introduced_index,
        run_id=run_id,
        artifact_path="docs/changes/specs/export-data/design.md",
        artifact_sha256=artifact_hash,
        receipt_sha256=receipt_hash,
        acceptance_event_key=acceptance_key,
    )
    assert revised["events"][0]["event"] == "agent-decision-revised"
    assert revised["events"][0]["payload"]["supersedes_event_id"] == "MEM-000010"
    retired = prepare_role_decision_diff(
        "design-author",
        [],
        introduced_index,
        run_id=run_id,
        artifact_path="docs/changes/specs/export-data/design.md",
        artifact_sha256=artifact_hash,
        receipt_sha256=receipt_hash,
        acceptance_event_key=acceptance_key,
    )
    assert retired["events"][0]["event"] == "agent-decision-retired"
    assert retired["events"][0]["payload"]["retired_event_id"] == "MEM-000010"
    assert decision_event_key_segment("DES-001") == "DES-001"
    unsafe_key = 'ADDED Requirement "Export history"'
    assert decision_event_key_segment(unsafe_key) == (
        "sha256-" + hashlib.sha256(unsafe_key.encode("utf-8")).hexdigest()
    )

    def initialized_active_idea(label: str) -> tuple[Path, Path, str]:
        project_root = temporary_root / label
        project_root.mkdir()
        initialization = initialize_feature_specification(
            project_root,
            label,
            "Export history.",
            self_test_execution_snapshot(),
            recorded_at="2026-08-23T12:34:56Z",
        )
        state_path = project_root / f"docs/changes/specs/{label}/state.yaml"
        request_hash = str(initialization["initial_request_sha256"])
        reserve_run_in_state(
            state_path,
            project_root,
            label,
            "idea",
            "idea-author",
            "draft",
            "author",
            "codex",
            f"docs/changes/specs/{label}/idea.md",
            request_hash,
        )
        flush_audit_outbox(
            state_path,
            project_root,
            label,
            recorded_at="2026-08-23T12:35:00Z",
        )
        idea_path = project_root / f"docs/changes/specs/{label}/idea.md"
        idea_path.write_text(
            "# Idea\n\n## Outcome\n\nExport history.\n",
            encoding="utf-8",
            newline="\n",
        )
        return project_root, state_path, request_hash

    invalid_root, invalid_state_path, invalid_request_hash = initialized_active_idea(
        "decision-invalid"
    )
    invalid_before = invalid_state_path.read_bytes()
    missing_snapshot_receipt = {
        "schema_version": 1,
        "run_id": "decision-invalid--idea--idea-author--1",
        "status": "completed",
        "output": {
            "path": "docs/changes/specs/decision-invalid/idea.md",
            "sha256": file_hash(
                invalid_root / "docs/changes/specs/decision-invalid/idea.md"
            ),
        },
    }
    _self_test_expect_value_error(
        lambda: accept_completed_role_result_in_state(
            invalid_state_path,
            invalid_root,
            "decision-invalid",
            missing_snapshot_receipt,
            [
                "docs/changes/specs/decision-invalid/request.md="
                + invalid_request_hash
            ],
        ),
        "accepted a completed role result without decisions",
    )
    assert invalid_state_path.read_bytes() == invalid_before
    _, invalid_state = validate_state_path(
        invalid_state_path, invalid_root, "decision-invalid"
    )
    assert invalid_state["active_run"] is not None
    invalid_source_receipt = {
        **missing_snapshot_receipt,
        "decisions": [
            decision(
                "IDEA-001",
                authority="user",
                kind="framing",
                source_event_keys=["interaction/decision-invalid/unknown-input"],
            )
        ],
    }
    _self_test_expect_value_error(
        lambda: accept_completed_role_result_in_state(
            invalid_state_path,
            invalid_root,
            "decision-invalid",
            invalid_source_receipt,
            [
                "docs/changes/specs/decision-invalid/request.md="
                + invalid_request_hash
            ],
        ),
        "accepted an undeclared user-authority source event",
    )
    assert invalid_state_path.read_bytes() == invalid_before

    valid_root, valid_state_path, valid_request_hash = initialized_active_idea(
        "decision-valid"
    )
    initial_source_key = "interaction/decision-valid/initial-request"
    valid_receipt = {
        "schema_version": 1,
        "run_id": "decision-valid--idea--idea-author--1",
        "status": "completed",
        "output": {
            "path": "docs/changes/specs/decision-valid/idea.md",
            "sha256": file_hash(
                valid_root / "docs/changes/specs/decision-valid/idea.md"
            ),
        },
        "decisions": [
            {
                **decision(
                    "IDEA-001",
                    authority="user",
                    kind="framing",
                    source_event_keys=[initial_source_key],
                ),
            },
            decision("IDEA-002", kind="framing"),
        ],
    }
    result = accept_completed_role_result_in_state(
        valid_state_path,
        valid_root,
        "decision-valid",
        valid_receipt,
        ["docs/changes/specs/decision-valid/request.md=" + valid_request_hash],
    )
    _, queued_state = validate_state_path(
        valid_state_path, valid_root, "decision-valid"
    )
    assert queued_state["active_run"] is None
    assert queued_state["stage"] == "idea" and queued_state["status"] == "awaiting-approval"
    assert queued_state["audit"]["decision_index"] == {}
    assert queued_state["audit"]["outbox"] is not None
    assert [event["event"] for event in queued_state["audit"]["outbox"]["events"]] == [
        "role-run-completed",
        "artifact-accepted",
        "clarification-applied",
        "agent-decision-introduced",
    ]
    normalization = queued_state["audit"]["outbox"]["events"][-2]
    assert normalization["related_events"] == ["MEM-000001"]
    assert result["unchanged_decision_keys"] == []
    queued_before = valid_state_path.read_bytes()
    _self_test_expect_value_error(
        lambda: accept_completed_role_result_in_state(
            valid_state_path,
            valid_root,
            "decision-valid",
            valid_receipt,
            ["docs/changes/specs/decision-valid/request.md=" + valid_request_hash],
        ),
        "accepted another role result while the audit outbox was pending",
    )
    assert valid_state_path.read_bytes() == queued_before
    flush_audit_outbox(
        valid_state_path,
        valid_root,
        "decision-valid",
        recorded_at="2026-08-23T12:36:00Z",
    )
    _, accepted_state = validate_state_path(
        valid_state_path, valid_root, "decision-valid"
    )
    assert set(accepted_state["audit"]["decision_index"]["idea-author"]) == {
        "IDEA-002"
    }

    waiting_state = copy.deepcopy(invalid_state)
    waiting_state["execution"]["adapters"]["mailbox"] = {
        "kind": "mailbox",
        "root": str(temporary_root / "mailbox"),
        "wait_seconds": 30,
    }
    waiting_state["execution"]["profiles"]["mailbox-author"] = {
        "adapter": "mailbox",
        "model": "author-v1",
        "project_inputs": [],
    }
    waiting_state["execution"]["bindings"]["idea-author"] = "mailbox-author"
    waiting_state["active_run"]["executor"] = "mailbox-author"
    waiting_state["active_run"]["adapter"] = "mailbox"
    waiting_state["status"] = "waiting-executor"
    validate_state_value(waiting_state, "decision-invalid")
    waiting_receipt = {
        **valid_receipt,
        "run_id": "decision-invalid--idea--idea-author--1",
        "output": missing_snapshot_receipt["output"],
        "executor": {
            "requested": {"model": "author-v1"},
            "effective": {"model": "author-v1"},
        },
    }
    _self_test_expect_value_error(
        lambda: prepare_completed_role_result_state(
            waiting_state,
            "decision-invalid",
            waiting_receipt,
            artifact_references=set(),
            review=None,
            durable_events=[],
        ),
        "accepted a completed result before ending waiting-executor",
    )

def self_test_audit_outbox_recovery(temporary_root: Path) -> None:
    # Continue crash-boundary coverage with fresh temporary labels.
    timestamp = "2026-08-23T13:00:00Z"

    def initialize(label: str) -> tuple[Path, Path, Path, str]:
        project_root = temporary_root / f"audit-outbox-{label}"
        project_root.mkdir()
        initialization = initialize_feature_specification(
            project_root,
            "audit-flow",
            "Record every durable decision.",
            self_test_execution_snapshot(),
            recorded_at="2026-08-23T12:00:00Z",
        )
        specification_root = project_root / "docs/changes/specs/audit-flow"
        return (
            project_root,
            specification_root / "state.yaml",
            specification_root / "mem-log.md",
            str(initialization["initial_request_sha256"]),
        )

    def queue_reservation(
        label: str,
    ) -> tuple[Path, Path, Path, dict[str, object], bytes]:
        project_root, state_path, log_path, request_hash = initialize(label)
        prefix = log_path.read_bytes()
        reserve_run_in_state(
            state_path,
            project_root,
            "audit-flow",
            "idea",
            "idea-author",
            "draft",
            "author",
            "codex",
            "docs/changes/specs/audit-flow/idea.md",
            request_hash,
        )
        _, queued_state = validate_state_path(
            state_path, project_root, "audit-flow"
        )
        return project_root, state_path, log_path, queued_state, prefix

    def crash() -> None:
        raise RuntimeError("simulated crash")

    project_root, state_path, log_path, _, prefix = queue_reservation("log")
    try:
        flush_audit_outbox(
            state_path,
            project_root,
            "audit-flow",
            recorded_at=timestamp,
            _after_log_replace=crash,
        )
    except RuntimeError:
        pass
    else:
        raise AssertionError("did not stop after replacing the audit log")
    appended_before_recovery = log_path.read_bytes()
    assert appended_before_recovery.startswith(prefix)
    _self_test_expect_value_error(
        lambda: validate_state_path(state_path, project_root, "audit-flow"),
        "ordinary state validation accepted uncommitted audit metadata",
    )
    recovered = flush_audit_outbox(state_path, project_root, "audit-flow")
    assert recovered["status"] == "recovered"
    assert recovered["event_ids"] == ["MEM-000004"]
    assert log_path.read_bytes() == appended_before_recovery
    assert len(validate_audit_log(log_path.read_bytes())["events"]) == 4

    project_root, state_path, log_path, _, _ = queue_reservation("state")
    try:
        flush_audit_outbox(
            state_path,
            project_root,
            "audit-flow",
            recorded_at=timestamp,
            _after_state_replace=crash,
        )
    except RuntimeError:
        pass
    else:
        raise AssertionError("did not simulate a lost result after state replacement")
    lost_result_log = log_path.read_bytes()
    _, lost_result_state = validate_state_path(state_path, project_root, "audit-flow")
    assert lost_result_state["audit"]["outbox"] is None
    assert flush_audit_outbox(state_path, project_root, "audit-flow")["status"] == "no-op"
    assert log_path.read_bytes() == lost_result_log
    assert len(validate_audit_log(lost_result_log)["events"]) == 4

    project_root, state_path, log_path, queued_state, prefix = queue_reservation("stale")
    stale = copy.deepcopy(queued_state)
    stale["audit"]["outbox"]["expected_log_sha256"] = "sha256:" + "f" * 64
    state_path.write_bytes(render_canonical_yaml_mapping(stale))
    _self_test_expect_value_error(
        lambda: flush_audit_outbox(state_path, project_root, "audit-flow"),
        "flushed an outbox prepared against stale log metadata",
    )
    assert log_path.read_bytes() == prefix

    project_root, state_path, log_path, request_hash = initialize("conflict")
    _, base_state = validate_state_path(state_path, project_root, "audit-flow")
    first_event = validate_audit_log(log_path.read_bytes())["events"][0]
    conflict_event = build_queued_audit_event(
        event_key="interaction/audit-flow/initial-request",
        kind="interaction",
        event="initial-request-captured",
        stage="idea",
        actor="user",
        payload={
            "request_path": "docs/changes/specs/audit-flow/request.md",
            "request_sha256": request_hash,
            "verbatim": first_event["payload"]["verbatim"],
        },
    )
    conflict_state = queue_audit_transaction(
        base_state, "audit-flow", [conflict_event]
    )
    state_path.write_bytes(render_canonical_yaml_mapping(conflict_state))
    conflict_prefix = log_path.read_bytes()
    _self_test_expect_value_error(
        lambda: flush_audit_outbox(state_path, project_root, "audit-flow"),
        "accepted an audit event key already present in the durable prefix",
    )
    assert log_path.read_bytes() == conflict_prefix

    project_root, state_path, log_path, _ = initialize("reordered")
    _, base_state = validate_state_path(state_path, project_root, "audit-flow")
    question_key = "interaction/audit-flow/question/1"
    question = build_queued_audit_event(
        event_key=question_key,
        kind="interaction",
        event="user-question",
        stage="idea",
        actor="user",
        payload={"verbatim": "What is recorded?"},
    )
    answer = build_queued_audit_event(
        event_key="interaction/audit-flow/answer/1",
        kind="interaction",
        event="agent-answer",
        stage="idea",
        actor="router",
        related_event_keys=[question_key],
        payload={"answer": "Every durable workflow decision."},
    )
    reordered_state = queue_audit_transaction(
        base_state, "audit-flow", [answer, question]
    )
    reordered_state["audit"]["outbox"]["events"].reverse()
    state_path.write_bytes(render_canonical_yaml_mapping(reordered_state))
    reordered_prefix = log_path.read_bytes()
    _self_test_expect_value_error(
        lambda: flush_audit_outbox(state_path, project_root, "audit-flow"),
        "flushed a reordered audit batch",
    )
    assert log_path.read_bytes() == reordered_prefix

    project_root, state_path, log_path, queued_state, prefix = queue_reservation(
        "suffix-conflict"
    )
    accepted_log = validate_audit_log(prefix)
    conflicting_outbox = copy.deepcopy(queued_state["audit"]["outbox"])
    conflicting_outbox["events"][0]["payload"]["profile"] = "other-profile"
    conflicting_events = materialize_audit_outbox(
        accepted_log, conflicting_outbox, timestamp
    )
    conflicting_suffix = prefix + b"".join(
        render_audit_event(event) for event in conflicting_events
    )
    validate_audit_log_extension(prefix, conflicting_suffix)
    log_path.write_bytes(conflicting_suffix)
    _self_test_expect_value_error(
        lambda: flush_audit_outbox(state_path, project_root, "audit-flow"),
        "recovered a canonical suffix with a conflicting queued payload",
    )
    assert state_path.read_bytes() == render_canonical_yaml_mapping(queued_state)
    log_path.write_bytes(prefix)
    assert flush_audit_outbox(
        state_path, project_root, "audit-flow", recorded_at=timestamp
    )["status"] == "flushed"

    project_root, state_path, log_path, _, prefix = queue_reservation("concurrent")

    def change_log() -> None:
        log_path.write_bytes(prefix + b"manual concurrent append\n")

    _self_test_expect_value_error(
        lambda: flush_audit_outbox(
            state_path,
            project_root,
            "audit-flow",
            recorded_at=timestamp,
            _before_log_replace=change_log,
        ),
        "overwrote a concurrently changed audit log",
    )
    assert not list(log_path.parent.glob(".mem-log.md.*.tmp"))
    log_path.write_bytes(prefix)
    assert flush_audit_outbox(
        state_path, project_root, "audit-flow", recorded_at=timestamp
    )["status"] == "flushed"


def _self_test_decision_key(role: str) -> str:
    return {
        "idea-author": "IDEA-001",
        "requirements-author": 'ADDED Requirement "Export history"',
        "requirements-reviewer": "REQ-R-001",
        "design-author": "DES-001",
        "specification-reviewer": "DES-R-001",
        "planner": "STEP-001",
    }[role]


def _self_test_audit_payload(kind: str, event: str, role: str | None) -> dict[str, object]:
    payload: dict[str, object] = {}
    for field, rule in AUDIT_EVENT_PAYLOAD_SCHEMAS[(kind, event)].items():
        if isinstance(rule, tuple):
            payload[field] = rule[0]
        elif rule == "text":
            payload[field] = "value"
        elif rule in {"nullable_text", "nullable_hash", "nullable_stage"}:
            payload[field] = None
        elif rule == "hash":
            payload[field] = "sha256:" + "a" * 64
        elif rule == "project_path":
            payload[field] = "docs/changes/specs/export-data/artifact.md"
        elif rule == "project_path_list":
            payload[field] = ["docs/changes/specs/export-data/artifact.md"]
        elif rule == "text_list":
            payload[field] = ["value"]
        elif rule == "alternatives":
            payload[field] = [
                {"option": "Synchronous export", "rejected_because": "It may time out."}
            ]
        elif rule == "references":
            payload[field] = [_self_test_decision_key(role or "design-author")]
        elif rule == "event_id":
            payload[field] = "MEM-000001"
        elif rule == "spec_id":
            payload[field] = "export-data"
        elif rule == "stage":
            payload[field] = "design"
        elif rule == "decision_kind":
            payload[field] = ROLE_DECISION_KINDS[role or "design-author"][0]
        elif rule == "positive_int":
            payload[field] = 1
        elif rule == "non_negative_int":
            payload[field] = 60
        elif rule == "runtime_identity":
            payload[field] = "unavailable"
        else:
            raise AssertionError(f"missing audit self-test fixture for {rule!r}")
    if "key" in payload and role is not None:
        payload["key"] = _self_test_decision_key(role)
    if event == "initial-request-captured":
        payload["request_path"] = "docs/changes/specs/export-data/request.md"
        payload["verbatim"] = "Export history."
    elif event == "specification-created":
        payload["request_path"] = "docs/changes/specs/export-data/request.md"
        payload["log_path"] = "docs/changes/specs/export-data/mem-log.md"
    elif event == "role-run-reserved":
        payload["configured_agent"] = "stepan-architect"
    elif event == "stage-entered":
        payload["reason"] = "initial"
    elif event == "stage-approved":
        payload["review_sha256"] = "sha256:" + "a" * 64
    elif event == "review-accepted":
        payload["finding_ids"] = ["DES-R-001"]
    elif event == "automatic-revision-limit-reached":
        payload["attempt"] = 3
    elif event == "upstream-invalidated":
        payload["from_stage"] = "design"
        payload["to_stage"] = "requirements"
    if event in {"agent-decision-introduced", "agent-decision-revised"}:
        payload["semantic_sha256"] = decision_semantic_sha256(
            {
                "authority": "agent",
                "kind": payload["decision_kind"],
                "summary": payload["summary"],
                "rationale": payload["rationale"],
                "alternatives": payload["alternatives"],
                "references": payload["references"],
            }
        )
    return payload


def self_test_audit_schemas() -> None:
    assert AUDIT_LOG_MAX_BYTES == 10 * 1024 * 1024
    assert set(AUDIT_EVENT_PAYLOAD_SCHEMAS) == {
        (kind, event)
        for kind, events in AUDIT_EVENTS_BY_KIND.items()
        for event in events
    }
    assert set(AUDIT_EVENT_ORDER) == set(AUDIT_EVENT_PAYLOAD_SCHEMAS)
    assert format_audit_event_id(1) == "MEM-000001"
    assert format_audit_event_id(1_000_000) == "MEM-1000000"
    assert parse_audit_event_id("MEM-000001") == 1
    assert format_audit_transaction_id(12) == "audit-tx-000012"
    assert parse_audit_transaction_id("audit-tx-000012") == 12
    validate_audit_recorded_at("2026-08-23T12:34:56Z")
    for invalid in ("MEM-1", "MEM-000000", "MEM-0000001", "mem-000001"):
        _self_test_expect_value_error(
            lambda invalid=invalid: parse_audit_event_id(invalid),
            f"accepted invalid audit event ID {invalid}",
        )
    for invalid in (
        "2026-02-30T12:34:56Z",
        "2026-08-23T12:34:56+00:00",
        "2026-08-23T12:34:56.000Z",
    ):
        _self_test_expect_value_error(
            lambda invalid=invalid: validate_audit_recorded_at(invalid),
            f"accepted invalid audit recorded_at {invalid}",
        )

    workflow_only = {
        "specification-created",
        "specification-collision-selected",
        "recovery-selected",
        "workflow-resumed",
        "workflow-completed",
    }
    role_events = {"blocking-question", "clarification-applied"}
    run_lifecycle = {
        "role-run-reserved",
        "role-run-completed",
        "role-run-blocked",
        "role-run-failed",
        "role-run-interrupted",
        "executor-wait-started",
        "executor-wait-ended",
        "artifact-accepted",
        "review-accepted",
    }
    valid_queued: dict[tuple[str, str], dict[str, object]] = {}
    for kind, events in AUDIT_EVENTS_BY_KIND.items():
        for event_name in events:
            if kind == "decision" or event_name == "clarification-applied":
                actor = "design-author"
                stage = "design"
            elif event_name == "blocking-question":
                actor = "requirements-author"
                stage = "requirements"
            elif kind == "lifecycle":
                actor = "router"
                stage = "workflow" if event_name in workflow_only else "design"
            elif event_name in {"agent-answer"}:
                actor = "router"
                stage = "design"
            else:
                actor = "user"
                stage = "workflow" if event_name in workflow_only else "idea" if event_name == "initial-request-captured" else "design"
            run_required = kind == "decision" or event_name in role_events | run_lifecycle
            run_role = actor if actor in ROLES else (
                "specification-reviewer" if event_name == "review-accepted" else "design-author"
            )
            run_id = (
                role_run_id("export-data", stage, run_role, 1) if run_required else None
            )
            event_key = (
                f"run/{run_id}/{event_name}"
                if run_id is not None
                else f"workflow/{event_name}"
                if stage == "workflow"
                else f"interaction/export-data/{event_name}"
            )
            queued: dict[str, object] = {
                "event_key": event_key,
                "kind": kind,
                "event": event_name,
                "stage": stage,
                "actor": actor,
                "related_events": [],
                "related_event_keys": [],
                "payload": _self_test_audit_payload(
                    kind, event_name, actor if actor in ROLES else run_role if kind == "decision" else None
                ),
            }
            if run_id is not None:
                queued["run_id"] = run_id
            valid_queued[(kind, event_name)] = validate_queued_audit_event(queued)

    invalid_payload_event = {
        **valid_queued[("interaction", "initial-request-captured")],
        "payload": {
            **valid_queued[("interaction", "initial-request-captured")]["payload"],
            "unknown": True,
        },
    }
    _self_test_expect_value_error(
        lambda: validate_queued_audit_event(invalid_payload_event),
        "accepted an unknown queued audit payload field",
    )

    durable = {
        key: value
        for key, value in valid_queued[("lifecycle", "role-run-completed")].items()
        if key != "related_event_keys"
    }
    durable.update(
        {
            "event_id": "MEM-000002",
            "recorded_at": "2026-08-23T12:34:56Z",
            "previous_event_sha256": "sha256:" + "b" * 64,
            "event_sha256": "sha256:" + "c" * 64,
            "related_events": ["MEM-000001"],
        }
    )
    assert validate_durable_audit_event(durable) == durable
    invalid_durable = {**durable, "related_events": ["MEM-000002"]}
    _self_test_expect_value_error(
        lambda: validate_durable_audit_event(invalid_durable),
        "accepted a durable event related to itself",
    )
    invalid_first = {
        **durable,
        "event_id": "MEM-000001",
        "related_events": [],
    }
    _self_test_expect_value_error(
        lambda: validate_durable_audit_event(invalid_first),
        "accepted a first durable event with a previous hash",
    )

    empty_audit = {
        "schema_version": 1,
        "log_path": "docs/changes/specs/export-data/mem-log.md",
        "log_size_bytes": 256,
        "log_sha256": "sha256:" + "1" * 64,
        "head_event_id": None,
        "head_event_sha256": None,
        "next_event_sequence": 1,
        "decision_index": {},
        "outbox": None,
    }
    assert validate_audit_state(empty_audit, "export-data") == empty_audit
    decision_index = {
        "design-author": {
            "DES-001": {
                "event_id": "MEM-000001",
                "semantic_sha256": "sha256:" + "2" * 64,
            }
        }
    }
    completed_event = valid_queued[("lifecycle", "role-run-completed")]
    artifact_event = {
        **valid_queued[("lifecycle", "artifact-accepted")],
        "related_event_keys": [completed_event["event_key"]],
    }
    outbox = {
        "transaction_id": "audit-tx-000002",
        "expected_head_event_id": "MEM-000001",
        "expected_head_event_sha256": "sha256:" + "3" * 64,
        "expected_log_sha256": "sha256:" + "4" * 64,
        "events": [completed_event, artifact_event],
        "decision_index_after": decision_index,
    }
    populated_audit = {
        **empty_audit,
        "log_sha256": "sha256:" + "4" * 64,
        "head_event_id": "MEM-000001",
        "head_event_sha256": "sha256:" + "3" * 64,
        "next_event_sequence": 2,
        "decision_index": decision_index,
        "outbox": outbox,
    }
    assert validate_audit_state(populated_audit, "export-data") == populated_audit
    for invalid, message in (
        ({**empty_audit, "extra": True}, "accepted unknown audit state field"),
        ({**empty_audit, "log_path": "docs/other/mem-log.md"}, "accepted wrong audit path"),
        ({**empty_audit, "next_event_sequence": 2}, "accepted inconsistent empty head"),
        ({**empty_audit, "decision_index": decision_index}, "accepted indexed empty head"),
        (
            {
                **populated_audit,
                "outbox": {**outbox, "expected_log_sha256": "sha256:" + "5" * 64},
            },
            "accepted a stale audit outbox",
        ),
        (
            {
                **populated_audit,
                "outbox": {**outbox, "events": list(reversed(outbox["events"]))},
            },
            "accepted a non-canonical audit batch order",
        ),
    ):
        _self_test_expect_value_error(
            lambda invalid=invalid: validate_audit_state(invalid, "export-data"),
            message,
        )

    for role in ROLES:
        authority = "user" if role == "idea-author" else "agent"
        decision = {
            "key": _self_test_decision_key(role),
            "authority": authority,
            "kind": ROLE_DECISION_KINDS[role][0],
            "summary": "Material result.",
            "rationale": "It affects observable behavior.",
            "alternatives": [],
            "references": [_self_test_decision_key(role)],
            "source_event_keys": (
                ["interaction/export-data/initial-request"] if authority == "user" else []
            ),
        }
        assert validate_decisions_snapshot([decision], role) == [decision]
        _self_test_expect_value_error(
            lambda role=role, decision=decision: validate_decisions_snapshot(
                [decision, decision], role
            ),
            f"accepted duplicate decision key for {role}",
        )
    bad_user_decision = {
        "key": "IDEA-001",
        "authority": "user",
        "kind": "framing",
        "summary": "Material result.",
        "rationale": "The user selected it.",
        "alternatives": [],
        "references": [],
        "source_event_keys": [],
    }
    _self_test_expect_value_error(
        lambda: validate_decisions_snapshot([bad_user_decision], "idea-author"),
        "accepted an unlinked user-authority decision",
    )
    invalid_alternative = {
        **bad_user_decision,
        "source_event_keys": ["interaction/export-data/initial-request"],
        "alternatives": [{"option": "Another framing"}],
    }
    _self_test_expect_value_error(
        lambda: validate_decisions_snapshot([invalid_alternative], "idea-author"),
        "accepted an alternative without a rejection reason",
    )

    request_hash = "sha256:" + "a" * 64
    header = render_audit_header(
        "export-data", "docs/changes/specs/export-data/request.md", request_hash
    )
    expected_header = {
        "schema_version": 1,
        "specification": "export-data",
        "request_path": "docs/changes/specs/export-data/request.md",
        "request_sha256": request_hash,
    }
    assert parse_audit_header(header) == expected_header
    assert render_audit_log(expected_header, []) == header
    empty_log = validate_audit_log(
        header,
        expected_log_size_bytes=len(header),
        expected_log_sha256=audit_bytes_sha256(header),
        expected_head_event_id=None,
        expected_head_event_sha256=None,
        expected_next_event_sequence=1,
    )
    assert empty_log["events"] == []
    _self_test_expect_value_error(
        lambda: parse_audit_header(header + b"\n"),
        "accepted a non-canonical audit header newline",
    )

    def durable_fixture(
        queued: dict[str, object], sequence: int, previous_hash: str | None
    ) -> dict[str, object]:
        event = {
            key: value
            for key, value in queued.items()
            if key != "related_event_keys"
        }
        event.update(
            {
                "event_id": format_audit_event_id(sequence),
                "recorded_at": "2026-08-23T12:34:56Z",
                "previous_event_sha256": previous_hash,
            }
        )
        return seal_audit_event(event)

    previous_fixture_hash = "sha256:" + "b" * 64
    for identity, queued in valid_queued.items():
        sequence = 1 if identity == ("interaction", "initial-request-captured") else 2
        event = durable_fixture(
            queued, sequence, None if sequence == 1 else previous_fixture_hash
        )
        rendered = render_audit_event(event)
        assert parse_audit_event(rendered) == event
        assert render_audit_event(parse_audit_event(rendered)) == rendered

    unicode_verbatim = (
        "Полный запрос\r\n"
        "## MEM-999999 — forged heading\n"
        "```markdown\ninside\n```\n"
        "`````` nested fence `````` and `inline`\n"
        "final without newline"
    )
    unicode_queued = {
        **valid_queued[("interaction", "initial-request-captured")],
        "payload": {
            **valid_queued[("interaction", "initial-request-captured")]["payload"],
            "verbatim": unicode_verbatim,
        },
    }
    unicode_event = durable_fixture(unicode_queued, 1, None)
    unicode_rendered = render_audit_event(unicode_event)
    assert b"```````stepan-text\n" in unicode_rendered
    assert parse_audit_event(unicode_rendered)["payload"]["verbatim"] == unicode_verbatim

    first = durable_fixture(
        valid_queued[("interaction", "initial-request-captured")], 1, None
    )
    second = durable_fixture(
        valid_queued[("lifecycle", "specification-created")],
        2,
        str(first["event_sha256"]),
    )
    third = durable_fixture(
        valid_queued[("lifecycle", "stage-entered")],
        3,
        str(second["event_sha256"]),
    )
    first_block = render_audit_event(first)
    second_block = render_audit_event(second)
    third_block = render_audit_event(third)
    log = render_audit_log(expected_header, [first, second, third])
    assert log == header + first_block + second_block + third_block
    parsed_log = validate_audit_log(
        log,
        expected_log_size_bytes=len(log),
        expected_log_sha256=audit_bytes_sha256(log),
        expected_head_event_id="MEM-000003",
        expected_head_event_sha256=third["event_sha256"],
        expected_next_event_sequence=4,
    )
    assert parsed_log["events"] == [first, second, third]
    assert validate_audit_log_extension(header + first_block, log) == parsed_log

    introduced = durable_fixture(
        valid_queued[("decision", "agent-decision-introduced")],
        4,
        str(third["event_sha256"]),
    )
    introduced_index = {
        "design-author": {
            "DES-001": {
                "event_id": "MEM-000004",
                "semantic_sha256": introduced["payload"]["semantic_sha256"],
            }
        }
    }
    assert audit_decision_index([first, second, third, introduced]) == introduced_index
    revised_queued = json.loads(
        json.dumps(valid_queued[("decision", "agent-decision-revised")])
    )
    revised_queued["payload"]["supersedes_event_id"] = "MEM-000004"
    revised_queued["payload"]["summary"] = "Revised material result."
    revised_queued["payload"]["semantic_sha256"] = decision_semantic_sha256(
        {
            "authority": "agent",
            "kind": revised_queued["payload"]["decision_kind"],
            "summary": revised_queued["payload"]["summary"],
            "rationale": revised_queued["payload"]["rationale"],
            "alternatives": revised_queued["payload"]["alternatives"],
            "references": revised_queued["payload"]["references"],
        }
    )
    revised = durable_fixture(revised_queued, 5, str(introduced["event_sha256"]))
    revised_index = {
        "design-author": {
            "DES-001": {
                "event_id": "MEM-000005",
                "semantic_sha256": revised_queued["payload"]["semantic_sha256"],
            }
        }
    }
    assert audit_decision_index(
        [first, second, third, introduced, revised]
    ) == revised_index
    retired_queued = json.loads(
        json.dumps(valid_queued[("decision", "agent-decision-retired")])
    )
    retired_queued["payload"]["retired_event_id"] = "MEM-000005"
    retired = durable_fixture(retired_queued, 6, str(revised["event_sha256"]))
    assert audit_decision_index(
        [first, second, third, introduced, revised, retired]
    ) == {}
    wrong_revision = json.loads(json.dumps(revised))
    wrong_revision["payload"]["supersedes_event_id"] = "MEM-000003"
    wrong_revision["event_sha256"] = AUDIT_EVENT_HASH_SENTINEL
    wrong_revision = seal_audit_event(wrong_revision)
    _self_test_expect_value_error(
        lambda: audit_decision_index(
            [first, second, third, introduced, wrong_revision]
        ),
        "accepted a decision revision that did not supersede the active event",
    )

    changed_metadata = log.replace(
        b"2026-08-23T12:34:56Z", b"2026-08-23T12:34:57Z", 1
    )
    changed_body = log.replace(b"Export history.", b"Export historx.", 1)
    deleted_event = header + first_block + third_block
    reordered_events = header + second_block + first_block + third_block
    duplicated_event = header + first_block + second_block + second_block + third_block
    truncated_event = log[:-5]
    wrong_previous = seal_audit_event(
        {**second, "previous_event_sha256": "sha256:" + "0" * 64}
    )
    invalid_previous_chain = header + first_block + render_audit_event(wrong_previous)
    duplicate_key = seal_audit_event(
        {
            **third,
            "event_key": second["event_key"],
            "previous_event_sha256": second["event_sha256"],
        }
    )
    duplicate_key_log = header + first_block + second_block + render_audit_event(
        duplicate_key
    )
    for tampered, message in (
        (changed_metadata, "accepted changed audit metadata"),
        (changed_body, "accepted changed audit body text"),
        (deleted_event, "accepted a deleted audit event"),
        (reordered_events, "accepted reordered audit events"),
        (duplicated_event, "accepted a duplicated audit event"),
        (truncated_event, "accepted a truncated audit event"),
        (invalid_previous_chain, "accepted an invalid previous-event hash"),
        (duplicate_key_log, "accepted a duplicate audit event key"),
        (log + b"manual append\n", "accepted an unrecognized manual append"),
    ):
        _self_test_expect_value_error(
            lambda tampered=tampered: validate_audit_log(tampered), message
        )

    for arguments, message in (
        (
            {"expected_log_size_bytes": len(log) + 1},
            "accepted a wrong state log length",
        ),
        (
            {"expected_log_sha256": "sha256:" + "f" * 64},
            "accepted a wrong state log hash",
        ),
        (
            {"expected_head_event_id": "MEM-000002"},
            "accepted a wrong state head event ID",
        ),
        (
            {"expected_head_event_sha256": "sha256:" + "f" * 64},
            "accepted a wrong state head event hash",
        ),
        (
            {"expected_next_event_sequence": 5},
            "accepted a wrong state next event sequence",
        ),
    ):
        _self_test_expect_value_error(
            lambda arguments=arguments: validate_audit_log(log, **arguments), message
        )
    _self_test_expect_value_error(
        lambda: validate_audit_log_extension(
            header + first_block, b"X" + log[1:]
        ),
        "accepted a candidate that changed the audit byte prefix",
    )
    try:
        validate_audit_log(b"\xff" * (AUDIT_LOG_MAX_BYTES + 1))
    except ValueError as error:
        assert "exceeds" in str(error)
    else:
        raise AssertionError("accepted an oversized audit log")


def self_test_revision_manifests(temporary_root: Path, skill_root: Path) -> None:
    project_root = temporary_root / "revision-manifests"
    specification_root = project_root / "docs/changes/specs/export-data"
    specification_root.mkdir(parents=True)
    context_path = project_root / "docs/product/context.md"
    context_path.parent.mkdir(parents=True)
    context_path.write_text("Current product context.\n", encoding="utf-8", newline="\n")

    execution = self_test_execution_snapshot()
    execution["profiles"]["author"]["project_inputs"] = [
        {
            "path": "docs/product/context.md",
            "sha256": file_hash(context_path),
        }
    ]
    initialized_files, initialized_state = _initial_feature_files(
        "export-data",
        "Export transaction history.",
        execution,
        "2026-08-15T12:00:00Z",
    )
    for relative, content in initialized_files:
        target = project_root.joinpath(*relative.parts)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content)

    artifact_contents = {
        "idea.md": "# Idea\n## Outcome\nExport transaction history.\n",
        "requirements.md": """# Export Specification Delta
## Purpose
Allow users to export transaction history.
## ADDED Requirements
### Requirement: Export History
The system SHALL export transaction history.
#### Scenario: Successful export
- **WHEN** an authorized user requests an export
- **THEN** the system returns the transaction history
## Boundaries and assumptions
Only authorized users are in scope.
""",
        "design.md": "# Design\n## Overview\nAdd an export service.\n",
        "plan.md": "# Plan\n## Steps\nImplement the export service.\n",
    }
    for name, content in artifact_contents.items():
        (specification_root / name).write_text(
            content, encoding="utf-8", newline="\n"
        )

    request_path = specification_root / "request.md"
    idea_path = specification_root / "idea.md"
    requirements_path = specification_root / "requirements.md"
    log_path = specification_root / "mem-log.md"
    review_path = specification_root / "review/requirements.yaml"
    review_path.parent.mkdir()
    review = {
        "schema_version": 2,
        "stage": "requirements",
        "inputs": [
            {
                "path": "docs/changes/specs/export-data/request.md",
                "sha256": file_hash(request_path),
            },
            {
                "path": "docs/changes/specs/export-data/idea.md",
                "sha256": file_hash(idea_path),
            },
            {
                "path": "docs/changes/specs/export-data/requirements.md",
                "sha256": file_hash(requirements_path),
            },
        ],
        "verdict": "changes-required",
        "findings": [
            {
                "id": "REQ-R-001",
                "severity": "blocking",
                "resolution": "author-revision",
                "references": ['ADDED Requirement "Export History"'],
                "problem": "The export format is underspecified.",
                "recommendation": "Specify a deterministic export format.",
            }
        ],
    }
    review_path.write_bytes(render_canonical_yaml_mapping(review))

    workflow_inputs = {
        "idea-author": ["request.md"],
        "requirements-author": ["request.md", "idea.md"],
        "design-author": ["request.md", "idea.md", "requirements.md"],
        "planner": ["idea.md", "requirements.md", "design.md"],
    }
    prefix = "docs/changes/specs/export-data"
    for role, names in workflow_inputs.items():
        draft = build_role_manifest_inputs(
            initialized_state, project_root, "export-data", role, "draft"
        )
        revise = build_role_manifest_inputs(
            initialized_state, project_root, "export-data", role, "revise"
        )
        artifact = ROLE_OWNED_ARTIFACTS[role]
        expected_draft = [f"{prefix}/{name}" for name in names] + [
            "docs/product/context.md"
        ]
        expected_revise = [f"{prefix}/{name}" for name in names] + [
            f"{prefix}/{artifact}",
            "docs/product/context.md",
        ]
        assert [item["path"] for item in draft] == expected_draft
        assert [item["path"] for item in revise] == expected_revise
        current = next(item for item in revise if item["path"] == f"{prefix}/{artifact}")
        assert current["sha256"] == file_hash(specification_root / artifact)
        assert all(item["path"] != f"{prefix}/mem-log.md" for item in revise)

    revision_state = copy.deepcopy(initialized_state)
    revision_state.update(
        {
            "stage": "requirements",
            "status": "revising",
            "automatic_revision_attempts": 1,
            "next_run_sequence": 2,
            "approvals": {
                "idea": {
                    "artifact_sha256": file_hash(idea_path),
                    "review_sha256": None,
                    "accepted_risks": [],
                }
            },
            "active_run": {
                "run_id": "export-data--requirements--requirements-author--1",
                "sequence": 1,
                "stage": "requirements",
                "role": "requirements-author",
                "purpose": "revise",
                "executor": "author",
                "adapter": "codex",
                "output": f"{prefix}/requirements.md",
                "request_sha256": file_hash(request_path),
            },
        }
    )
    validate_state_value(revision_state, "export-data")
    revision_inputs = build_role_manifest_inputs(
        revision_state,
        project_root,
        "export-data",
        "requirements-author",
        "revise",
    )
    feedback = {
        "previous_review": {
            "path": f"{prefix}/review/requirements.yaml",
            "sha256": file_hash(review_path),
        },
        "unresolved_finding_ids": ["REQ-R-001"],
    }
    native_manifest = {
        "schema_version": 2,
        "run_id": "export-data--requirements--requirements-author--1",
        "spec_id": "export-data",
        "stage": "requirements",
        "role": "requirements-author",
        "purpose": "revise",
        "project_root": str(project_root.resolve()),
        "skill_root": str(skill_root.resolve()),
        "brief": ROLE_BRIEFS["requirements-author"],
        "resources": role_resource_manifest(skill_root, "requirements-author")[
            "resources"
        ],
        "inputs": revision_inputs,
        "clarifications": [],
        "feedback": feedback,
        "output": f"{prefix}/requirements.md",
        "allowed_write": f"{prefix}/requirements.md",
    }
    assert (
        validate_role_manifest(
            native_manifest, skill_root, project_root, "native", revision_state
        )
        == native_manifest
    )

    mailbox_state = copy.deepcopy(revision_state)
    mailbox_state["execution"]["adapters"]["mailbox"] = {
        "kind": "mailbox",
        "root": str((temporary_root / "mailbox").resolve()),
        "wait_seconds": 15,
    }
    mailbox_state["execution"]["profiles"]["mailbox-author"] = {
        "adapter": "mailbox",
        "model": "company-author-v1",
        "reasoning": "high",
        "project_inputs": copy.deepcopy(
            revision_state["execution"]["profiles"]["author"]["project_inputs"]
        ),
    }
    mailbox_state["execution"]["bindings"]["requirements-author"] = (
        "mailbox-author"
    )
    mailbox_state["active_run"]["executor"] = "mailbox-author"
    mailbox_state["active_run"]["adapter"] = "mailbox"
    validate_state_value(mailbox_state, "export-data")
    mailbox_manifest = {
        **native_manifest,
        "executor": {"model": "company-author-v1", "reasoning": "high"},
    }
    assert mailbox_manifest["inputs"] == native_manifest["inputs"]
    assert (
        validate_role_manifest(
            mailbox_manifest, skill_root, project_root, "mailbox", mailbox_state
        )
        == mailbox_manifest
    )

    wrong_hash_manifest = copy.deepcopy(native_manifest)
    wrong_hash_manifest["inputs"][2]["sha256"] = "sha256:" + "0" * 64
    _self_test_expect_value_error(
        lambda: validate_role_manifest(
            wrong_hash_manifest, skill_root, project_root, "native", revision_state
        ),
        "accepted a revision manifest with the wrong current-output input hash",
    )

    missing_path = requirements_path.with_name("requirements.missing")
    requirements_path.replace(missing_path)
    try:
        _self_test_expect_value_error(
            lambda: validate_role_manifest(
                native_manifest,
                skill_root,
                project_root,
                "native",
                revision_state,
            ),
            "accepted a revision manifest with a missing current artifact",
        )
    finally:
        missing_path.replace(requirements_path)

    mem_log_manifest = copy.deepcopy(native_manifest)
    mem_log_manifest["inputs"][2] = {
        "path": f"{prefix}/mem-log.md",
        "sha256": file_hash(log_path),
    }
    _self_test_expect_value_error(
        lambda: validate_role_manifest(
            mem_log_manifest, skill_root, project_root, "native", revision_state
        ),
        "accepted mem-log.md as a revision manifest input",
    )
    _self_test_expect_value_error(
        lambda: validate_role_manifest(
            {**native_manifest, "allowed_write": f"{prefix}/mem-log.md"},
            skill_root,
            project_root,
            "native",
            revision_state,
        ),
        "accepted mem-log.md as a revision allowed write",
    )

    old_requirements = requirements_path.read_bytes()
    old_stat = requirements_path.stat()
    revised_requirements = artifact_contents["requirements.md"].replace(
        "Only authorized users are in scope.",
        "Only authorized users are in scope; JSON is the deterministic format.",
    ).encode("utf-8")
    _atomic_replace_bytes(
        requirements_path,
        revised_requirements,
        expected_bytes=old_requirements,
        expected_stat=old_stat,
        operation="self-testing revision artifact replacement",
    )
    _self_test_expect_value_error(
        lambda: validate_role_manifest(
            native_manifest, skill_root, project_root, "native", revision_state
        ),
        "accepted a revision manifest after its current artifact changed",
    )

    receipt = {
        "output": {
            "path": f"{prefix}/requirements.md",
            "sha256": file_hash(requirements_path),
        }
    }
    input_arguments = [f"{item['path']}={item['sha256']}" for item in revision_inputs]
    references, result_review, observed_paths = _validate_role_result_files(
        project_root,
        "export-data",
        revision_state,
        receipt,
        input_arguments,
    )
    assert references == {'ADDED Requirement "Export History"'}
    assert result_review is None
    assert observed_paths.count(requirements_path) == 1

    idea_bytes = idea_path.read_bytes()
    idea_path.write_bytes(idea_bytes + b"Changed after dispatch.\n")
    try:
        _self_test_expect_value_error(
            lambda: _validate_role_result_files(
                project_root,
                "export-data",
                revision_state,
                receipt,
                input_arguments,
            ),
            "skipped hash validation for a non-output revision input",
        )
    finally:
        idea_path.write_bytes(idea_bytes)


def self_test_feature_transitions(temporary_root: Path) -> None:
    declared_handlers = {
        "role-outcome",
        "pending-answer",
        "collision-selection",
        "user-revision",
        "approve-stage",
        "checkpoint-outcome",
        "artifact-question",
        "flow-stop",
        "flow-resume",
        "executor-wait",
        "native-interruption",
        "upstream-invalidation",
        "trusted-integrity-pause",
    }
    allowed_modes = {
        "initialize-feature",
        "reserve-run",
        "accept-role-result",
        "workflow-transition",
        "observation",
        "hard-stop",
        "pre-state-observation",
    }
    for name, definition in FEATURE_TRANSITION_TABLE.items():
        assert definition["mode"] in allowed_modes
        assert bool(definition["events"]) == bool(definition["audited"])
        if definition["mode"] == "workflow-transition":
            assert definition["handler"] in declared_handlers
        else:
            assert definition["handler"] is None
        if not definition["audited"]:
            assert require_non_empty_string(
                definition["reason"], f"{name} intentionally-unlogged reason"
            )
        for event_type in definition["events"]:
            kind, event = event_type.split("/", 1)
            assert (kind, event) in AUDIT_EVENT_PAYLOAD_SCHEMAS

    def initialize(
        label: str, *, mailbox: bool = False
    ) -> tuple[Path, Path, Path, str, str]:
        specification = f"phase9-{label}"
        project_root = temporary_root / specification
        project_root.mkdir()
        execution = self_test_execution_snapshot()
        if mailbox:
            mailbox_root = project_root / "mailbox"
            mailbox_root.mkdir()
            execution["adapters"]["mailbox"] = {
                "kind": "mailbox",
                "root": str(mailbox_root.resolve()),
                "wait_seconds": 30,
            }
            execution["profiles"]["mailbox-author"] = {
                "adapter": "mailbox",
                "model": "author-v1",
                "reasoning": "high",
                "project_inputs": [],
            }
            execution["bindings"]["idea-author"] = "mailbox-author"
        result = initialize_feature_specification(
            project_root,
            specification,
            f"Feature transition fixture {label}.",
            execution,
            recorded_at="2026-08-23T12:00:00Z",
        )
        spec_root = project_root / f"docs/changes/specs/{specification}"
        return (
            project_root,
            spec_root / "state.yaml",
            spec_root / "mem-log.md",
            specification,
            str(result["initial_request_sha256"]),
        )

    def reserve_idea(
        label: str, *, mailbox: bool = False
    ) -> tuple[Path, Path, Path, str, str, str]:
        project_root, state_path, log_path, specification, request_hash = initialize(
            label, mailbox=mailbox
        )
        adapter = "mailbox" if mailbox else "codex"
        executor = "mailbox-author" if mailbox else "author"
        reservation = reserve_run_in_state(
            state_path,
            project_root,
            specification,
            "idea",
            "idea-author",
            "draft",
            executor,
            adapter,
            f"docs/changes/specs/{specification}/idea.md",
            request_hash,
        )
        flush_audit_outbox(
            state_path,
            project_root,
            specification,
            recorded_at="2026-08-23T12:01:00Z",
        )
        return (
            project_root,
            state_path,
            log_path,
            specification,
            request_hash,
            str(reservation["run_id"]),
        )

    def accept_idea(
        label: str, *, mailbox: bool = False
    ) -> tuple[Path, Path, Path, str, str]:
        (
            project_root,
            state_path,
            log_path,
            specification,
            request_hash,
            run_id,
        ) = reserve_idea(label, mailbox=mailbox)
        idea_path = project_root / f"docs/changes/specs/{specification}/idea.md"
        idea_path.write_text("# Idea\n\nA validated idea.\n", encoding="utf-8", newline="\n")
        receipt: dict[str, object] = {
            "schema_version": 1,
            "run_id": run_id,
            "status": "completed",
            "output": {
                "path": f"docs/changes/specs/{specification}/idea.md",
                "sha256": file_hash(idea_path),
            },
            "decisions": [],
        }
        if mailbox:
            receipt["executor"] = {
                "requested": {"model": "author-v1", "reasoning": "high"},
                "effective": {"model": "author-v1", "reasoning": "high"},
            }
        accept_completed_role_result_in_state(
            state_path,
            project_root,
            specification,
            receipt,
            [f"docs/changes/specs/{specification}/request.md={request_hash}"],
        )
        flush_audit_outbox(
            state_path,
            project_root,
            specification,
            recorded_at="2026-08-23T12:02:00Z",
        )
        return project_root, state_path, log_path, specification, request_hash

    # Blocked question, atomic mutation/outbox, gate, verbatim answer, and replay.
    project_root, state_path, log_path, specification, _, run_id = reserve_idea(
        "blocked"
    )
    blocked_receipt = {
        "schema_version": 1,
        "run_id": run_id,
        "status": "blocked",
        "question": "Who may export?",
    }
    prefix = log_path.read_bytes()
    result = apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "role-blocked",
        {"receipt": blocked_receipt, "output_sha256_before": None},
    )
    assert result == {
        "schema_version": 1,
        "transition": "role-blocked",
        "status": "queued",
    }
    assert "audit" not in json.dumps(result)
    _, blocked = validate_state_path(state_path, project_root, specification)
    assert blocked["status"] == "awaiting-decision" and blocked["active_run"] is None
    assert blocked["pending"]["request"] == "Who may export?"
    assert [event["event"] for event in blocked["audit"]["outbox"]["events"]] == [
        "role-run-blocked",
        "blocking-question",
    ]
    assert log_path.read_bytes() == prefix
    _self_test_expect_value_error(
        lambda: apply_workflow_transition_in_state(
            state_path,
            project_root,
            specification,
            "flow-stop",
            {"operation_id": "blocked-stop", "verbatim": "stop"},
        ),
        "allowed a transition while its prior outbox was pending",
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:03:00Z"
    )
    event_count = len(validate_audit_log(log_path.read_bytes())["events"])
    assert apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "role-blocked",
        {"receipt": blocked_receipt, "output_sha256_before": None},
    )["status"] == "already-applied"
    assert len(validate_audit_log(log_path.read_bytes())["events"]) == event_count
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "pending-answer",
        {"operation_id": "answer-1", "verbatim": "Workspace administrators only."},
    )
    _, answered = validate_state_path(state_path, project_root, specification)
    assert answered["status"] == "drafting"
    assert answered["pending"]["response"] == "Workspace administrators only."
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:04:00Z"
    )
    assert apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "pending-answer",
        {"operation_id": "answer-1", "verbatim": "Workspace administrators only."},
    )["status"] == "already-applied"

    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "flow-stop",
        {"operation_id": "stop-1", "verbatim": "Stop for now."},
    )
    status_after_recovery = apply_workflow_transition_in_state(
        state_path, project_root, specification, "status", {}
    )
    assert status_after_recovery["status"] == "drafting"
    _, stopped_state = validate_state_path(state_path, project_root, specification)
    assert stopped_state["audit"]["outbox"] is None
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "flow-resume",
        {
            "operation_id": "resume-1",
            "choice": "continue",
            "verbatim": "Continue.",
            "reason": "User resumed the feature flow.",
        },
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:06:00Z"
    )

    # The same persisted flow accepts two sequential author clarifications,
    # first in idea and then in requirements, without using the audit log as a
    # role input.
    _, clarification_state = validate_state_path(
        state_path, project_root, specification
    )
    first_idea_reservation = reserve_run_in_state(
        state_path,
        project_root,
        specification,
        "idea",
        "idea-author",
        "draft",
        "author",
        "codex",
        f"docs/changes/specs/{specification}/idea.md",
        clarification_state["initial_request_sha256"],
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:06:01Z"
    )
    idea_path = project_root / f"docs/changes/specs/{specification}/idea.md"
    idea_path.write_text(
        "# Idea\n\n## Outcome\n\nAdministrators may export.\n",
        encoding="utf-8",
        newline="\n",
    )
    first_answer_key = f"interaction/{specification}/answer/answer-1"
    accept_completed_role_result_in_state(
        state_path,
        project_root,
        specification,
        {
            "schema_version": 1,
            "run_id": first_idea_reservation["run_id"],
            "status": "completed",
            "output": {
                "path": f"docs/changes/specs/{specification}/idea.md",
                "sha256": file_hash(idea_path),
            },
            "decisions": [
                {
                    "key": "IDEA-001",
                    "authority": "user",
                    "kind": "framing",
                    "summary": "Limit export to workspace administrators.",
                    "rationale": "The user supplied that audience explicitly.",
                    "alternatives": [],
                    "references": [],
                    "source_event_keys": [first_answer_key],
                }
            ],
        },
        [
            f"docs/changes/specs/{specification}/request.md="
            f"{clarification_state['initial_request_sha256']}"
        ],
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:06:02Z"
    )
    _, idea_clarified = validate_state_path(state_path, project_root, specification)
    assert len(idea_clarified["clarifications"]) == 1
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "approve-stage",
        {
            "stage": "idea",
            "action": "continue",
            "verbatim": "Continue.",
            "accepted_risks": [],
        },
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:06:03Z"
    )
    _, requirements_state = validate_state_path(
        state_path, project_root, specification
    )
    requirements_reservation = reserve_run_in_state(
        state_path,
        project_root,
        specification,
        "requirements",
        "requirements-author",
        "draft",
        "author",
        "codex",
        f"docs/changes/specs/{specification}/requirements.md",
        requirements_state["initial_request_sha256"],
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:06:04Z"
    )
    second_question = "Should exports include deleted transactions?"
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "role-blocked",
        {
            "receipt": {
                "schema_version": 1,
                "run_id": requirements_reservation["run_id"],
                "status": "blocked",
                "question": second_question,
            },
            "output_sha256_before": None,
        },
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:06:05Z"
    )
    second_answer = "Exclude deleted transactions."
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "pending-answer",
        {"operation_id": "answer-2", "verbatim": second_answer},
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:06:06Z"
    )
    _, second_answer_state = validate_state_path(
        state_path, project_root, specification
    )
    second_requirements_reservation = reserve_run_in_state(
        state_path,
        project_root,
        specification,
        "requirements",
        "requirements-author",
        "draft",
        "author",
        "codex",
        f"docs/changes/specs/{specification}/requirements.md",
        second_answer_state["initial_request_sha256"],
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:06:07Z"
    )
    requirements_path = (
        project_root / f"docs/changes/specs/{specification}/requirements.md"
    )
    requirement_reference = 'ADDED Requirement "Transaction export"'
    requirements_path.write_text(
        """# Export Specification Delta
## Purpose
Allow administrators to export transaction history.
## ADDED Requirements
### Requirement: Transaction export
The system SHALL export only non-deleted transactions for an administrator.
#### Scenario: Successful export
- **WHEN** an administrator requests an export
- **THEN** the system returns non-deleted transactions
## Boundaries and assumptions
Deleted transactions are excluded.
""",
        encoding="utf-8",
        newline="\n",
    )
    accept_completed_role_result_in_state(
        state_path,
        project_root,
        specification,
        {
            "schema_version": 1,
            "run_id": second_requirements_reservation["run_id"],
            "status": "completed",
            "output": {
                "path": f"docs/changes/specs/{specification}/requirements.md",
                "sha256": file_hash(requirements_path),
            },
            "decisions": [
                {
                    "key": requirement_reference,
                    "authority": "user",
                    "kind": "scope",
                    "summary": "Exclude deleted transactions from exports.",
                    "rationale": "The user explicitly selected that scope.",
                    "alternatives": [],
                    "references": [requirement_reference],
                    "source_event_keys": [
                        f"interaction/{specification}/answer/answer-2"
                    ],
                }
            ],
        },
        [
            f"docs/changes/specs/{specification}/request.md="
            f"{second_answer_state['initial_request_sha256']}",
            f"docs/changes/specs/{specification}/idea.md={file_hash(idea_path)}",
        ],
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:06:08Z"
    )
    _, twice_clarified = validate_state_path(
        state_path, project_root, specification
    )
    assert twice_clarified["clarifications"] == [
        {
            "stage": "idea",
            "origin": "author",
            "question": "Who may export?",
            "answer": "Workspace administrators only.",
        },
        {
            "stage": "requirements",
            "origin": "author",
            "question": second_question,
            "answer": second_answer,
        },
    ]

    # Valid failure and native interruption/recovery preserve deterministic run state.
    project_root, state_path, _, specification, _, run_id = reserve_idea("failed")
    failed_receipt = {
        "schema_version": 1,
        "run_id": run_id,
        "status": "failed",
        "error": "Executor failed.",
    }
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "role-failed",
        {"receipt": failed_receipt, "output_sha256_before": None},
    )
    _, failed = validate_state_path(state_path, project_root, specification)
    assert failed["status"] == "drafting" and failed["active_run"] is None

    project_root, state_path, _, specification, _, run_id = reserve_idea("native")
    native_state_before = state_path.read_bytes()
    assert apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "native-response-malformed",
        {},
    )["status"] == "preserved"
    assert state_path.read_bytes() == native_state_before
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "native-run-interrupted",
        {"run_id": run_id, "reason": "Host run is no longer inspectable."},
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:07:00Z"
    )
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "native-run-recovery",
        {
            "operation_id": "native-retry-1",
            "choice": "abandon-and-retry",
            "verbatim": "Retry.",
            "reason": "User selected a fresh run.",
        },
    )
    _, recovering = validate_state_path(state_path, project_root, specification)
    assert recovering["active_run"] is None and recovering["status"] == "drafting"

    # Mailbox timeout, malformed response, and completion each end a durable wait
    # before the completed result is accepted through the shared result primitive.
    project_root, state_path, _, specification, request_hash, run_id = reserve_idea(
        "mailbox", mailbox=True
    )
    for wait_id, end_name, detail in (
        ("wait-1", "mailbox-wait-timeout", "No response before the bound."),
        ("wait-2", "mailbox-response-malformed", "Invalid response JSON."),
        ("wait-3", "mailbox-wait-interrupted", "Mailbox polling was interrupted."),
        ("wait-4", "mailbox-wait-completed", None),
    ):
        apply_workflow_transition_in_state(
            state_path,
            project_root,
            specification,
            "mailbox-wait-started",
            {"run_id": run_id, "wait_id": wait_id, "wait_seconds": 30},
        )
        flush_audit_outbox(
            state_path,
            project_root,
            specification,
            recorded_at="2026-08-23T12:08:00Z",
        )
        apply_workflow_transition_in_state(
            state_path,
            project_root,
            specification,
            end_name,
            {"run_id": run_id, "wait_id": wait_id, "detail": detail},
        )
        flush_audit_outbox(
            state_path,
            project_root,
            specification,
            recorded_at="2026-08-23T12:09:00Z",
        )
    _, mailbox_ready = validate_state_path(state_path, project_root, specification)
    assert mailbox_ready["status"] == "drafting" and mailbox_ready["active_run"] is not None
    idea_path = project_root / f"docs/changes/specs/{specification}/idea.md"
    idea_path.write_text("# Idea\n\nMailbox result.\n", encoding="utf-8", newline="\n")
    accept_completed_role_result_in_state(
        state_path,
        project_root,
        specification,
        {
            "schema_version": 1,
            "run_id": run_id,
            "status": "completed",
            "output": {
                "path": f"docs/changes/specs/{specification}/idea.md",
                "sha256": file_hash(idea_path),
            },
            "decisions": [],
            "executor": {
                "requested": {"model": "author-v1", "reasoning": "high"},
                "effective": {"model": "author-v1", "reasoning": "high"},
            },
        },
        [f"docs/changes/specs/{specification}/request.md={request_hash}"],
    )

    # Artifact question, ordinary approval, durable commit intent, failure retry,
    # and success mutation all preserve product truth in state.
    project_root, state_path, log_path, specification, _ = accept_idea("approval")
    before_state = state_path.read_bytes()
    before_log = log_path.read_bytes()
    _, product_before_question = validate_state_path(
        state_path, project_root, specification
    )
    product_before_question = {
        key: copy.deepcopy(value)
        for key, value in product_before_question.items()
        if key != "audit"
    }
    status_result = apply_workflow_transition_in_state(
        state_path, project_root, specification, "status", {}
    )
    assert status_result["stage"] == "idea" and status_result["pending"] is False
    assert state_path.read_bytes() == before_state and log_path.read_bytes() == before_log
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "artifact-question",
        {
            "operation_id": "question-1",
            "question": "Does this include administrators?",
            "answer": "Yes, as stated in the idea artifact.",
        },
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:10:00Z"
    )
    _, product_after_question = validate_state_path(
        state_path, project_root, specification
    )
    assert {
        key: value
        for key, value in product_after_question.items()
        if key != "audit"
    } == product_before_question
    question_events = validate_audit_log(log_path.read_bytes())["events"][-2:]
    assert [event["event"] for event in question_events] == [
        "user-question",
        "agent-answer",
    ]
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "approve-stage",
        {"stage": "idea", "action": "continue", "verbatim": "Continue", "accepted_risks": []},
    )
    _, advanced = validate_state_path(state_path, project_root, specification)
    assert advanced["stage"] == "requirements" and set(advanced["approvals"]) == {"idea"}
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:11:00Z"
    )
    assert apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "approve-stage",
        {"stage": "idea", "action": "continue", "verbatim": "Continue", "accepted_risks": []},
    )["status"] == "already-applied"

    project_root, state_path, _, specification, _ = accept_idea("commit")
    selection_details = {
        "stage": "idea",
        "action": "continue-and-commit",
        "verbatim": "Continue and commit",
        "accepted_risks": [],
    }
    apply_workflow_transition_in_state(
        state_path, project_root, specification, "approve-stage", selection_details
    )
    _, selected = validate_state_path(state_path, project_root, specification)
    selection_key = selected["checkpoint_commit"]["selection_event_key"]
    assert selected["stage"] == "idea" and selected["status"] == "awaiting-approval"
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:12:00Z"
    )
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "checkpoint-commit-failed",
        {
            "selection_event_key": selection_key,
            "attempt_id": "attempt-1",
            "error": "Hook rejected the commit.",
        },
    )
    _, commit_failed = validate_state_path(state_path, project_root, specification)
    assert commit_failed["checkpoint_commit"] is not None
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:13:00Z"
    )
    assert apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "checkpoint-commit-failed",
        {
            "selection_event_key": selection_key,
            "attempt_id": "attempt-1",
            "error": "Hook rejected the commit.",
        },
    )["status"] == "already-applied"
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "checkpoint-commit-succeeded",
        {"selection_event_key": selection_key},
    )
    _, committed = validate_state_path(state_path, project_root, specification)
    assert committed["checkpoint_commit"] is None and committed["stage"] == "requirements"

    # Collision normalization and explicit user revision are independent audited
    # interactions with stable operation IDs.
    project_root, state_path, _, specification, _ = initialize("collision")
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "collision-selection",
        {
            "operation_id": "collision-1",
            "choice": specification,
            "verbatim": "Create the numbered specification.",
        },
    )
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:14:00Z"
    )
    project_root, state_path, _, specification, _ = accept_idea("user-revision")
    revision_feedback = "Добавить владельцев рабочих областей.\nСохранить перенос строки."
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "user-revision",
        {"operation_id": "revision-1", "verbatim": revision_feedback},
    )
    _, revision_state = validate_state_path(state_path, project_root, specification)
    assert revision_state["status"] == "revising"
    assert revision_state["pending"] == {
        "kind": "revision",
        "origin": "router",
        "stage": "idea",
        "request": USER_REVISION_PENDING_REQUEST,
        "response": revision_feedback,
        "resume_purpose": "revise",
    }
    flush_audit_outbox(
        state_path,
        project_root,
        specification,
        recorded_at="2026-08-23T12:14:30Z",
    )
    revision_events = validate_audit_log(
        project_root.joinpath(
            *PurePosixPath(
                f"docs/changes/specs/{specification}/mem-log.md"
            ).parts
        ).read_bytes()
    )["events"]
    revision_event = _audit_event_by_key(
        revision_events, f"interaction/{specification}/revision/revision-1"
    )
    assert revision_event is not None
    assert revision_event["payload"]["verbatim"] == revision_feedback
    revision_reservation = reserve_run_in_state(
        state_path,
        project_root,
        specification,
        "idea",
        "idea-author",
        "revise",
        "author",
        "codex",
        f"docs/changes/specs/{specification}/idea.md",
        revision_state["initial_request_sha256"],
    )
    flush_audit_outbox(
        state_path,
        project_root,
        specification,
        recorded_at="2026-08-23T12:14:31Z",
    )
    _, revision_run_state = validate_state_path(
        state_path, project_root, specification
    )
    revision_inputs = build_role_manifest_inputs(
        revision_run_state,
        project_root,
        specification,
        "idea-author",
        "revise",
    )
    revision_manifest = {
        "schema_version": 2,
        "run_id": revision_reservation["run_id"],
        "spec_id": specification,
        "stage": "idea",
        "role": "idea-author",
        "purpose": "revise",
        "project_root": str(project_root.resolve()),
        "skill_root": str(Path(__file__).resolve().parent.parent),
        "brief": ROLE_BRIEFS["idea-author"],
        "resources": role_resource_manifest(
            Path(__file__).resolve().parent.parent, "idea-author"
        )["resources"],
        "inputs": revision_inputs,
        "clarifications": revision_run_state["clarifications"],
        "feedback": {
            "pending_response": revision_feedback,
            "revision_request": revision_feedback,
        },
        "output": f"docs/changes/specs/{specification}/idea.md",
        "allowed_write": f"docs/changes/specs/{specification}/idea.md",
    }
    assert validate_role_manifest(
        revision_manifest,
        Path(__file__).resolve().parent.parent,
        project_root,
        "native",
        revision_run_state,
    ) == revision_manifest
    invalid_revision_manifest = copy.deepcopy(revision_manifest)
    invalid_revision_manifest["feedback"]["revision_request"] = "different"
    _self_test_expect_value_error(
        lambda: validate_role_manifest(
            invalid_revision_manifest,
            Path(__file__).resolve().parent.parent,
            project_root,
            "native",
            revision_run_state,
        ),
        "accepted revision feedback not bound to primary state",
    )

    def write_stage_fixture_files(
        project_root: Path, specification: str, *, blocking_requirements: bool
    ) -> tuple[Path, Path, Path, Path, Path, Path]:
        spec_root = project_root / f"docs/changes/specs/{specification}"
        idea = spec_root / "idea.md"
        requirements = spec_root / "requirements.md"
        design = spec_root / "design.md"
        plan = spec_root / "plan.md"
        idea.write_text("# Idea\n\nFixture.\n", encoding="utf-8", newline="\n")
        requirements.write_text("# Requirements\n\nFixture.\n", encoding="utf-8", newline="\n")
        design.write_text("# Design\n\nFixture.\n", encoding="utf-8", newline="\n")
        plan.write_text("# Plan\n\nFixture.\n", encoding="utf-8", newline="\n")
        review_root = spec_root / "review"
        review_root.mkdir(exist_ok=True)
        requirements_review = review_root / "requirements.yaml"
        requirements_findings = (
            [
                {
                    "id": "REQ-R-001",
                    "severity": "blocking",
                    "resolution": "author-revision",
                    "references": ["Requirement Export"],
                    "problem": "Export volume may be high.",
                    "recommendation": "Accept or revise the risk.",
                }
            ]
            if blocking_requirements
            else []
        )
        requirements_review.write_bytes(
            render_canonical_yaml_mapping(
                {
                    "schema_version": 2,
                    "stage": "requirements",
                    "inputs": [],
                    "verdict": (
                        "changes-required" if blocking_requirements else "pass"
                    ),
                    "findings": requirements_findings,
                }
            )
        )
        design_review = review_root / "design.yaml"
        design_review.write_bytes(
            render_canonical_yaml_mapping(
                {
                    "schema_version": 2,
                    "stage": "design",
                    "inputs": [],
                    "verdict": "pass",
                    "findings": [],
                }
            )
        )
        return idea, requirements, design, plan, requirements_review, design_review

    # Risk acceptance must exactly cover blocking author-revision findings.
    project_root, state_path, _, specification, _ = initialize("risk")
    idea, requirements, _, _, requirements_review, _ = write_stage_fixture_files(
        project_root, specification, blocking_requirements=True
    )
    _, risk_state = validate_state_path(state_path, project_root, specification)
    risk_state.update(
        {
            "stage": "requirements",
            "status": "awaiting-approval",
            "approvals": {
                "idea": {
                    "artifact_sha256": file_hash(idea),
                    "review_sha256": None,
                    "accepted_risks": [],
                }
            },
        }
    )
    risk_events = [
        build_queued_audit_event(
            event_key=f"run/{specification}--requirements--requirements-author--1/artifact-accepted",
            kind="lifecycle",
            event="artifact-accepted",
            stage="requirements",
            actor="router",
            run_id=f"{specification}--requirements--requirements-author--1",
            payload={
                "path": f"docs/changes/specs/{specification}/requirements.md",
                "sha256": file_hash(requirements),
                "receipt_sha256": "sha256:" + "a" * 64,
            },
        ),
        build_queued_audit_event(
            event_key=f"run/{specification}--requirements--requirements-reviewer--2/review-accepted",
            kind="lifecycle",
            event="review-accepted",
            stage="requirements",
            actor="router",
            run_id=f"{specification}--requirements--requirements-reviewer--2",
            payload={
                "path": f"docs/changes/specs/{specification}/review/requirements.yaml",
                "sha256": file_hash(requirements_review),
                "receipt_sha256": "sha256:" + "b" * 64,
                "verdict": "changes-required",
                "finding_ids": ["REQ-R-001"],
            },
        ),
    ]
    risk_state = queue_audit_transaction(risk_state, specification, risk_events)
    state_path.write_bytes(render_canonical_yaml_mapping(risk_state))
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:15:00Z"
    )
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "approve-stage",
        {
            "stage": "requirements",
            "action": "continue",
            "verbatim": "Accept REQ-R-001 and continue.",
            "accepted_risks": ["REQ-R-001"],
        },
    )
    _, risk_approved = validate_state_path(state_path, project_root, specification)
    assert risk_approved["approvals"]["requirements"]["accepted_risks"] == [
        "REQ-R-001"
    ]
    assert [
        event["event"] for event in risk_approved["audit"]["outbox"]["events"]
    ] == ["risk-accepted", "stage-approved", "stage-entered"]

    # Plan approval emits final completion in the same mutation; a later
    # upstream defect invalidates the affected approval suffix without versions.
    project_root, state_path, _, specification, _ = initialize("complete")
    idea, requirements, design, plan, requirements_review, design_review = (
        write_stage_fixture_files(
            project_root, specification, blocking_requirements=False
        )
    )
    _, completion_state = validate_state_path(state_path, project_root, specification)
    completion_state.update(
        {
            "stage": "plan",
            "status": "awaiting-approval",
            "approvals": {
                "idea": {
                    "artifact_sha256": file_hash(idea),
                    "review_sha256": None,
                    "accepted_risks": [],
                },
                "requirements": {
                    "artifact_sha256": file_hash(requirements),
                    "review_sha256": file_hash(requirements_review),
                    "accepted_risks": [],
                },
                "design": {
                    "artifact_sha256": file_hash(design),
                    "review_sha256": file_hash(design_review),
                    "accepted_risks": [],
                },
            },
        }
    )
    completion_state = queue_audit_transaction(
        completion_state,
        specification,
        [
            build_queued_audit_event(
                event_key=f"run/{specification}--plan--planner--1/artifact-accepted",
                kind="lifecycle",
                event="artifact-accepted",
                stage="plan",
                actor="router",
                run_id=f"{specification}--plan--planner--1",
                payload={
                    "path": f"docs/changes/specs/{specification}/plan.md",
                    "sha256": file_hash(plan),
                    "receipt_sha256": "sha256:" + "c" * 64,
                },
            )
        ],
    )
    state_path.write_bytes(render_canonical_yaml_mapping(completion_state))
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:16:00Z"
    )
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "approve-stage",
        {"stage": "plan", "action": "continue", "verbatim": "Approve", "accepted_risks": []},
    )
    _, completed = validate_state_path(state_path, project_root, specification)
    assert completed["status"] == "approved"
    assert [event["event"] for event in completed["audit"]["outbox"]["events"]] == [
        "stage-approved",
        "workflow-completed",
    ]
    flush_audit_outbox(
        state_path, project_root, specification, recorded_at="2026-08-23T12:17:00Z"
    )
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "upstream-invalidation",
        {
            "operation_id": "upstream-1",
            "to_stage": "requirements",
            "reason": "Plan review exposed an upstream requirement defect.",
            "verbatim": "Return to requirements.",
        },
    )
    _, invalidated = validate_state_path(state_path, project_root, specification)
    assert invalidated["stage"] == "requirements" and invalidated["status"] == "revising"
    assert set(invalidated["approvals"]) == {"idea"}

    # Trusted integrity evidence is audited; untrusted log damage is a hard stop
    # with no attempted state mutation or append.
    project_root, state_path, log_path, specification, _ = initialize("integrity")
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "trusted-integrity-pause",
        {
            "operation_id": "integrity-1",
            "problem": "Pinned input changed.",
            "affected_paths": [f"docs/changes/specs/{specification}/request.md"],
            "question": "Should the changed input be accepted?",
        },
    )
    _, paused = validate_state_path(state_path, project_root, specification)
    assert paused["status"] == "awaiting-decision" and paused["pending"] is not None

    project_root, state_path, log_path, specification, _ = initialize("tamper")
    state_before = state_path.read_bytes()
    log_path.write_bytes(log_path.read_bytes() + b"manual append\n")
    damaged = log_path.read_bytes()
    _self_test_expect_value_error(
        lambda: apply_workflow_transition_in_state(
            state_path,
            project_root,
            specification,
            "flow-stop",
            {"operation_id": "must-not-write", "verbatim": None},
        ),
        "continued a workflow with an untrusted audit log",
    )
    assert state_path.read_bytes() == state_before and log_path.read_bytes() == damaged

    # Pure completed-review composition covers pass, user decision, automatic
    # revision, and the third automatic failure without a second transition.
    def reviewer_fixture(
        attempts: int, resolution: str, verdict: str = "changes-required"
    ) -> tuple[dict[str, object], dict[str, object], dict[str, object], list[dict[str, object]]]:
        _, initial = _initial_feature_files(
            "phase9-review",
            "Review transition fixture.",
            self_test_execution_snapshot(),
            "2026-08-23T12:00:00Z",
        )
        initial["stage"] = "requirements"
        initial["status"] = "reviewing"
        initial["automatic_revision_attempts"] = attempts
        initial["next_run_sequence"] = 2
        initial["approvals"] = {
            "idea": {
                "artifact_sha256": "sha256:" + "a" * 64,
                "review_sha256": None,
                "accepted_risks": [],
            }
        }
        run_id = "phase9-review--requirements--requirements-reviewer--1"
        initial["active_run"] = {
            "run_id": run_id,
            "sequence": 1,
            "stage": "requirements",
            "role": "requirements-reviewer",
            "purpose": "review",
            "executor": "author",
            "adapter": "codex",
            "output": "docs/changes/specs/phase9-review/review/requirements.yaml",
            "request_sha256": initial["initial_request_sha256"],
        }
        findings = []
        decisions = [
            {
                "key": "VERDICT",
                "authority": "agent",
                "kind": "verdict",
                "summary": verdict,
                "rationale": "Validated review verdict.",
                "alternatives": [],
                "references": [],
                "source_event_keys": [],
            }
        ]
        if verdict == "changes-required":
            finding = {
                "id": "REQ-R-001",
                "severity": "blocking",
                "resolution": resolution,
                "references": ["Requirement Export"],
                "problem": "The export audience is unclear.",
                "recommendation": "Specify the allowed audience.",
            }
            findings.append(finding)
            decisions.append(
                {
                    "key": "REQ-R-001",
                    "authority": "agent",
                    "kind": "finding",
                    "summary": finding["problem"],
                    "rationale": finding["recommendation"],
                    "alternatives": [],
                    "references": list(finding["references"]),
                    "source_event_keys": [],
                }
            )
        review = {
            "schema_version": 2,
            "stage": "requirements",
            "inputs": [],
            "verdict": verdict,
            "findings": findings,
        }
        receipt = {
            "schema_version": 1,
            "run_id": run_id,
            "status": "completed",
            "output": {
                "path": "docs/changes/specs/phase9-review/review/requirements.yaml",
                "sha256": "sha256:" + "b" * 64,
            },
            "decisions": decisions,
        }
        reservation = seal_audit_event(
            {
                "event_id": "MEM-000003",
                "event_key": f"run/{run_id}/reserved",
                "recorded_at": "2026-08-23T12:00:00Z",
                "kind": "lifecycle",
                "event": "role-run-reserved",
                "stage": "requirements",
                "actor": "router",
                "run_id": run_id,
                "related_events": [],
                "previous_event_sha256": "sha256:" + "c" * 64,
                "payload": {
                    "purpose": "review",
                    "profile": "author",
                    "adapter": "codex",
                    "configured_agent": "stepan_author",
                    "requested_model": None,
                    "requested_reasoning": None,
                    "output_path": receipt["output"]["path"],
                },
            }
        )
        validate_state_value(initial, "phase9-review")
        return initial, receipt, review, [reservation]

    pass_state, pass_receipt, pass_review, pass_events = reviewer_fixture(
        0, "none", "pass"
    )
    passed, _ = prepare_completed_role_result_state(
        pass_state,
        "phase9-review",
        pass_receipt,
        artifact_references=set(),
        review=pass_review,
        durable_events=pass_events,
    )
    assert passed["status"] == "awaiting-approval"
    automatic_state, automatic_receipt, automatic_review, automatic_events = reviewer_fixture(
        0, "author-revision"
    )
    automatic, _ = prepare_completed_role_result_state(
        automatic_state,
        "phase9-review",
        automatic_receipt,
        artifact_references=set(),
        review=automatic_review,
        durable_events=automatic_events,
    )
    assert automatic["status"] == "revising"
    assert automatic["automatic_revision_attempts"] == 1
    limit_state, limit_receipt, limit_review, limit_events = reviewer_fixture(
        2, "author-revision"
    )
    limited, _ = prepare_completed_role_result_state(
        limit_state,
        "phase9-review",
        limit_receipt,
        artifact_references=set(),
        review=limit_review,
        durable_events=limit_events,
        review_question="How should the remaining finding be handled?",
    )
    assert limited["status"] == "awaiting-decision"
    assert limited["automatic_revision_attempts"] == 3
    assert [event["event"] for event in limited["audit"]["outbox"]["events"]][:3] == [
        "role-run-completed",
        "automatic-revision-limit-reached",
        "blocking-question",
    ]
    decision_state, decision_receipt, decision_review, decision_events = reviewer_fixture(
        0, "user-decision"
    )
    decision_pending, _ = prepare_completed_role_result_state(
        decision_state,
        "phase9-review",
        decision_receipt,
        artifact_references=set(),
        review=decision_review,
        durable_events=decision_events,
        review_question="Which audience should be allowed to export?",
    )
    assert decision_pending["pending"]["origin"] == "review"


def self_test_feature_audit_e2e(temporary_root: Path, skill_root: Path) -> None:
    """Exercise one complete feature through the public mutation boundaries."""

    project_root = temporary_root / "feature-audit-e2e"
    project_root.mkdir()
    specification = "e2e-export"
    initialization = initialize_feature_specification(
        project_root,
        specification,
        "Export transaction history for workspace administrators.",
        self_test_execution_snapshot(),
        recorded_at="2026-08-24T10:00:00Z",
    )
    spec_root = project_root / f"docs/changes/specs/{specification}"
    state_path = spec_root / "state.yaml"
    log_path = spec_root / "mem-log.md"
    request_hash = str(initialization["initial_request_sha256"])
    expected_timeline = [
        "initial-request-captured",
        "specification-created",
        "stage-entered",
    ]
    accepted_prefixes = [log_path.read_bytes()]
    flush_number = 0

    def current_state() -> dict[str, object]:
        _, value = validate_state_path(state_path, project_root, specification)
        return value

    def flush_expected(expected_events: list[str]) -> list[dict[str, object]]:
        nonlocal flush_number
        before = log_path.read_bytes()
        before_log = validate_audit_log(before)
        flush_number += 1
        result = flush_audit_outbox(
            state_path,
            project_root,
            specification,
            recorded_at=f"2026-08-24T10:{flush_number:02d}:00Z",
        )
        assert result["status"] == "flushed"
        after = log_path.read_bytes()
        assert after.startswith(before) and len(after) > len(before)
        accepted_prefixes.append(after)
        parsed = validate_audit_log(after)
        appended = parsed["events"][len(before_log["events"]) :]
        assert [event["event"] for event in appended] == expected_events
        assert result["event_ids"] == [event["event_id"] for event in appended]
        expected_timeline.extend(expected_events)
        state = current_state()
        audit = state["audit"]
        assert audit["outbox"] is None
        assert audit["log_size_bytes"] == len(after)
        assert audit["log_sha256"] == audit_bytes_sha256(after)
        assert audit["head_event_id"] == parsed["head_event_id"]
        assert audit["head_event_sha256"] == parsed["head_event_sha256"]
        assert audit["next_event_sequence"] == parsed["next_event_sequence"]
        return appended

    def reserve_and_validate_manifest(
        stage: str, role: str, purpose: str
    ) -> tuple[str, dict[str, object]]:
        state = current_state()
        profile_name = str(state["execution"]["bindings"][role])
        profile = state["execution"]["profiles"][profile_name]
        adapter = str(state["execution"]["adapters"][profile["adapter"]]["kind"])
        output = expected_output_path(specification, stage, role)
        reservation = reserve_run_in_state(
            state_path,
            project_root,
            specification,
            stage,
            role,
            purpose,
            profile_name,
            adapter,
            output,
            request_hash,
        )
        flush_expected(["role-run-reserved"])
        reserved = current_state()
        inputs = build_role_manifest_inputs(
            reserved, project_root, specification, role, purpose
        )
        manifest: dict[str, object] = {
            "schema_version": 2,
            "run_id": reservation["run_id"],
            "spec_id": specification,
            "stage": stage,
            "role": role,
            "purpose": purpose,
            "project_root": str(project_root.resolve()),
            "skill_root": str(skill_root.resolve()),
            "brief": ROLE_BRIEFS[role],
            "resources": role_resource_manifest(skill_root, role)["resources"],
            "inputs": inputs,
            "clarifications": copy.deepcopy(reserved["clarifications"]),
            "output": output,
            "allowed_write": output,
        }
        adapter_class = "mailbox" if adapter == "mailbox" else "native"
        if adapter_class == "mailbox":
            executor = {"model": profile["model"]}
            if "reasoning" in profile:
                executor["reasoning"] = profile["reasoning"]
            manifest["executor"] = executor
        assert (
            validate_role_manifest(
                manifest, skill_root, project_root, adapter_class, reserved
            )
            == manifest
        )
        assert all(
            not str(item["path"]).endswith("/mem-log.md") for item in inputs
        )
        assert manifest["allowed_write"] != (
            f"docs/changes/specs/{specification}/mem-log.md"
        )
        return str(reservation["run_id"]), manifest

    def accept_result(
        run_id: str,
        manifest: dict[str, object],
        *,
        content: str | None,
        review: dict[str, object] | None,
        decisions: list[dict[str, object]],
        expected_events: list[str],
    ) -> dict[str, object]:
        output_path = project_root.joinpath(
            *PurePosixPath(str(manifest["output"])).parts
        )
        output_path.parent.mkdir(parents=True, exist_ok=True)
        if review is None:
            assert content is not None
            output_path.write_text(content, encoding="utf-8", newline="\n")
        else:
            assert content is None
            output_path.write_bytes(render_canonical_yaml_mapping(review))
        receipt = {
            "schema_version": 1,
            "run_id": run_id,
            "status": "completed",
            "output": {
                "path": manifest["output"],
                "sha256": file_hash(output_path),
            },
            "decisions": decisions,
        }
        assert (
            validate_receipt(
                receipt, run_id, str(manifest["output"]), "native"
            )
            == receipt
        )
        before = log_path.read_bytes()
        input_arguments = [
            f"{item['path']}={item['sha256']}" for item in manifest["inputs"]
        ]
        result = accept_completed_role_result_in_state(
            state_path,
            project_root,
            specification,
            receipt,
            input_arguments,
        )
        assert log_path.read_bytes() == before
        queued = current_state()
        assert queued["active_run"] is None
        outbox_events = queued["audit"]["outbox"]["events"]
        assert [event["event"] for event in outbox_events] == expected_events
        assert outbox_events == sorted(outbox_events, key=_audit_event_sort_key)
        assert result["receipt_sha256"] == canonical_receipt_sha256(receipt)
        flush_expected(expected_events)
        return receipt

    def approve(stage: str, expected_events: list[str]) -> None:
        before = log_path.read_bytes()
        result = apply_workflow_transition_in_state(
            state_path,
            project_root,
            specification,
            "approve-stage",
            {
                "stage": stage,
                "action": "continue",
                "verbatim": f"Approve {stage}.",
                "accepted_risks": [],
            },
        )
        assert result["status"] == "queued"
        assert log_path.read_bytes() == before
        flush_expected(expected_events)

    idea_body_marker = "IDEA-BODY-IS-NOT-AUDIT-HISTORY"
    idea_run, idea_manifest = reserve_and_validate_manifest(
        "idea", "idea-author", "draft"
    )
    idea_receipt = accept_result(
        idea_run,
        idea_manifest,
        content=f"# Idea\n\n## Outcome\n\n{idea_body_marker}\n",
        review=None,
        decisions=[],
        expected_events=["role-run-completed", "artifact-accepted"],
    )
    approve("idea", ["stage-approved", "stage-entered"])
    assert current_state()["stage"] == "requirements"

    requirement_reference = 'ADDED Requirement "Transaction export"'
    requirements_run, requirements_manifest = reserve_and_validate_manifest(
        "requirements", "requirements-author", "draft"
    )
    requirements_content = """# Export Specification Delta
## Purpose
Allow workspace administrators to export transaction history.
## ADDED Requirements
### Requirement: Transaction export
The system SHALL let a workspace administrator export transaction history.
#### Scenario: Successful export
- **WHEN** a workspace administrator requests an export
- **THEN** the system returns the transaction history
## Boundaries and assumptions
Only workspace administrators are in scope.
"""
    requirements_decision = {
        "key": requirement_reference,
        "authority": "agent",
        "kind": "behavior",
        "summary": "Expose transaction-history export to administrators.",
        "rationale": "The approved idea limits export to that audience.",
        "alternatives": [],
        "references": [requirement_reference],
        "source_event_keys": [],
    }
    requirements_receipt = accept_result(
        requirements_run,
        requirements_manifest,
        content=requirements_content,
        review=None,
        decisions=[requirements_decision],
        expected_events=[
            "role-run-completed",
            "artifact-accepted",
            "agent-decision-introduced",
        ],
    )

    requirements_review_run, requirements_review_manifest = (
        reserve_and_validate_manifest(
            "requirements", "requirements-reviewer", "review"
        )
    )
    requirements_review = {
        "schema_version": 2,
        "stage": "requirements",
        "inputs": copy.deepcopy(requirements_review_manifest["inputs"]),
        "verdict": "pass",
        "findings": [],
    }
    requirements_verdict = {
        "key": "VERDICT",
        "authority": "agent",
        "kind": "verdict",
        "summary": "Requirements pass review.",
        "rationale": "The delta is complete, testable, and traceable.",
        "alternatives": [],
        "references": [],
        "source_event_keys": [],
    }
    requirements_review_receipt = accept_result(
        requirements_review_run,
        requirements_review_manifest,
        content=None,
        review=requirements_review,
        decisions=[requirements_verdict],
        expected_events=[
            "role-run-completed",
            "review-accepted",
            "agent-decision-introduced",
        ],
    )
    approve("requirements", ["stage-approved", "stage-entered"])
    assert current_state()["stage"] == "design"

    design_run, design_manifest = reserve_and_validate_manifest(
        "design", "design-author", "draft"
    )
    design_content = f"""# Design
## Overview
Add a bounded asynchronous export service.
## Decisions
### DES-001 — Asynchronous export job
Covers: {requirement_reference}
Decision: Queue a bounded export job.
Rationale: Export duration may exceed a request lifetime.
### DES-002 — Administrator authorization
Covers: {requirement_reference}
Decision: Check workspace-administrator authorization before enqueueing.
Rationale: The requirement limits the eligible audience.
## Affected components
Export API, worker, and authorization service.
## Constraints, risks and trade-offs
Export jobs require bounded retention and retry behavior.
## Verification
Verify DES-001 and DES-002 with integration tests.
"""
    rejected_alternative = {
        "option": "Generate every export synchronously.",
        "rejected_because": "Large exports may exceed the request timeout.",
    }
    design_decisions = [
        {
            "key": "DES-001",
            "authority": "agent",
            "kind": "technical",
            "summary": "Queue a bounded asynchronous export job.",
            "rationale": "Export duration may exceed a request lifetime.",
            "alternatives": [rejected_alternative],
            "references": ["DES-001"],
            "source_event_keys": [],
        },
        {
            "key": "DES-002",
            "authority": "agent",
            "kind": "compatibility",
            "summary": "Reuse workspace-administrator authorization.",
            "rationale": "The approved requirement names that audience.",
            "alternatives": [],
            "references": ["DES-002"],
            "source_event_keys": [],
        },
    ]
    design_receipt = accept_result(
        design_run,
        design_manifest,
        content=design_content,
        review=None,
        decisions=design_decisions,
        expected_events=[
            "role-run-completed",
            "artifact-accepted",
            "agent-decision-introduced",
            "agent-decision-introduced",
        ],
    )

    design_review_run, design_review_manifest = reserve_and_validate_manifest(
        "design", "specification-reviewer", "review"
    )
    design_review = {
        "schema_version": 2,
        "stage": "design",
        "inputs": copy.deepcopy(design_review_manifest["inputs"]),
        "verdict": "pass",
        "findings": [],
    }
    design_review_receipt = accept_result(
        design_review_run,
        design_review_manifest,
        content=None,
        review=design_review,
        decisions=[
            {
                "key": "VERDICT",
                "authority": "agent",
                "kind": "verdict",
                "summary": "Design passes specification review.",
                "rationale": "Every requirement is covered by a traced decision.",
                "alternatives": [],
                "references": [],
                "source_event_keys": [],
            }
        ],
        expected_events=[
            "role-run-completed",
            "review-accepted",
            "agent-decision-introduced",
        ],
    )
    approve("design", ["stage-approved", "stage-entered"])
    assert current_state()["stage"] == "plan"

    plan_run, plan_manifest = reserve_and_validate_manifest(
        "plan", "planner", "draft"
    )
    plan_content = f"""# Plan
## Steps
### STEP-001 — Implement audited export
Covers: {requirement_reference}, DES-001, DES-002
Outcome: Administrators can request a bounded asynchronous export.
Changes: Add the export API, worker, and authorization integration.
Verification: Run authorization and export integration tests.
## Final verification
Run the complete export test suite.
"""
    plan_receipt = accept_result(
        plan_run,
        plan_manifest,
        content=plan_content,
        review=None,
        decisions=[
            {
                "key": "STEP-001",
                "authority": "agent",
                "kind": "ordering",
                "summary": "Implement authorization before worker dispatch.",
                "rationale": "Unauthorized work must not enter the queue.",
                "alternatives": [],
                "references": ["STEP-001"],
                "source_event_keys": [],
            }
        ],
        expected_events=[
            "role-run-completed",
            "artifact-accepted",
            "agent-decision-introduced",
        ],
    )
    approve("plan", ["stage-approved", "workflow-completed"])

    final_state = current_state()
    final_log_bytes = log_path.read_bytes()
    final_log = validate_audit_log(final_log_bytes)
    assert final_state["stage"] == "plan" and final_state["status"] == "approved"
    assert set(final_state["approvals"]) == set(STAGES)
    assert final_state["approvals"]["idea"]["artifact_sha256"] == (
        idea_receipt["output"]["sha256"]
    )
    assert final_state["approvals"]["requirements"] == {
        "artifact_sha256": requirements_receipt["output"]["sha256"],
        "review_sha256": requirements_review_receipt["output"]["sha256"],
        "accepted_risks": [],
    }
    assert final_state["approvals"]["design"] == {
        "artifact_sha256": design_receipt["output"]["sha256"],
        "review_sha256": design_review_receipt["output"]["sha256"],
        "accepted_risks": [],
    }
    assert final_state["approvals"]["plan"]["artifact_sha256"] == (
        plan_receipt["output"]["sha256"]
    )
    assert [event["event"] for event in final_log["events"]] == expected_timeline
    assert [event["event_id"] for event in final_log["events"]] == [
        format_audit_event_id(sequence)
        for sequence in range(1, len(final_log["events"]) + 1)
    ]
    assert all(
        accepted_prefixes[index + 1].startswith(prefix)
        for index, prefix in enumerate(accepted_prefixes[:-1])
    )
    assert idea_body_marker.encode("utf-8") not in final_log_bytes
    design_introduced = [
        event
        for event in final_log["events"]
        if event["event"] == "agent-decision-introduced"
        and event["actor"] == "design-author"
        and event["payload"]["key"] == "DES-001"
    ]
    assert len(design_introduced) == 1
    assert design_introduced[0]["payload"]["alternatives"] == [
        rejected_alternative
    ]
    design_reservation = _audit_event_by_key(
        final_log["events"], f"run/{design_run}/reserved"
    )
    design_completion = _audit_event_by_key(
        final_log["events"], f"run/{design_run}/completed"
    )
    assert design_reservation is not None and design_completion is not None
    assert design_reservation["payload"]["configured_agent"] == "stepan_author"
    assert design_reservation["payload"]["requested_model"] is None
    assert design_completion["payload"]["effective_model"] == "unavailable"
    assert design_completion["payload"]["receipt_sha256"] == (
        canonical_receipt_sha256(design_receipt)
    )


def self_test_checkpoint_commits(temporary_root: Path) -> None:
    def git(project_root: Path, *arguments: str) -> str:
        return _checked_git_output(
            project_root, list(arguments), f"self-testing Git {' '.join(arguments)}"
        )

    def initialize(
        name: str, *, flush_selection: bool = True
    ) -> tuple[Path, Path, Path, str, str]:
        project_root = temporary_root / f"checkpoint-{name}"
        project_root.mkdir()
        git(project_root, "init", "--quiet")
        git(project_root, "config", "user.name", "Stepan Self Test")
        git(project_root, "config", "user.email", "stepan@example.invalid")
        git(project_root, "config", "commit.gpgsign", "false")
        git(project_root, "config", "core.autocrlf", "false")
        git(project_root, "config", "core.hooksPath", ".git/hooks")
        baseline = project_root / "baseline.txt"
        baseline.write_text("baseline\n", encoding="utf-8", newline="\n")
        git(project_root, "add", "--", "baseline.txt")
        git(project_root, "commit", "--quiet", "--message", "baseline")

        specification = f"git-{name}"
        initialize_feature_specification(
            project_root,
            specification,
            "Create an audited Git checkpoint.",
            self_test_execution_snapshot(),
            recorded_at="2026-08-23T13:00:00Z",
        )
        specification_root = project_root / f"docs/changes/specs/{specification}"
        artifact = specification_root / "idea.md"
        artifact.write_text("# Idea\n\nCheckpoint fixture.\n", encoding="utf-8", newline="\n")
        state_path = specification_root / "state.yaml"
        _, state = validate_state_path(state_path, project_root, specification)
        state["status"] = "awaiting-approval"
        accepted = build_queued_audit_event(
            event_key=f"run/{specification}--idea--idea-author--1/artifact-accepted",
            kind="lifecycle",
            event="artifact-accepted",
            stage="idea",
            actor="router",
            run_id=f"{specification}--idea--idea-author--1",
            payload={
                "path": f"docs/changes/specs/{specification}/idea.md",
                "sha256": file_hash(artifact),
                "receipt_sha256": "sha256:" + "a" * 64,
            },
        )
        state = queue_audit_transaction(state, specification, [accepted])
        state_path.write_bytes(render_canonical_yaml_mapping(state))
        flush_audit_outbox(
            state_path,
            project_root,
            specification,
            recorded_at="2026-08-23T13:01:00Z",
        )
        apply_workflow_transition_in_state(
            state_path,
            project_root,
            specification,
            "approve-stage",
            {
                "stage": "idea",
                "action": "continue-and-commit",
                "verbatim": "Continue and commit",
                "accepted_risks": [],
            },
        )
        if flush_selection:
            flush_audit_outbox(
                state_path,
                project_root,
                specification,
                recorded_at="2026-08-23T13:02:00Z",
            )
        _, selected = validate_state_path(state_path, project_root, specification)
        checkpoint = selected["checkpoint_commit"]
        assert isinstance(checkpoint, dict)
        selection_key = str(checkpoint["selection_event_key"])
        return project_root, state_path, artifact, specification, selection_key

    def write_hook(project_root: Path, name: str, content: str) -> Path:
        hook = project_root / ".git/hooks" / name
        hook.write_text(content, encoding="utf-8", newline="\n")
        hook.chmod(hook.stat().st_mode | 0o111)
        return hook

    # A successful partial commit includes only the specification, preserves an
    # unrelated staged change, and leaves hook-created working files uncommitted.
    project_root, state_path, artifact, specification, selection_key = initialize(
        "success"
    )
    unrelated = project_root / "unrelated.txt"
    unrelated.write_text("user staged\n", encoding="utf-8", newline="\n")
    git(project_root, "add", "--", "unrelated.txt")
    outside_before = _staged_semantic_snapshot(
        project_root, exclude_scope=f"docs/changes/specs/{specification}"
    )
    hook_created = project_root / "hook-created.txt"
    write_hook(
        project_root,
        "pre-commit",
        "#!/bin/sh\nprintf 'created by hook\\n' > hook-created.txt\n",
    )
    result = checkpoint_commit_in_state(state_path, project_root, specification)
    assert result["status"] == "committed", result
    commit = str(result["commit"])
    assert commit == _current_git_head(project_root)
    _, advanced = validate_state_path(state_path, project_root, specification)
    assert advanced["stage"] == "requirements" and advanced["checkpoint_commit"] is None
    assert _staged_semantic_snapshot(
        project_root, exclude_scope=f"docs/changes/specs/{specification}"
    ) == outside_before
    assert not _scope_has_staged_changes(
        project_root, f"docs/changes/specs/{specification}"
    )
    committed_paths = {
        line
        for line in git(
            project_root,
            "diff-tree",
            "--no-commit-id",
            "--name-only",
            "-r",
            commit,
        ).splitlines()
        if line
    }
    assert committed_paths and all(
        path.startswith(f"docs/changes/specs/{specification}/")
        for path in committed_paths
    )
    assert hook_created.read_bytes() == b"created by hook\n"
    assert _run_git(
        project_root, ["cat-file", "-e", f"{commit}:hook-created.txt"]
    ).returncode != 0
    _, completed_state = validate_state_path(state_path, project_root, specification)
    _, _, _, completed_events = _audit_events_for_state(project_root, completed_state)
    selection = _audit_event_by_key(completed_events, selection_key)
    assert selection is not None
    assert _checkpoint_message_has_link(
        _checkpoint_commit_message(project_root, commit),
        f"stepan({specification}): approve idea",
        str(selection["event_id"]),
    )
    assert any(event["event"] == "checkpoint-commit-completed" for event in completed_events)

    project_root, state_path, _, specification, _ = initialize("post-hook")
    post_hook_file = project_root / "post-hook.txt"
    write_hook(
        project_root,
        "post-commit",
        "#!/bin/sh\nprintf 'post hook\\n' > post-hook.txt\nexit 1\n",
    )
    post_result = checkpoint_commit_in_state(state_path, project_root, specification)
    assert post_result["status"] == "committed"
    assert post_hook_file.read_bytes() == b"post hook\n"
    assert _run_git(
        project_root,
        ["cat-file", "-e", f"{post_result['commit']}:post-hook.txt"],
    ).returncode != 0

    # A crash after Git updates HEAD is recovered from that exact HEAD without a
    # duplicate commit or a commit SHA entering the audit log.
    project_root, state_path, _, specification, _ = initialize("crash")
    head_before = _current_git_head(project_root)

    class SimulatedCheckpointCrash(RuntimeError):
        pass

    try:
        checkpoint_commit_in_state(
            state_path,
            project_root,
            specification,
            _after_commit=lambda _commit: (_ for _ in ()).throw(
                SimulatedCheckpointCrash("after commit")
            ),
        )
    except SimulatedCheckpointCrash:
        pass
    else:
        raise AssertionError("checkpoint commit crash injection did not stop the operation")
    committed_head = _current_git_head(project_root)
    assert committed_head is not None and committed_head != head_before
    _, interrupted = validate_state_path(state_path, project_root, specification)
    assert interrupted["checkpoint_commit"] is not None
    recovered = checkpoint_commit_in_state(state_path, project_root, specification)
    assert recovered == {
        "schema_version": 1,
        "status": "recovered",
        "commit": committed_head,
    }
    assert _current_git_head(project_root) == committed_head
    log_text = (
        project_root / f"docs/changes/specs/{specification}/mem-log.md"
    ).read_text(encoding="utf-8")
    assert committed_head not in log_text

    project_root, state_path, _, specification, selection_key = initialize(
        "outcome-crash"
    )
    try:
        checkpoint_commit_in_state(
            state_path,
            project_root,
            specification,
            _after_commit=lambda _commit: (_ for _ in ()).throw(
                SimulatedCheckpointCrash("after commit")
            ),
        )
    except SimulatedCheckpointCrash:
        pass
    outcome_head = _current_git_head(project_root)
    apply_workflow_transition_in_state(
        state_path,
        project_root,
        specification,
        "checkpoint-commit-succeeded",
        {"selection_event_key": selection_key},
    )
    try:
        flush_audit_outbox(
            state_path,
            project_root,
            specification,
            _after_log_replace=lambda: (_ for _ in ()).throw(
                SimulatedCheckpointCrash("after outcome log replacement")
            ),
        )
    except SimulatedCheckpointCrash:
        pass
    else:
        raise AssertionError("checkpoint outcome crash injection did not stop the flush")
    assert checkpoint_commit_in_state(state_path, project_root, specification) == {
        "schema_version": 1,
        "status": "recovered-outcome",
    }
    assert _current_git_head(project_root) == outcome_head

    # Once another commit advances HEAD, an older matching checkpoint commit is
    # never accepted and no duplicate checkpoint commit is created.
    project_root, state_path, _, specification, _ = initialize("advanced")
    try:
        checkpoint_commit_in_state(
            state_path,
            project_root,
            specification,
            _after_commit=lambda _commit: (_ for _ in ()).throw(
                SimulatedCheckpointCrash("after commit")
            ),
        )
    except SimulatedCheckpointCrash:
        pass
    linked_head = _current_git_head(project_root)
    advancement = project_root / "advancement.txt"
    advancement.write_text("advance\n", encoding="utf-8", newline="\n")
    git(project_root, "add", "--", "advancement.txt")
    git(project_root, "commit", "--quiet", "--message", "advance branch")
    advanced_head = _current_git_head(project_root)
    _self_test_expect_value_error(
        lambda: checkpoint_commit_in_state(state_path, project_root, specification),
        "recovered a checkpoint commit that was no longer Git HEAD",
    )
    assert _current_git_head(project_root) == advanced_head != linked_head

    # Hook rejection keeps HEAD and unrelated staged intent, preserves the hook
    # working file, removes only router-created specification staging, and logs
    # one trusted failure outcome.
    project_root, state_path, _, specification, _ = initialize("hook-failure")
    unrelated = project_root / "unrelated.txt"
    unrelated.write_text("staged before hook\n", encoding="utf-8", newline="\n")
    git(project_root, "add", "--", "unrelated.txt")
    staged_before = _staged_semantic_snapshot(project_root)
    head_before = _current_git_head(project_root)
    hook_file = project_root / "hook-failure.txt"
    write_hook(
        project_root,
        "pre-commit",
        "#!/bin/sh\nprintf 'hook failure\\n' > hook-failure.txt\nexit 1\n",
    )
    failed = checkpoint_commit_in_state(state_path, project_root, specification)
    assert failed["status"] == "failed"
    assert _current_git_head(project_root) == head_before
    assert hook_file.read_bytes() == b"hook failure\n"
    assert _staged_semantic_snapshot(project_root) == staged_before
    _, failed_state = validate_state_path(state_path, project_root, specification)
    assert failed_state["checkpoint_commit"] is not None
    _, _, _, failed_events = _audit_events_for_state(project_root, failed_state)
    assert [
        event["event"] for event in failed_events if event["event"] == "checkpoint-commit-failed"
    ] == ["checkpoint-commit-failed"]

    project_root, state_path, _, specification, _ = initialize("hook-staged-outside")
    head_before = _current_git_head(project_root)
    staged_by_hook = project_root / "hook-staged-outside.txt"
    staged_before = _staged_semantic_snapshot(project_root)
    write_hook(
        project_root,
        "pre-commit",
        "#!/bin/sh\nprintf 'staged by hook\\n' > hook-staged-outside.txt\n"
        "git add -- hook-staged-outside.txt\n",
    )
    failed = checkpoint_commit_in_state(state_path, project_root, specification)
    assert failed["status"] == "failed"
    assert "staged a path outside" in str(failed["error"])
    assert _current_git_head(project_root) == head_before
    assert staged_by_hook.read_bytes() == b"staged by hook\n"
    assert _staged_semantic_snapshot(project_root) == staged_before

    # A commit-msg hook that removes or changes the required event trailer is
    # rejected before ref update; the checkpoint remains retryable and the
    # failure is durable. Both variants exercise exact trailer validation.
    for variant in ("missing-trailer", "wrong-trailer"):
        project_root, state_path, _, specification, _ = initialize(variant)
        head_before = _current_git_head(project_root)
        if variant == "missing-trailer":
            script = "#!/bin/sh\nsed -n '1p' \"$1\" > \"$1.tmp\"\nmv \"$1.tmp\" \"$1\"\n"
        else:
            script = (
                "#!/bin/sh\n"
                f"printf 'stepan({specification}): approve idea\\n\\n"
                "Stepan-Audit-Event: MEM-999999\\n' > \"$1\"\n"
            )
        write_hook(project_root, "commit-msg", script)
        failed = checkpoint_commit_in_state(state_path, project_root, specification)
        assert failed["status"] == "failed"
        wrong_commit = _current_git_head(project_root)
        assert wrong_commit == head_before
        _, state = validate_state_path(state_path, project_root, specification)
        assert state["checkpoint_commit"] is not None
        _, _, _, events = _audit_events_for_state(project_root, state)
        selection = _audit_event_by_key(
            events, str(state["checkpoint_commit"]["selection_event_key"])
        )
        assert selection is not None
        assert not _checkpoint_message_has_link(
            _checkpoint_commit_message(project_root, wrong_commit),
            f"stepan({specification}): approve idea",
            str(selection["event_id"]),
        )

    # The selection must already be durable. An artifact changed after selection
    # blocks before HEAD or index changes.
    project_root, state_path, _, specification, _ = initialize(
        "selection-flush", flush_selection=False
    )
    head_before = _current_git_head(project_root)
    _self_test_expect_value_error(
        lambda: checkpoint_commit_in_state(state_path, project_root, specification),
        "started Git before the checkpoint selection event was durable",
    )
    assert _current_git_head(project_root) == head_before
    flush_audit_outbox(state_path, project_root, specification)
    assert checkpoint_commit_in_state(state_path, project_root, specification)["status"] == "committed"

    project_root, state_path, artifact, specification, _ = initialize("changed")
    head_before = _current_git_head(project_root)
    artifact.write_text("# Idea\n\nChanged after selection.\n", encoding="utf-8", newline="\n")
    _self_test_expect_value_error(
        lambda: checkpoint_commit_in_state(state_path, project_root, specification),
        "committed a specification changed after checkpoint selection",
    )
    assert _current_git_head(project_root) == head_before

    project_root, state_path, _, specification, _ = initialize("staged-scope")
    git(
        project_root,
        "add",
        "--",
        f"docs/changes/specs/{specification}/idea.md",
    )
    staged_before = _staged_semantic_snapshot(project_root)
    _self_test_expect_value_error(
        lambda: checkpoint_commit_in_state(state_path, project_root, specification),
        "overwrote a pre-existing staged specification variant",
    )
    assert _staged_semantic_snapshot(project_root) == staged_before

    project_root, state_path, _, specification, _ = initialize("audit-gate")
    log_path = project_root / f"docs/changes/specs/{specification}/mem-log.md"
    log_path.write_bytes(log_path.read_bytes() + b"manual append\n")
    head_before = _current_git_head(project_root)
    _self_test_expect_value_error(
        lambda: checkpoint_commit_in_state(state_path, project_root, specification),
        "ran Git with an untrusted checkpoint audit log",
    )
    assert _current_git_head(project_root) == head_before


AUDIT_ACCEPTANCE_CRITERIA_COVERAGE = {
    1: ("feature-initialization:request-header-state", "feature-e2e:straight-through"),
    2: ("audit-outbox:exact-prefix", "feature-e2e:every-flush-prefix"),
    3: ("audit-schemas:tamper-matrix", "feature-transitions:untrusted-hard-stop"),
    4: ("audit-outbox:pending-gates",),
    5: ("audit-outbox-recovery:all-crash-boundaries",),
    6: ("feature-transitions:block-answer-normalize",),
    7: ("role-decisions:receipt-required-and-role-empty-rules",),
    8: ("role-decisions:artifact-review-coverage", "feature-e2e:validated-receipts"),
    9: ("role-decisions:introduced-revised-retired-unchanged",),
    10: ("feature-e2e:material-alternative-durable",),
    11: ("revision-manifests:current-artifact-isolated",),
    12: ("feature-e2e:hash-evidence-without-body-archive",),
    13: ("feature-e2e:executor-provenance", "feature-transitions:mailbox-wait-resume"),
    14: ("feature-transitions:question-status-stop",),
    15: ("checkpoint-commits:trailer-and-no-self-reference",),
    16: ("legacy-contracts:state-and-receipt-rejected",),
    17: ("revision-manifests:no-audit-access", "feature-e2e:no-audit-access"),
    18: (
        "feature-transitions:complete-routing-matrix",
        "feature-e2e:straight-through",
        "checkpoint-commits:hooks-index-recovery",
    ),
    19: (
        "audit-schemas:markdown-hash-and-tamper",
        "audit-outbox-recovery:all-crash-boundaries",
        "role-decisions:introduced-revised-retired-unchanged",
        "feature-e2e:straight-through",
    ),
}


def self_test_progressive_disclosure(skill_root: Path) -> None:
    entrypoint = (skill_root / "SKILL.md").read_text(encoding="utf-8")
    feature_root = skill_root / "references/flows/feature"
    protocol = (feature_root / "protocol.md").read_text(encoding="utf-8")
    execution = (feature_root / "execution.md").read_text(encoding="utf-8")
    router = (feature_root / "router.md").read_text(encoding="utf-8")
    adapters = [
        path.read_text(encoding="utf-8")
        for path in sorted((feature_root / "adapters").glob("*.md"))
    ]
    role_paths = [feature_root / "roles" / f"{role}.md" for role in ROLES]
    role_texts = [path.read_text(encoding="utf-8") for path in role_paths]

    assert "references/flows/feature/protocol.md" in entrypoint
    assert "audit-log-spec.md" not in entrypoint
    assert "modules/audit/decisions.md" not in entrypoint
    assert "[`audit-log-spec.md`](audit-log-spec.md)" in protocol
    assert "audit: <required mapping from audit-log-spec.md>" in protocol
    for duplicated_schema_token in (
        "head_event_id",
        "head_event_sha256",
        "decision_index",
        "audit-tx-",
        "event_sha256",
        "previous_event_sha256",
    ):
        assert duplicated_schema_token not in entrypoint
        assert duplicated_schema_token not in protocol
        assert all(duplicated_schema_token not in text for text in role_texts)

    direct_decision_link = "../../../modules/audit/decisions.md"
    for role, path, text in zip(ROLES, role_paths, role_texts, strict=True):
        assert path.stem == role
        assert text.count(direct_decision_link) == 1
        assert "audit-log-spec.md" not in text
        assert "audit-log-plan.md" not in text
        assert "mem-log.md" not in text
        resources = role_resource_manifest(skill_root, role)["resources"]
        assert resources[-1] == "references/modules/audit/decisions.md"

    non_role_runtime_texts = [entrypoint, protocol, execution, router, *adapters]
    assert all(
        "modules/audit/decisions.md" not in text
        for text in [entrypoint, protocol, router, *adapters]
    )
    assert all(
        "audit-log-plan.md" not in text
        for text in [*non_role_runtime_texts, *role_texts]
    )
    assert '"decisions"' in execution
    assert "checkpoint_commit: null" in protocol
    assert "audit: <required mapping" in protocol


def self_test_audit_acceptance_coverage(
    skill_root: Path, executed_capabilities: set[str]
) -> None:
    specification = (
        skill_root / "references/flows/feature/audit-log-spec.md"
    ).read_text(encoding="utf-8")
    _, marker, acceptance = specification.partition("\n## Acceptance criteria\n")
    assert marker
    criterion_ids = {
        int(match.group(1))
        for match in re.finditer(r"^(\d+)\. ", acceptance, flags=re.MULTILINE)
    }
    assert criterion_ids == set(AUDIT_ACCEPTANCE_CRITERIA_COVERAGE)
    assert all(AUDIT_ACCEPTANCE_CRITERIA_COVERAGE.values())
    mapped_capabilities = {
        capability
        for capabilities in AUDIT_ACCEPTANCE_CRITERIA_COVERAGE.values()
        for capability in capabilities
    }
    assert mapped_capabilities <= executed_capabilities


def self_test() -> None:
    self_test_audit_schemas()
    assert spec_id("export transaction history") == "export-transaction-history"
    assert spec_id("Ёж и щука") == "yozh-i-shchuka"
    assert spec_id("ъь") == "change"
    assert spec_id("a" * 70) == "a" * 63

    existing = {"test", "test-2"}
    assert next_available_spec_id("test", existing.__contains__) == "test-3"
    long_id = "a" * 63
    assert next_available_spec_id(long_id, {long_id}.__contains__) == "a" * 61 + "-2"

    assert (
        role_run_id("export-data", "design", "design-author", 2)
        == "export-data--design--design-author--2"
    )
    assert run_reservation("export-data", "design", "design-author", 2) == {
        "run_id": "export-data--design--design-author--2",
        "sequence": 2,
        "next_run_sequence": 3,
    }
    approval_cases = {
        "idea": None,
        "requirements": "pass",
        "design": "pass",
        "plan": None,
    }
    for stage, review_verdict in approval_cases.items():
        results = [
            approval_transition(
                action,
                stage,
                "awaiting-approval",
                False,
                review_verdict,
                False,
            )
            for action in APPROVAL_ACTIONS
        ]
        assert results[0]["approved_stage"] == results[1]["approved_stage"] == stage
        assert results[0]["next_stage"] == results[1]["next_stage"]
        assert results[0]["next_status"] == results[1]["next_status"]
        assert results[0]["commit"] is False
        assert results[1]["commit"] is True

    invalid_approvals = (
        ("idea", "drafting", False, None, False),
        ("idea", "awaiting-approval", False, "pass", False),
        ("requirements", "awaiting-approval", False, None, False),
        ("requirements", "awaiting-approval", False, "changes-required", False),
        ("design", "awaiting-approval", True, "pass", False),
        ("plan", "awaiting-approval", False, None, True),
    )
    for action in APPROVAL_ACTIONS:
        for stage, status, pending, review_verdict, user_decision in invalid_approvals:
            try:
                approval_transition(
                    action,
                    stage,
                    status,
                    pending,
                    review_verdict,
                    user_decision,
                )
            except ValueError:
                pass
            else:
                raise AssertionError(
                    f"accepted invalid approval state: {action}, {stage}, {status}"
                )
    request_hash = "sha256:" + "b" * 64
    state_content = self_test_state_content(request_hash)
    state = validate_state_value(parse_strict_yaml(state_content, "state"), "export-data")
    legacy_state = dict(state)
    del legacy_state["audit"]
    _self_test_expect_value_error(
        lambda: validate_state_value(legacy_state, "export-data"),
        "accepted a legacy feature state without audit metadata",
    )
    reserved_state, reservation = reserved_state_content(
        state_content,
        state,
        "export-data",
        "requirements",
        "requirements-author",
        "draft",
        "author",
        "codex",
        "docs/changes/specs/export-data/requirements.md",
        request_hash,
    )
    assert reservation == {
        "run_id": "export-data--requirements--requirements-author--2",
        "sequence": 2,
        "next_run_sequence": 3,
    }
    assert "next_run_sequence: 3\n" in reserved_state
    assert "active_run:\n" in reserved_state
    assert "  sequence: 2\n" in reserved_state
    try:
        reserved_state_content(
            reserved_state,
            validate_state_value(
                parse_strict_yaml(reserved_state, "state"), "export-data"
            ),
            "export-data",
            "requirements",
            "requirements-author",
            "draft",
            "author",
            "codex",
            "docs/changes/specs/export-data/requirements.md",
            request_hash,
        )
    except ValueError:
        pass
    else:
        raise AssertionError("reserved a second run over an active run")
    try:
        validate_state_value(
            parse_strict_yaml(
                state_content.replace("schema_version: 1", "schema_version: 3"),
                "state",
            ),
            "export-data",
        )
    except ValueError:
        pass
    else:
        raise AssertionError("accepted obsolete feature state schema version")
    invalid_checkpoint = {**state, "status": "awaiting-approval"}
    try:
        validate_run_eligibility(
            invalid_checkpoint,
            "export-data",
            "requirements",
            "design-author",
            "draft",
            "author",
            "codex",
            "docs/changes/specs/export-data/design.md",
            request_hash,
        )
    except ValueError:
        pass
    else:
        raise AssertionError("reserved design author at an approved idea checkpoint")
    valid_run = {
        "specification": "export-data",
        "stage": "requirements",
        "role": "requirements-author",
        "purpose": "draft",
        "executor": "author",
        "adapter": "codex",
        "output": "docs/changes/specs/export-data/requirements.md",
        "request_sha256": request_hash,
    }
    for field, invalid_value in (
        ("stage", "design"),
        ("role", "design-author"),
        ("purpose", "revise"),
        ("executor", "orchestrator"),
        ("adapter", "mailbox"),
        ("output", "docs/changes/specs/export-data/other.md"),
        ("request_sha256", "sha256:" + "a" * 64),
    ):
        arguments = {**valid_run, field: invalid_value}
        try:
            validate_run_eligibility(state, **arguments)
        except ValueError:
            pass
        else:
            raise AssertionError(f"accepted invalid run {field}: {invalid_value}")
    try:
        validate_state_value(parse_strict_yaml(state_content, "state"), "other-spec")
    except ValueError:
        pass
    else:
        raise AssertionError("accepted mismatched state specification identity")

    answered_state = {
        **state,
        "status": "revising",
        "pending": {
            "kind": "clarification",
            "origin": "review",
            "stage": "requirements",
            "request": "Which records are included?",
            "response": "Posted records only.",
            "resume_purpose": "revise",
        },
    }
    validate_state_value(answered_state, "export-data")
    validate_run_eligibility(
        answered_state,
        "export-data",
        "requirements",
        "requirements-author",
        "revise",
        "author",
        "codex",
        "docs/changes/specs/export-data/requirements.md",
        request_hash,
    )
    unanswered_state = {
        **answered_state,
        "pending": {**answered_state["pending"], "response": None},
    }
    try:
        validate_run_eligibility(
            unanswered_state,
            "export-data",
            "requirements",
            "requirements-author",
            "revise",
            "author",
            "codex",
            "docs/changes/specs/export-data/requirements.md",
            request_hash,
        )
    except ValueError:
        pass
    else:
        raise AssertionError("reserved a run with an unanswered pending question")
    blocked_state = {
        **state,
        "status": "awaiting-decision",
        "pending": {
            **answered_state["pending"],
            "response": None,
        },
    }
    validate_state_value(blocked_state, "export-data")
    reviewing_state = {**state, "status": "reviewing"}
    validate_run_eligibility(
        reviewing_state,
        "export-data",
        "requirements",
        "requirements-reviewer",
        "review",
        "author",
        "codex",
        "docs/changes/specs/export-data/review/requirements.yaml",
        request_hash,
    )
    revising_state = {**state, "status": "revising"}
    validate_run_eligibility(
        revising_state,
        "export-data",
        "requirements",
        "requirements-author",
        "revise",
        "author",
        "codex",
        "docs/changes/specs/export-data/requirements.md",
        request_hash,
    )
    for arguments in (
        ("bad--id", "design", "design-author", 1),
        ("export-data", "invalid", "design-author", 1),
        ("export-data", "design", "invalid", 1),
        ("export-data", "design", "design-author", 0),
    ):
        try:
            role_run_id(*arguments)
        except ValueError:
            pass
        else:
            raise AssertionError(f"accepted invalid run ID arguments: {arguments!r}")

    actual = canonicalize(b"\xef\xbb\xbfa\r\nb\r")
    canonical = canonicalize(b"a\nb\n\n")
    assert actual == canonical == b"a\nb\n"

    blocked = {
        "schema_version": 1,
        "run_id": "export-data--requirements--requirements-author--1",
        "status": "blocked",
        "question": "Кто может экспортировать данные?",
    }
    assert (
        validate_receipt(
            blocked,
            "export-data--requirements--requirements-author--1",
        "docs/changes/specs/export-data/requirements.md",
            "native",
        )
        == blocked
    )
    completed = {
        "schema_version": 1,
        "run_id": "export-data--requirements--requirements-author--2",
        "status": "completed",
        "output": {
            "path": "docs/changes/specs/export-data/requirements.md",
            "sha256": "sha256:" + "a" * 64,
        },
    }
    completed_with_decisions = {
        **completed,
        "decisions": [
            {
                "key": 'ADDED Requirement "Export history"',
                "authority": "agent",
                "kind": "behavior",
                "summary": "Expose transaction export history.",
                "rationale": "The approved idea requires observable export history.",
                "alternatives": [],
                "references": ['ADDED Requirement "Export history"'],
                "source_event_keys": [],
            }
        ],
    }
    assert (
        validate_receipt(
            completed_with_decisions,
            "export-data--requirements--requirements-author--2",
            "docs/changes/specs/export-data/requirements.md",
            "native",
        )
        == completed_with_decisions
    )
    _self_test_expect_value_error(
        lambda: validate_receipt(
            completed,
            "export-data--requirements--requirements-author--2",
            "docs/changes/specs/export-data/requirements.md",
            "native",
        ),
        "accepted a completed receipt without required decisions",
    )
    mailbox_completed = {
        **completed_with_decisions,
        "executor": {
            "requested": {"model": "requirements-v2", "reasoning": "high"},
            "effective": {
                "model": "requirements-v2-2026-08-01",
                "reasoning": "high",
            },
        },
    }
    assert (
        validate_receipt(
            mailbox_completed,
            "export-data--requirements--requirements-author--2",
        "docs/changes/specs/export-data/requirements.md",
            "mailbox",
            "requirements-v2",
            "high",
        )
        == mailbox_completed
    )
    assert parse_json_receipt(json.dumps(blocked)) == blocked
    router_result = {
        "schema_version": 1,
        "message": "Кто может экспортировать данные?",
        "continuation": {"kind": "state", "spec_id": "export-data"},
    }
    assert validate_router_result(router_result) == router_result
    assert validate_router_result(
        {
            "schema_version": 1,
            "message": "Выберите существующую или новую спецификацию.",
            "continuation": {"kind": "pre-state"},
        }
    )
    assert validate_router_result(
        {"schema_version": 1, "message": "Готово.", "continuation": None}
    )
    try:
        parse_json_receipt(f"```json\n{json.dumps(blocked)}\n```")
    except ValueError:
        pass
    else:
        raise AssertionError("accepted a fenced JSON receipt")

    for invalid, expected_run_id in (
        ({**blocked, "extra": True}, "export-data--requirements--requirements-author--1"),
        (
            {**blocked, "run_id": "export-data--requirements--requirements-author--9"},
            "export-data--requirements--requirements-author--1",
        ),
        (
            {
                **completed,
                "output": {
                    **completed["output"],
                    "path": "docs/changes/specs/export-data/other.md",
                },
            },
            "export-data--requirements--requirements-author--2",
        ),
    ):
        try:
            validate_receipt(
                invalid,
                expected_run_id,
                "docs/changes/specs/export-data/requirements.md",
                "native",
            )
        except ValueError:
            pass
        else:
            raise AssertionError(f"accepted invalid receipt: {invalid!r}")

    for invalid in (
        {**router_result, "extra": True},
        {**router_result, "continuation": {"kind": "state"}},
        {**router_result, "continuation": {"kind": "state", "spec_id": "bad--id"}},
        {
            **router_result,
            "continuation": {"kind": "pre-state", "spec_id": "export-data"},
        },
    ):
        try:
            validate_router_result(invalid)
        except ValueError:
            pass
        else:
            raise AssertionError(f"accepted invalid router result: {invalid!r}")

    expected_files = codex_init_files()
    validate_codex_contract_examples(expected_files)
    claude_expected_files = claude_init_files()
    validate_claude_contract_examples(claude_expected_files)
    skill_root = Path(__file__).resolve().parent.parent
    expected_role_resources = {
        "idea-author": [
            "references/modules/idea/artifact.md",
            "references/modules/idea/common.md",
            "references/modules/idea/authoring.md",
            "references/modules/audit/decisions.md",
        ],
        "requirements-author": [
            "references/modules/idea/artifact.md",
            "references/modules/requirements/artifact.md",
            "references/modules/idea/common.md",
            "references/modules/requirements/common.md",
            "references/modules/requirements/authoring.md",
            "references/modules/audit/decisions.md",
        ],
        "requirements-reviewer": [
            "references/modules/idea/artifact.md",
            "references/modules/requirements/artifact.md",
            "references/modules/idea/common.md",
            "references/modules/requirements/common.md",
            "references/modules/requirements/reviewing.md",
            "references/modules/review/artifact.md",
            "references/modules/audit/decisions.md",
        ],
        "design-author": [
            "references/modules/idea/artifact.md",
            "references/modules/requirements/artifact.md",
            "references/modules/idea/common.md",
            "references/modules/requirements/common.md",
            "references/modules/design/common.md",
            "references/modules/design/authoring.md",
            "references/modules/design/artifact.md",
            "references/modules/audit/decisions.md",
        ],
        "specification-reviewer": [
            "references/modules/idea/artifact.md",
            "references/modules/requirements/artifact.md",
            "references/modules/design/artifact.md",
            "references/modules/idea/common.md",
            "references/modules/requirements/common.md",
            "references/modules/design/common.md",
            "references/modules/design/reviewing.md",
            "references/modules/review/artifact.md",
            "references/modules/audit/decisions.md",
        ],
        "planner": [
            "references/modules/idea/artifact.md",
            "references/modules/requirements/artifact.md",
            "references/modules/design/artifact.md",
            "references/modules/plan/artifact.md",
            "references/modules/idea/common.md",
            "references/modules/requirements/common.md",
            "references/modules/design/common.md",
            "references/modules/audit/decisions.md",
        ],
    }
    for role in ROLES:
        manifest = role_resource_manifest(skill_root, role)
        assert manifest["brief"] == ROLE_BRIEFS[role]
        assert manifest["resources"] == expected_role_resources[role]
    assert len(expected_files) == 6
    assert expected_files[-1][0] == PurePosixPath(".stepan/config.yaml")
    assert all(content.endswith(b"\n") for _, content in expected_files)
    assert all(b"\r" not in content for _, content in expected_files)
    assert expected_files[0][0] == PurePosixPath(
        ".codex/agents/stepan_orchestrator.toml"
    )
    assert expected_files[1][0] == PurePosixPath(
        ".codex/agents/stepan_author.toml"
    )
    assert expected_files[2][0] == PurePosixPath(
        ".codex/agents/stepan_architect.toml"
    )
    assert b'model = "gpt-5.6"' in expected_files[0][1]
    assert b'model = "gpt-5.6-terra"' in expected_files[-2][1]
    assert len(claude_expected_files) == 8
    assert claude_expected_files[0][0] == PurePosixPath(
        ".claude/agents/stepan-orchestrator.md"
    )
    assert claude_expected_files[-3][0] == PurePosixPath(".claude/settings.json")
    assert claude_expected_files[-2][0] == PurePosixPath(
        ".claude/hooks/stepan-runtime.py"
    )
    assert claude_expected_files[-1][0] == PurePosixPath(".stepan/config.yaml")
    assert all(content.endswith(b"\n") for _, content in claude_expected_files)
    assert all(b"\r" not in content for _, content in claude_expected_files)
    assert b"model: opus\n" in claude_expected_files[0][1]
    assert b"model: sonnet\n" in claude_expected_files[-4][1]

    expected_paths = [path.as_posix() for path, _ in expected_files]
    executed_audit_capabilities = {
        "audit-schemas:markdown-hash-and-tamper",
        "audit-schemas:tamper-matrix",
        "audit-outbox:exact-prefix",
        "audit-outbox:pending-gates",
        "audit-outbox-recovery:all-crash-boundaries",
        "checkpoint-commits:hooks-index-recovery",
        "checkpoint-commits:trailer-and-no-self-reference",
        "feature-e2e:every-flush-prefix",
        "feature-e2e:executor-provenance",
        "feature-e2e:hash-evidence-without-body-archive",
        "feature-e2e:material-alternative-durable",
        "feature-e2e:no-audit-access",
        "feature-e2e:straight-through",
        "feature-e2e:validated-receipts",
        "feature-initialization:request-header-state",
        "feature-transitions:block-answer-normalize",
        "feature-transitions:complete-routing-matrix",
        "feature-transitions:mailbox-wait-resume",
        "feature-transitions:question-status-stop",
        "feature-transitions:untrusted-hard-stop",
        "legacy-contracts:state-and-receipt-rejected",
        "revision-manifests:current-artifact-isolated",
        "revision-manifests:no-audit-access",
        "role-decisions:artifact-review-coverage",
        "role-decisions:introduced-revised-retired-unchanged",
        "role-decisions:receipt-required-and-role-empty-rules",
    }
    with TemporaryDirectory(prefix="stepan-self-test-") as temporary:
        temporary_root = Path(temporary)
        self_test_feature_initialization(temporary_root)
        self_test_audit_outbox(temporary_root)
        self_test_audit_outbox_recovery(temporary_root)
        self_test_role_decisions(temporary_root)
        self_test_revision_manifests(temporary_root, skill_root)
        self_test_feature_transitions(temporary_root)
        self_test_feature_audit_e2e(temporary_root, skill_root)
        self_test_checkpoint_commits(temporary_root)

        wrong_host_root = temporary_root / "wrong-host"
        wrong_host_root.mkdir()
        try:
            initialize_codex(wrong_host_root, "claude-code")
        except ValueError:
            pass
        else:
            raise AssertionError("initialized Codex files on a non-Codex host")
        assert list(wrong_host_root.iterdir()) == []

        unsupported_host_root = temporary_root / "unsupported-host"
        unsupported_host_root.mkdir()
        try:
            initialize_claude(unsupported_host_root, "other")
        except ValueError:
            pass
        else:
            raise AssertionError("initialized Claude files on an unsupported host")
        assert list(unsupported_host_root.iterdir()) == []

        claude_root = temporary_root / "claude"
        claude_root.mkdir()
        claude_paths = [path.as_posix() for path, _ in claude_expected_files]
        claude_created = initialize_claude(claude_root, "codex")
        assert claude_created == {
            "schema_version": 1,
            "host": "claude-code",
            "status": "created",
            "created": claude_paths,
            "unchanged": [],
        }
        claude_unchanged = initialize_claude(claude_root, "claude-code")
        assert claude_unchanged == {
            "schema_version": 1,
            "host": "claude-code",
            "status": "unchanged",
            "created": [],
            "unchanged": claude_paths,
        }
        claude_snapshot = validate_project_config_path(
            claude_root / ".stepan/config.yaml", claude_root, "claude-code"
        )
        assert claude_snapshot["bindings"]["router"] == "orchestrator"
        assert claude_snapshot["profiles"]["reviewer"]["agent"] == "stepan-reviewer"
        assert json.loads(
            (claude_root / ".claude/settings.json").read_text()
        ) == expected_claude_settings()

        fresh_root = temporary_root / "fresh"
        fresh_root.mkdir()
        created = initialize_codex(fresh_root, "codex")
        assert created == {
            "schema_version": 1,
            "host": "codex",
            "status": "created",
            "created": expected_paths,
            "unchanged": [],
        }
        unchanged = initialize_codex(fresh_root, "codex")
        assert unchanged == {
            "schema_version": 1,
            "host": "codex",
            "status": "unchanged",
            "created": [],
            "unchanged": expected_paths,
        }
        snapshot = validate_project_config_path(
            fresh_root / ".stepan/config.yaml", fresh_root, "codex"
        )
        assert snapshot["bindings"]["router"] == "orchestrator"
        assert snapshot["profiles"]["author"]["project_inputs"] == []

        baseline_path = fresh_root / "docs/baseline.md"
        baseline_path.parent.mkdir()
        baseline_path.write_text("# Baseline\n", encoding="utf-8", newline="\n")
        configured = codex_stepan_config().replace(
            "    agent: stepan_author\n    project_inputs: []",
            "    agent: stepan_author\n    project_inputs:\n      - docs/baseline.md",
        )
        (fresh_root / ".stepan/config.yaml").write_text(
            configured, encoding="utf-8", newline="\n"
        )
        snapshot = validate_project_config_path(
            fresh_root / ".stepan/config.yaml", fresh_root, "codex"
        )
        assert snapshot["profiles"]["author"]["project_inputs"] == [
            {"path": "docs/baseline.md", "sha256": file_hash(baseline_path)}
        ]
        for invalid_config in (
            configured.replace("docs/baseline.md", "docs/missing.md"),
            configured.replace("schema_version: 1", "schema_version: 1\nunknown: value"),
        ):
            try:
                validate_project_config(
                    parse_strict_yaml(invalid_config, "configuration"),
                    fresh_root,
                    "codex",
                )
            except ValueError:
                pass
            else:
                raise AssertionError("accepted invalid project configuration")

        specification_root = fresh_root / "docs/changes/specs/export-data"
        specification_root.mkdir(parents=True)
        initialized_files, initialized_state = _initial_feature_files(
            "export-data",
            "Export transaction history.",
            self_test_execution_snapshot(),
            "2026-08-15T12:00:00Z",
        )
        initialized_content = {
            relative.name: content for relative, content in initialized_files
        }
        request_path = specification_root / "request.md"
        request_path.write_bytes(initialized_content["request.md"])
        (specification_root / "mem-log.md").write_bytes(
            initialized_content["mem-log.md"]
        )
        idea_path = specification_root / "idea.md"
        idea_path.write_text(
            "# Idea\n## Outcome\nExport history.\n",
            encoding="utf-8",
            newline="\n",
        )
        state_path = specification_root / "state.yaml"
        state_path.write_text(
            self_test_state_content(
                file_hash(request_path),
                file_hash(idea_path),
                initialized_state["audit"],
            ),
            encoding="utf-8",
            newline="\n",
        )
        reservation = reserve_run_in_state(
            state_path,
            fresh_root,
            "export-data",
            "requirements",
            "requirements-author",
            "draft",
            "author",
            "codex",
            "docs/changes/specs/export-data/requirements.md",
            file_hash(request_path),
        )
        assert reservation["sequence"] == 2
        _, queued_value = validate_state_path(state_path, fresh_root, "export-data")
        assert queued_value["active_run"]["role"] == "requirements-author"
        assert queued_value["audit"]["outbox"] is not None
        try:
            reserve_run_in_state(
                state_path,
                fresh_root,
                "export-data",
                "requirements",
                "requirements-author",
                "draft",
                "author",
                "codex",
                "docs/changes/specs/export-data/requirements.md",
                file_hash(request_path),
            )
        except ValueError:
            pass
        else:
            raise AssertionError("reserved over an interrupted active run")
        assert flush_audit_outbox(
            state_path,
            fresh_root,
            "export-data",
            recorded_at="2026-08-15T12:01:00Z",
        )["status"] == "flushed"
        _, reserved_value = validate_state_path(state_path, fresh_root, "export-data")
        assert reserved_value["audit"]["outbox"] is None

        mailbox_state = json.loads(json.dumps(reserved_value))
        mailbox_state["status"] = "waiting-executor"
        mailbox_state["execution"]["adapters"]["mailbox"] = {
            "kind": "mailbox",
            "root": str(temporary_root / "mailbox"),
            "wait_seconds": 15,
        }
        mailbox_state["execution"]["profiles"]["author"] = {
            "adapter": "mailbox",
            "model": "company-author-v1",
            "reasoning": "high",
            "project_inputs": [],
        }
        mailbox_state["active_run"]["adapter"] = "mailbox"
        validate_state_value(mailbox_state, "export-data")

        requirements_content = """# Export Specification Delta
## Purpose
Allow users to export history.
## ADDED Requirements
### Requirement: Export History
The system SHALL export transaction history.
#### Scenario: Successful export
- **WHEN** an authorized user requests an export
- **THEN** the system returns the transaction history
## Boundaries and assumptions
Only authorized users are in scope.
"""
        design_content = """# Design
## Overview
Add an export service.
## Decisions
### DES-001 — Export service
Covers: ADDED Requirement "Export History"
Decision: Add one export service.
Rationale: It isolates export behavior.
## Affected components
Export API and service.
## Constraints, risks and trade-offs
Large exports require bounded memory.
## Verification
Verify DES-001 with an integration test.
"""
        plan_content = """# Plan
## Steps
### STEP-001 — Implement export
Covers: ADDED Requirement "Export History", DES-001
Outcome: Authorized users can export history.
Changes: Export API and service.
Verification: Run the export integration test.
## Final verification
Run all export tests.
        """
        requirements_path = specification_root / "requirements.md"
        design_path = specification_root / "design.md"
        plan_path = specification_root / "plan.md"
        requirements_path.write_text(requirements_content, encoding="utf-8", newline="\n")
        design_path.write_text(design_content, encoding="utf-8", newline="\n")
        plan_path.write_text(plan_content, encoding="utf-8", newline="\n")
        assert validate_artifact_path("requirements", requirements_path)["requirements"] == 1
        assert validate_artifact_path(
            "design", design_path, requirements_path
        )["decisions"] == 1
        assert validate_artifact_path(
            "plan", plan_path, requirements_path, design_path
        )["steps"] == 1
        try:
            validate_plan_structure(plan_content, {"ADDED Requirement \"Other\""}, {"DES-001"})
        except ValueError:
            pass
        else:
            raise AssertionError("accepted a plan with broken requirement traceability")

        native_manifest = {
            "schema_version": 2,
            "run_id": "export-data--requirements--requirements-author--2",
            "spec_id": "export-data",
            "stage": "requirements",
            "role": "requirements-author",
            "purpose": "draft",
            "project_root": str(fresh_root.resolve()),
            "skill_root": str(skill_root.resolve()),
            "brief": ROLE_BRIEFS["requirements-author"],
            "resources": role_resource_manifest(skill_root, "requirements-author")[
                "resources"
            ],
            "inputs": [
                {
                    "path": "docs/changes/specs/export-data/request.md",
                    "sha256": file_hash(request_path),
                },
                {"path": "docs/changes/specs/export-data/idea.md", "sha256": file_hash(idea_path)},
            ],
            "clarifications": [],
            "output": "docs/changes/specs/export-data/requirements.md",
            "allowed_write": "docs/changes/specs/export-data/requirements.md",
        }
        assert validate_role_manifest(
            native_manifest, skill_root, fresh_root, "native", reserved_value
        ) == native_manifest
        mailbox_manifest = {
            **native_manifest,
            "executor": {"model": "company-author-v1", "reasoning": "high"},
        }
        assert validate_role_manifest(
            mailbox_manifest, skill_root, fresh_root, "mailbox", mailbox_state
        ) == mailbox_manifest
        for invalid_manifest, adapter_class, manifest_state in (
            (native_manifest, "native", mailbox_state),
            (
                {
                    **mailbox_manifest,
                    "executor": {"model": "company-author-v0", "reasoning": "high"},
                },
                "mailbox",
                mailbox_state,
            ),
        ):
            try:
                validate_role_manifest(
                    invalid_manifest,
                    skill_root,
                    fresh_root,
                    adapter_class,
                    manifest_state,
                )
            except ValueError:
                pass
            else:
                raise AssertionError("accepted inconsistent role manifest execution")
        try:
            validate_role_manifest(
                {**native_manifest, "resources": list(reversed(native_manifest["resources"]))},
                skill_root,
                fresh_root,
                "native",
                reserved_value,
            )
        except ValueError:
            pass
        else:
            raise AssertionError("accepted an out-of-order role resource manifest")

        review_path = specification_root / "review/requirements.yaml"
        review_path.parent.mkdir()
        review_inputs = [
            f"docs/changes/specs/export-data/request.md={file_hash(request_path)}",
            f"docs/changes/specs/export-data/idea.md={file_hash(idea_path)}",
            f"docs/changes/specs/export-data/requirements.md={file_hash(requirements_path)}",
        ]
        review_path.write_text(
            f"""schema_version: 2
stage: requirements
inputs:
  - path: docs/changes/specs/export-data/request.md
    sha256: {file_hash(request_path)}
  - path: docs/changes/specs/export-data/idea.md
    sha256: {file_hash(idea_path)}
  - path: docs/changes/specs/export-data/requirements.md
    sha256: {file_hash(requirements_path)}
verdict: pass
findings: []
""",
            encoding="utf-8",
            newline="\n",
        )
        assert validate_review_path(review_path, "requirements", review_inputs)[
            "verdict"
        ] == "pass"
        expected_review_inputs = [parse_input_hash(value) for value in review_inputs]
        blocking_finding = {
            "id": "REQ-R-001",
            "severity": "blocking",
            "resolution": "author-revision",
            "references": ['ADDED Requirement "Export History"'],
            "problem": "The export scope is incomplete.",
            "recommendation": "Define the export scope from approved inputs.",
        }
        changes_required_review = {
            "schema_version": 2,
            "stage": "requirements",
            "inputs": expected_review_inputs,
            "verdict": "changes-required",
            "findings": [blocking_finding],
        }
        assert (
            validate_review_value(
                changes_required_review, "requirements", expected_review_inputs
            )["verdict"]
            == "changes-required"
        )
        advisory_finding = {
            **blocking_finding,
            "id": "REQ-R-002",
            "severity": "advisory",
            "resolution": "none",
        }
        advisory_review = {
            **changes_required_review,
            "verdict": "pass",
            "findings": [advisory_finding],
        }
        assert (
            validate_review_value(
                advisory_review, "requirements", expected_review_inputs
            )["verdict"]
            == "pass"
        )
        invalid_reviews = (
            {**changes_required_review, "findings": []},
            {**changes_required_review, "findings": [advisory_finding]},
            {
                **changes_required_review,
                "findings": [{**blocking_finding, "resolution": "none"}],
            },
            {
                **changes_required_review,
                "findings": [{**blocking_finding, "references": []}],
            },
            {
                **changes_required_review,
                "verdict": "pass",
                "findings": [blocking_finding],
            },
            {
                **changes_required_review,
                "verdict": "pass",
                "findings": [
                    {
                        **blocking_finding,
                        "severity": "advisory",
                        "resolution": "author-revision",
                    }
                ],
            },
        )
        for invalid_review in invalid_reviews:
            try:
                validate_review_value(
                    invalid_review, "requirements", expected_review_inputs
                )
            except ValueError:
                pass
            else:
                raise AssertionError(
                    "accepted an inconsistent review verdict or finding"
                )
        try:
            validate_review_path(review_path, "requirements", review_inputs[:1])
        except ValueError:
            pass
        else:
            raise AssertionError("accepted a review with mismatched input hashes")
        review_run_state = json.loads(json.dumps(reserved_value))
        review_run_state["status"] = "reviewing"
        review_run_state["next_run_sequence"] = 4
        review_run_state["active_run"] = {
            "run_id": "export-data--requirements--requirements-reviewer--3",
            "sequence": 3,
            "stage": "requirements",
            "role": "requirements-reviewer",
            "purpose": "review",
            "executor": "author",
            "adapter": "codex",
            "output": "docs/changes/specs/export-data/review/requirements.yaml",
            "request_sha256": file_hash(request_path),
        }
        validate_state_value(review_run_state, "export-data")
        re_review_manifest = {
            "schema_version": 2,
            "run_id": "export-data--requirements--requirements-reviewer--3",
            "spec_id": "export-data",
            "stage": "requirements",
            "role": "requirements-reviewer",
            "purpose": "review",
            "project_root": str(fresh_root.resolve()),
            "skill_root": str(skill_root.resolve()),
            "brief": ROLE_BRIEFS["requirements-reviewer"],
            "resources": role_resource_manifest(skill_root, "requirements-reviewer")[
                "resources"
            ],
            "inputs": [
                {"path": "docs/changes/specs/export-data/request.md", "sha256": file_hash(request_path)},
                {"path": "docs/changes/specs/export-data/idea.md", "sha256": file_hash(idea_path)},
                {
                    "path": "docs/changes/specs/export-data/requirements.md",
                    "sha256": file_hash(requirements_path),
                },
            ],
            "clarifications": [],
            "feedback": {
                "previous_review": {
                    "path": "docs/changes/specs/export-data/review/requirements.yaml",
                    "sha256": file_hash(review_path),
                },
                "unresolved_finding_ids": [],
            },
            "output": "docs/changes/specs/export-data/review/requirements.yaml",
            "allowed_write": "docs/changes/specs/export-data/review/requirements.yaml",
        }
        validate_role_manifest(
            re_review_manifest,
            skill_root,
            fresh_root,
            "native",
            review_run_state,
        )
        invalid_feedback_manifest = json.loads(json.dumps(re_review_manifest))
        invalid_feedback_manifest["feedback"]["unresolved_finding_ids"] = [
            "REQ-R-999"
        ]
        try:
            validate_role_manifest(
                invalid_feedback_manifest,
                skill_root,
                fresh_root,
                "native",
                review_run_state,
            )
        except ValueError:
            pass
        else:
            raise AssertionError("accepted finding IDs absent from the previous review")
        try:
            validate_role_manifest(
                {key: value for key, value in re_review_manifest.items() if key != "feedback"},
                skill_root,
                fresh_root,
                "native",
                review_run_state,
            )
        except ValueError:
            pass
        else:
            raise AssertionError("accepted a re-review without previous review feedback")
        try:
            parse_json_receipt("x" * (CONTROL_FILE_MAX_BYTES + 1))
        except ValueError:
            pass
        else:
            raise AssertionError("accepted an oversized JSON control input")

        conflict_root = temporary_root / "conflict"
        conflict_path = conflict_root / ".codex/agents/stepan_orchestrator.toml"
        conflict_path.parent.mkdir(parents=True)
        conflict_path.write_text("conflict\n", encoding="utf-8", newline="\n")
        try:
            initialize_codex(conflict_root, "codex")
        except ValueError:
            pass
        else:
            raise AssertionError("initialized files despite a preflight conflict")
        assert conflict_path.read_bytes() == b"conflict\n"
        assert sorted(
            path.relative_to(conflict_root).as_posix()
            for path in conflict_root.rglob("*")
            if path.is_file()
        ) == [".codex/agents/stepan_orchestrator.toml"]

        claude_conflict_root = temporary_root / "claude-conflict"
        claude_conflict_path = (
            claude_conflict_root / ".claude/agents/stepan-orchestrator.md"
        )
        claude_conflict_path.parent.mkdir(parents=True)
        claude_conflict_path.write_text("conflict\n", encoding="utf-8", newline="\n")
        try:
            initialize_claude(claude_conflict_root, "codex")
        except ValueError:
            pass
        else:
            raise AssertionError("initialized Claude files despite a preflight conflict")
        assert claude_conflict_path.read_bytes() == b"conflict\n"
        assert sorted(
            path.relative_to(claude_conflict_root).as_posix()
            for path in claude_conflict_root.rglob("*")
            if path.is_file()
        ) == [".claude/agents/stepan-orchestrator.md"]

    self_test_progressive_disclosure(skill_root)
    self_test_audit_acceptance_coverage(skill_root, executed_audit_capabilities)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)

    identifier = commands.add_parser(
        "spec-id", help="Generate a Stepan spec ID from a semantic hint"
    )
    identifier.add_argument("--id-hint", required=True)
    identifier.add_argument("--root", type=Path, default=Path("docs/changes/specs"))

    run_identifier = commands.add_parser("run-id", help="Generate a role run ID")
    run_identifier.add_argument("--spec-id", required=True)
    run_identifier.add_argument("--stage", required=True, choices=STAGES)
    run_identifier.add_argument("--role", required=True, choices=ROLES)
    run_identifier.add_argument("--sequence", required=True, type=int)

    approval = commands.add_parser(
        "approval-transition",
        help="Validate one feature approval checkpoint transition",
    )
    approval.add_argument("--action", required=True, choices=APPROVAL_ACTIONS)
    approval.add_argument("--stage", required=True, choices=STAGES)
    approval.add_argument("--status", required=True)
    approval.add_argument("--pending", action="store_true")
    approval.add_argument(
        "--review-verdict", choices=("pass", "changes-required")
    )
    approval.add_argument("--unresolved-user-decision", action="store_true")

    reservation = commands.add_parser(
        "reserve-run", help="Atomically reserve one role run in state.yaml"
    )
    reservation.add_argument("--state", required=True, type=Path)
    reservation.add_argument("--project-root", required=True, type=Path)
    reservation.add_argument("--spec-id", required=True)
    reservation.add_argument("--stage", required=True, choices=STAGES)
    reservation.add_argument("--role", required=True, choices=ROLES)
    reservation.add_argument("--purpose", required=True, choices=PURPOSES)
    reservation.add_argument("--executor", required=True)
    reservation.add_argument("--adapter", required=True, choices=ADAPTER_KINDS)
    reservation.add_argument("--output", required=True)
    reservation.add_argument("--request-sha256", required=True)

    config = commands.add_parser(
        "validate-config", help="Validate and normalize project execution configuration"
    )
    config.add_argument("--config", required=True, type=Path)
    config.add_argument("--project-root", required=True, type=Path)
    config.add_argument("--host", required=True)

    state_validator = commands.add_parser(
        "validate-state", help="Validate one feature state file and its pinned inputs"
    )
    state_validator.add_argument("--state", required=True, type=Path)
    state_validator.add_argument("--project-root", required=True, type=Path)
    state_validator.add_argument("--spec-id", required=True)

    feature_initializer = commands.add_parser(
        "initialize-feature",
        help="Atomically initialize request.md, mem-log.md, and state.yaml",
    )
    feature_initializer.add_argument("--project-root", required=True, type=Path)
    feature_initializer.add_argument("--spec-id", required=True)

    audit_flush = commands.add_parser(
        "flush-audit",
        help="Idempotently flush the pending state audit outbox",
    )
    audit_flush.add_argument("--state", required=True, type=Path)
    audit_flush.add_argument("--project-root", required=True, type=Path)
    audit_flush.add_argument("--spec-id", required=True)

    checkpoint_commit = commands.add_parser(
        "checkpoint-commit",
        help="Create or recover one isolated, audit-linked checkpoint commit",
    )
    checkpoint_commit.add_argument("--state", required=True, type=Path)
    checkpoint_commit.add_argument("--project-root", required=True, type=Path)
    checkpoint_commit.add_argument("--spec-id", required=True)

    role_result = commands.add_parser(
        "accept-role-result",
        help="Atomically accept one completed role result and queue its audit batch",
    )
    role_result.add_argument("--state", required=True, type=Path)
    role_result.add_argument("--project-root", required=True, type=Path)
    role_result.add_argument("--spec-id", required=True)
    role_result.add_argument("--input", action="append", default=[])
    role_result.add_argument("--review-question")

    workflow_transition = commands.add_parser(
        "workflow-transition",
        help="Atomically apply one table-defined feature workflow transition",
    )
    workflow_transition.add_argument("--state", required=True, type=Path)
    workflow_transition.add_argument("--project-root", required=True, type=Path)
    workflow_transition.add_argument("--spec-id", required=True)
    workflow_transition.add_argument(
        "--name",
        required=True,
        choices=tuple(
            sorted(
                name
                for name, definition in FEATURE_TRANSITION_TABLE.items()
                if definition["mode"]
                in {"workflow-transition", "observation", "hard-stop"}
            )
        ),
    )

    resources = commands.add_parser(
        "role-resources", help="Resolve a role brief's ordered direct resources"
    )
    resources.add_argument("--skill-root", required=True, type=Path)
    resources.add_argument("--role", required=True, choices=ROLES)

    manifest = commands.add_parser(
        "validate-role-manifest",
        help="Validate one native or mailbox role-run manifest from standard input",
    )
    manifest.add_argument("--skill-root", required=True, type=Path)
    manifest.add_argument("--project-root", required=True, type=Path)
    manifest.add_argument("--state", required=True, type=Path)
    manifest.add_argument("--spec-id", required=True)
    manifest.add_argument("--adapter", required=True, choices=("native", "mailbox"))

    artifact = commands.add_parser(
        "validate-artifact", help="Validate one requirements, design, or plan artifact"
    )
    artifact.add_argument("--kind", required=True, choices=("requirements", "design", "plan"))
    artifact.add_argument("--file", required=True, type=Path)
    artifact.add_argument("--requirements", type=Path)
    artifact.add_argument("--design", type=Path)

    review = commands.add_parser("validate-review", help="Validate one review YAML file")
    review.add_argument("--file", required=True, type=Path)
    review.add_argument("--stage", required=True, choices=("requirements", "design"))
    review.add_argument("--input", action="append", default=[])

    hashing = commands.add_parser("hash", help="Hash canonical Markdown or YAML")
    hashing.add_argument("files", nargs="+", type=Path)

    receipt = commands.add_parser(
        "validate-receipt", help="Validate one role receipt read from standard input"
    )
    receipt.add_argument("--run-id", required=True)
    receipt.add_argument("--output", required=True)
    receipt.add_argument("--adapter", required=True, choices=("native", "mailbox"))
    receipt.add_argument("--requested-model")
    receipt.add_argument("--requested-reasoning")

    commands.add_parser(
        "validate-router-result",
        help="Validate one dedicated router result read from standard input",
    )

    initializer = commands.add_parser(
        "init-codex", help="Create recommended project-local Codex agents"
    )
    initializer.add_argument("--host", required=True)
    initializer.add_argument("--project-root", type=Path, default=Path("."))

    claude_initializer = commands.add_parser(
        "init-claude", help="Create recommended project-local Claude Code agents"
    )
    claude_initializer.add_argument("--host", required=True)
    claude_initializer.add_argument("--project-root", type=Path, default=Path("."))

    commands.add_parser("self-test", help="Run built-in checks")
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        if args.command == "spec-id":
            output = collision_result(args.id_hint, args.root)
        elif args.command == "run-id":
            output = {
                "run_id": role_run_id(
                    args.spec_id, args.stage, args.role, args.sequence
                )
            }
        elif args.command == "approval-transition":
            output = approval_transition(
                args.action,
                args.stage,
                args.status,
                args.pending,
                args.review_verdict,
                args.unresolved_user_decision,
            )
        elif args.command == "reserve-run":
            output = reserve_run_in_state(
                args.state,
                args.project_root,
                args.spec_id,
                args.stage,
                args.role,
                args.purpose,
                args.executor,
                args.adapter,
                args.output,
                args.request_sha256,
            )
        elif args.command == "validate-config":
            output = validate_project_config_path(
                args.config, args.project_root, args.host
            )
        elif args.command == "validate-state":
            _, output = validate_state_path(
                args.state, args.project_root, args.spec_id
            )
        elif args.command == "initialize-feature":
            initialization = parse_feature_initialization_json(sys.stdin.read())
            output = initialize_feature_specification(
                args.project_root,
                args.spec_id,
                initialization["initial_request"],
                initialization["execution"],
            )
        elif args.command == "flush-audit":
            output = flush_audit_outbox(
                args.state, args.project_root, args.spec_id
            )
        elif args.command == "checkpoint-commit":
            output = checkpoint_commit_in_state(
                args.state, args.project_root, args.spec_id
            )
        elif args.command == "accept-role-result":
            output = accept_completed_role_result_in_state(
                args.state,
                args.project_root,
                args.spec_id,
                parse_json_receipt(sys.stdin.read()),
                args.input,
                args.review_question,
            )
        elif args.command == "workflow-transition":
            output = apply_workflow_transition_in_state(
                args.state,
                args.project_root,
                args.spec_id,
                args.name,
                parse_json_receipt(sys.stdin.read()),
            )
        elif args.command == "role-resources":
            output = role_resource_manifest(args.skill_root, args.role)
        elif args.command == "validate-role-manifest":
            _, state = validate_state_path(
                args.state, args.project_root, args.spec_id
            )
            output = validate_role_manifest(
                parse_json_receipt(sys.stdin.read()),
                args.skill_root,
                args.project_root,
                args.adapter,
                state,
            )
        elif args.command == "validate-artifact":
            output = validate_artifact_path(
                args.kind, args.file, args.requirements, args.design
            )
        elif args.command == "validate-review":
            output = validate_review_path(args.file, args.stage, args.input)
        elif args.command == "hash":
            output = {
                "files": [
                    {"path": path.as_posix(), "sha256": file_hash(path)}
                    for path in args.files
                ]
            }
        elif args.command == "validate-receipt":
            output = validate_receipt(
                parse_json_receipt(sys.stdin.read()),
                args.run_id,
                args.output,
                args.adapter,
                args.requested_model,
                args.requested_reasoning,
            )
        elif args.command == "validate-router-result":
            output = validate_router_result(parse_json_receipt(sys.stdin.read()))
        elif args.command == "init-codex":
            output = initialize_codex(args.project_root, args.host)
        elif args.command == "init-claude":
            output = initialize_claude(args.project_root, args.host)
        else:
            self_test()
            output = {"ok": True}
    except (OSError, UnicodeError, ValueError) as error:
        print(f"stepan.py: {error}", file=sys.stderr)
        return 1

    print(json.dumps(output, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
