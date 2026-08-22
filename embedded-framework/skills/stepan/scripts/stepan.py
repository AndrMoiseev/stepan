#!/usr/bin/env -S uv run --no-project --no-python-downloads --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Deterministic helpers for the Stepan router."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import tomllib
from collections.abc import Callable
from pathlib import Path, PurePosixPath
from tempfile import TemporaryDirectory, mkstemp


SPEC_ID_PATTERN = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")
LOCAL_ID_PATTERN = re.compile(r"[a-z][a-z0-9]*(?:-[a-z0-9]+)*")
HASH_PATTERN = re.compile(r"sha256:[0-9a-f]{64}")
STAGES = ("idea", "requirements", "design", "plan")
ROLES = (
    "idea-author",
    "requirements-author",
    "requirements-reviewer",
    "design-author",
    "specification-reviewer",
    "planner",
)
PURPOSES = ("draft", "revise", "review")
ADAPTER_KINDS = ("codex", "claude-code", "mailbox")
APPROVAL_ACTIONS = ("continue", "continue-and-commit")
CODEX_HOST = "codex"


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


def codex_agent_instructions(profile: str) -> str:
    if profile == "orchestrator":
        return (
            "Act only as a Stepan workflow orchestrator when given one "
            "router launch manifest.\n"
            "Read and follow every protocol, execution, router, and adapter "
            "contract declared by that manifest.\n"
            "Never invoke the Stepan skill recursively or treat parent "
            "conversation as product input.\n"
            "Dispatch only the fresh role runs selected by the persisted "
            "workflow state.\n"
            "Return only the exact JSON router result required by the router "
            "contract."
        )
    return """Act only as a Stepan workflow role executor when given one role-run manifest.
Read only the skill resources and project inputs declared by that manifest.
Follow the selected role brief and its directly linked contracts exactly.
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
                "",
            )
        )
    lines.extend(("workflows:", "  feature:", "    router: orchestrator", "    roles:"))
    lines.extend(f"      {role}: {profile}" for role, profile in CODEX_ROLE_BINDINGS)
    return "\n".join(lines) + "\n"


def parse_generated_mapping_yaml(content: str) -> dict[str, object]:
    if content.startswith("\ufeff"):
        raise ValueError("generated YAML must not contain a byte-order mark")
    if "\r" in content or not content.endswith("\n"):
        raise ValueError("generated YAML must use LF and end with a newline")

    result: dict[str, object] = {}
    stack: list[tuple[int, dict[str, object]]] = [(-2, result)]
    for line_number, line in enumerate(content.splitlines(), 1):
        if not line:
            continue
        match = re.fullmatch(r"( *)([a-z][a-z0-9_-]*):(.*)", line)
        if match is None or "\t" in line:
            raise ValueError(f"invalid generated YAML line {line_number}")
        indentation = len(match.group(1))
        if indentation % 2:
            raise ValueError(f"invalid generated YAML indentation on line {line_number}")
        while stack[-1][0] >= indentation:
            stack.pop()
        parent_indentation, parent = stack[-1]
        if indentation != parent_indentation + 2:
            raise ValueError(f"invalid generated YAML nesting on line {line_number}")

        key = match.group(2)
        if key in parent:
            raise ValueError(f"duplicate generated YAML key on line {line_number}")
        remainder = match.group(3)
        if remainder == "":
            child: dict[str, object] = {}
            parent[key] = child
            stack.append((indentation, child))
            continue
        if not remainder.startswith(" ") or remainder != f" {remainder[1:].strip()}":
            raise ValueError(f"invalid generated YAML scalar on line {line_number}")
        scalar = remainder[1:]
        if scalar == "null":
            value: object = None
        elif re.fullmatch(r"0|[1-9][0-9]*", scalar):
            value = int(scalar)
        elif re.fullmatch(r"[A-Za-z0-9_.-]+", scalar):
            value = scalar
        else:
            raise ValueError(f"unsupported generated YAML scalar on line {line_number}")
        parent[key] = value
    return result


def expected_codex_config() -> dict[str, object]:
    return {
        "schema_version": 1,
        "adapters": {"codex": {"kind": "codex"}},
        "profiles": {
            profile: {"adapter": "codex", "agent": f"stepan_{profile}"}
            for profile, _, _, _ in CODEX_AGENT_PROFILES
        },
        "workflows": {
            "feature": {
                "router": "orchestrator",
                "roles": dict(CODEX_ROLE_BINDINGS),
            }
        },
    }


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


def validate_codex_contract_examples(
    files: tuple[tuple[PurePosixPath, bytes], ...]
) -> None:
    skill_root = Path(__file__).resolve().parent.parent
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


def _rollback_codex_init(
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


def initialize_codex(project_root: Path, current_host: str) -> dict[str, object]:
    if current_host != CODEX_HOST:
        raise ValueError("init-codex requires the current host to be Codex")
    if not project_root.exists() or not project_root.is_dir():
        raise ValueError("project root must be an existing directory")
    project_root = project_root.resolve(strict=True)
    if project_root.parent == project_root:
        raise ValueError("project root must not be a filesystem root")

    files = codex_init_files()
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
        _rollback_codex_init(created_files, created_directories)
        raise

    return {
        "schema_version": 1,
        "host": "codex",
        "status": "created" if created else "unchanged",
        "created": created,
        "unchanged": unchanged,
    }


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
    owners = {
        "idea": "idea-author",
        "requirements": "requirements-author",
        "design": "design-author",
        "plan": "planner",
    }
    review_roles = {"requirements-reviewer", "specification-reviewer"}
    if role in review_roles:
        expected_review_stage = (
            "requirements" if role == "requirements-reviewer" else "design"
        )
        if stage != expected_review_stage:
            raise ValueError("role is not valid for review stage")
        suffix = f"review/{stage}.yaml"
    elif owners.get(stage) == role:
        suffix = f"{stage}.md"
    else:
        raise ValueError("role does not own the selected stage")
    return f"docs/changes/specs/{specification}/{suffix}"


def reserved_state_content(
    content: str,
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

    if content.startswith("\ufeff"):
        raise ValueError("state file must not contain a byte-order mark")
    lines = content.replace("\r\n", "\n").replace("\r", "\n").splitlines()
    schema_lines = [line for line in lines if line.startswith("schema_version:")]
    if schema_lines != ["schema_version: 1"]:
        raise ValueError("state schema_version must be 1")
    sequence_matches = [
        (index, match)
        for index, line in enumerate(lines)
        if (match := re.fullmatch(r"next_run_sequence: ([1-9][0-9]*)", line))
    ]
    active_lines = [
        index for index, line in enumerate(lines) if line.startswith("active_run:")
    ]
    if len(sequence_matches) != 1:
        raise ValueError("state must contain one positive next_run_sequence")
    if len(active_lines) != 1 or lines[active_lines[0]] != "active_run: null":
        raise ValueError("state must contain no active run")

    sequence_index, sequence_match = sequence_matches[0]
    sequence = int(sequence_match.group(1))
    reservation = run_reservation(specification, stage, role, sequence)
    lines[sequence_index] = (
        f"next_run_sequence: {reservation['next_run_sequence']}"
    )
    active_index = active_lines[0]
    lines[active_index : active_index + 1] = [
        "active_run:",
        f"  run_id: {reservation['run_id']}",
        f"  sequence: {reservation['sequence']}",
        f"  stage: {stage}",
        f"  role: {role}",
        f"  purpose: {purpose}",
        f"  executor: {executor}",
        f"  adapter: {adapter}",
        f"  output: {output}",
        f"  request_sha256: {request_sha256}",
    ]

    return "\n".join(lines) + "\n", reservation


def reserve_run_in_state(
    state_path: Path,
    specification: str,
    stage: str,
    role: str,
    purpose: str,
    executor: str,
    adapter: str,
    output: str,
    request_sha256: str,
) -> dict[str, object]:
    if state_path.is_symlink() or not state_path.is_file():
        raise ValueError("state path must be a regular file")
    initial_stat = state_path.stat()
    initial_bytes = state_path.read_bytes()
    content = initial_bytes.decode("utf-8")
    reserved_content, reservation = reserved_state_content(
        content,
        specification,
        stage,
        role,
        purpose,
        executor,
        adapter,
        output,
        request_sha256,
    )

    descriptor, temporary_name = mkstemp(
        dir=state_path.parent, prefix=f".{state_path.name}.", suffix=".tmp"
    )
    temporary_path = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as stream:
            stream.write(reserved_content)
            stream.flush()
            os.fsync(stream.fileno())
        current_stat = state_path.stat()
        if state_path.is_symlink() or (
            current_stat.st_ino,
            current_stat.st_size,
            current_stat.st_mtime_ns,
        ) != (
            initial_stat.st_ino,
            initial_stat.st_size,
            initial_stat.st_mtime_ns,
        ):
            raise ValueError("state changed while reserving run")
        if state_path.read_bytes() != initial_bytes:
            raise ValueError("state changed while reserving run")
        os.chmod(temporary_path, initial_stat.st_mode)
        os.replace(temporary_path, state_path)
    finally:
        if temporary_path.exists():
            temporary_path.unlink()
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
    require_non_empty_string(expected_output, "expected output path")
    if adapter not in {"native", "mailbox"}:
        raise ValueError("invalid receipt adapter")
    if adapter == "native" and (
        requested_model is not None or requested_reasoning is not None
    ):
        raise ValueError("native receipt validation does not accept executor overrides")
    if adapter == "mailbox" and requested_model is None:
        raise ValueError("mailbox receipt validation requires requested model")

    receipt = require_object(
        value,
        "receipt",
        {"schema_version", "run_id", "status"},
        {"output", "executor", "question", "error"},
    )
    if type(receipt["schema_version"]) is not int or receipt["schema_version"] != 1:
        raise ValueError("invalid receipt schema version")
    actual_run_id = require_non_empty_string(receipt["run_id"], "receipt.run_id")
    if actual_run_id != expected_run_id:
        raise ValueError("receipt run ID does not match active run")

    status = require_non_empty_string(receipt["status"], "receipt.status")
    if status == "completed":
        required = {"schema_version", "run_id", "status", "output"}
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
    try:
        return json.loads(content)
    except json.JSONDecodeError as error:
        raise ValueError("invalid JSON receipt") from error


def canonicalize(content: bytes) -> bytes:
    text = content.decode("utf-8")
    if text.startswith("\ufeff"):
        text = text[1:]
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    return (text.rstrip("\n") + "\n").encode("utf-8")


def file_hash(path: Path) -> str:
    return f"sha256:{hashlib.sha256(canonicalize(path.read_bytes())).hexdigest()}"


def self_test() -> None:
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
    reserved_state, reservation = reserved_state_content(
        (
            "schema_version: 1\n"
            "next_run_sequence: 2\n"
            "pending: null\n"
            "active_run: null\n"
        ),
        "export-data",
        "requirements",
        "requirements-author",
        "draft",
        "native-default",
        "codex",
        "docs/changes/specs/export-data/requirements.md",
        "sha256:" + "b" * 64,
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
            "export-data",
            "requirements",
            "requirements-author",
            "draft",
            "native-default",
            "codex",
            "docs/changes/specs/export-data/requirements.md",
            "sha256:" + "b" * 64,
        )
    except ValueError:
        pass
    else:
        raise AssertionError("reserved a second run over an active run")
    try:
        reserved_state_content(
            "schema_version: 3\nnext_run_sequence: 1\nactive_run: null\n",
            "export-data",
            "idea",
            "idea-author",
            "draft",
            "author",
            "codex",
            "docs/changes/specs/export-data/idea.md",
            "sha256:" + "b" * 64,
        )
    except ValueError:
        pass
    else:
        raise AssertionError("accepted obsolete feature state schema version")
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
    assert (
        validate_receipt(
            completed,
            "export-data--requirements--requirements-author--2",
            "docs/changes/specs/export-data/requirements.md",
            "native",
        )
        == completed
    )
    mailbox_completed = {
        **completed,
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

    expected_paths = [path.as_posix() for path, _ in expected_files]
    with TemporaryDirectory(prefix="stepan-self-test-") as temporary:
        temporary_root = Path(temporary)

        wrong_host_root = temporary_root / "wrong-host"
        wrong_host_root.mkdir()
        try:
            initialize_codex(wrong_host_root, "claude-code")
        except ValueError:
            pass
        else:
            raise AssertionError("initialized Codex files on a non-Codex host")
        assert list(wrong_host_root.iterdir()) == []

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
    reservation.add_argument("--spec-id", required=True)
    reservation.add_argument("--stage", required=True, choices=STAGES)
    reservation.add_argument("--role", required=True, choices=ROLES)
    reservation.add_argument("--purpose", required=True, choices=PURPOSES)
    reservation.add_argument("--executor", required=True)
    reservation.add_argument("--adapter", required=True, choices=ADAPTER_KINDS)
    reservation.add_argument("--output", required=True)
    reservation.add_argument("--request-sha256", required=True)

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
                args.spec_id,
                args.stage,
                args.role,
                args.purpose,
                args.executor,
                args.adapter,
                args.output,
                args.request_sha256,
            )
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
