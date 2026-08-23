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
ADAPTER_KINDS = ("codex", "claude-code", "mailbox")
APPROVAL_ACTIONS = ("continue", "continue-and-commit")
CODEX_HOST = "codex"
CONTROL_FILE_MAX_BYTES = 256 * 1024
ARTIFACT_MAX_BYTES = 2 * 1024 * 1024
PROJECT_INPUT_MAX_BYTES = 10 * 1024 * 1024

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

    def parse_mapping(index: int, indentation: int) -> tuple[dict[str, object], int]:
        mapping: dict[str, object] = {}
        while index < len(tokens):
            current_indent, text, line_number = tokens[index]
            if current_indent < indentation:
                break
            if current_indent != indentation or text.startswith("- "):
                raise ValueError(f"invalid {name} mapping on line {line_number}")
            match = re.fullmatch(r"([a-zA-Z][a-zA-Z0-9_.-]*):(.*)", text)
            if match is None:
                raise ValueError(f"invalid {name} key on line {line_number}")
            key, remainder = match.groups()
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
            mapping_match = re.fullmatch(r"([a-zA-Z][a-zA-Z0-9_.-]*):(.*)", remainder)
            if mapping_match is None:
                sequence.append(_parse_yaml_scalar(remainder, line_number))
                index += 1
                continue
            key, value_text = mapping_match.groups()
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
    contracts_section = content.split("## Task", 1)[0]
    resources: list[str] = []
    modules_root = (skill_root / "references/modules").resolve(strict=True)
    modules_prefix = str(modules_root) + os.sep
    for match in re.finditer(r"\[[^\]]+\]\(([^)]+)\)", contracts_section):
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
        raise ValueError("execution contract must show native and mailbox role manifests")
    expected_resources = role_resource_manifest(skill_root, "design-author")["resources"]
    if any(example.get("resources") != expected_resources for example in examples):
        raise ValueError("role manifest example resources differ from the role brief")
    native = next((value for value in examples if "executor" not in value), None)
    mailbox = next((value for value in examples if "executor" in value), None)
    if native is None or mailbox is None:
        raise ValueError("role manifest examples must distinguish native and mailbox")
    if {key: value for key, value in mailbox.items() if key != "executor"} != native:
        raise ValueError("native and mailbox role manifests have different semantics")


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
    state_path: Path, project_root: Path, specification: str
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
    request_path = resolve_project_file(
        project_root,
        f"docs/changes/specs/{specification}/request.md",
        "initial request",
        ARTIFACT_MAX_BYTES,
    )
    if file_hash(request_path) != state["initial_request_sha256"]:
        raise ValueError("state initial request hash does not match request.md")
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
    lines = content.replace("\r\n", "\n").replace("\r", "\n").splitlines()
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
    if len(content.encode("utf-8")) > CONTROL_FILE_MAX_BYTES:
        raise ValueError(f"JSON control input exceeds {CONTROL_FILE_MAX_BYTES} bytes")
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
        if finding["severity"] not in {"blocking", "advisory"}:
            raise ValueError("invalid finding severity")
        if finding["resolution"] not in {"author-revision", "user-decision", "none"}:
            raise ValueError("invalid finding resolution")
        if not isinstance(finding["references"], list) or any(
            not isinstance(reference, str) or not reference.strip()
            for reference in finding["references"]
        ):
            raise ValueError("finding references must be non-empty strings")
        require_non_empty_string(finding["problem"], "finding problem")
        require_non_empty_string(finding["recommendation"], "finding recommendation")
        blocking = blocking or finding["severity"] == "blocking"
    if review["verdict"] == "pass" and blocking:
        raise ValueError("passing review must not contain blocking findings")
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


def validate_role_manifest(
    value: object,
    skill_root: Path,
    project_root: Path,
    adapter_class: str,
    state: dict[str, object],
) -> dict[str, object]:
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
        resolved = resolve_project_file(
            project_root, path, "role manifest input", PROJECT_INPUT_MAX_BYTES
        )
        if file_hash(resolved) != digest:
            raise ValueError(f"role manifest input hash does not match: {path}")

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
    profile_name = state["execution"]["bindings"][role]
    project_inputs = state["execution"]["profiles"][profile_name]["project_inputs"]
    expected_input_paths = required_inputs + [item["path"] for item in project_inputs]
    if [item["path"] for item in inputs] != expected_input_paths:
        raise ValueError("role manifest inputs do not match required and pinned inputs")

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


def self_test_state_content(
    request_hash: str, idea_hash: str = "sha256:" + "d" * 64
) -> str:
    return f"""schema_version: 1
specification: export-data
created_at: 2026-08-15T12:00:00Z
initial_request_sha256: {request_hash}
stage: requirements
status: drafting
automatic_revision_attempts: 0
next_run_sequence: 2
approvals:
  idea:
    artifact_sha256: {idea_hash}
    review_sha256: null
    accepted_risks: []
clarifications: []
pending: null
active_run: null
execution:
  source: project
  config_sha256: sha256:{'c' * 64}
  adapters:
    codex:
      kind: codex
  profiles:
    orchestrator:
      adapter: codex
      agent: stepan_orchestrator
      project_inputs: []
    author:
      adapter: codex
      agent: stepan_author
      project_inputs: []
  bindings:
    router: orchestrator
    idea-author: author
    requirements-author: author
    requirements-reviewer: author
    design-author: author
    specification-reviewer: author
    planner: author
"""


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
    request_hash = "sha256:" + "b" * 64
    state_content = self_test_state_content(request_hash)
    state = validate_state_value(parse_strict_yaml(state_content, "state"), "export-data")
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
    skill_root = Path(__file__).resolve().parent.parent
    for role in ROLES:
        manifest = role_resource_manifest(skill_root, role)
        assert manifest["brief"] == ROLE_BRIEFS[role]
        assert manifest["resources"]
    assert role_resource_manifest(skill_root, "design-author")["resources"] == [
        "references/modules/idea/artifact.md",
        "references/modules/requirements/artifact.md",
        "references/modules/idea/common.md",
        "references/modules/requirements/common.md",
        "references/modules/design/common.md",
        "references/modules/design/authoring.md",
        "references/modules/design/artifact.md",
    ]
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
        request_path = specification_root / "request.md"
        request_path.write_text("Export transaction history.\n", encoding="utf-8", newline="\n")
        idea_path = specification_root / "idea.md"
        idea_path.write_text(
            "# Idea\n## Outcome\nExport history.\n",
            encoding="utf-8",
            newline="\n",
        )
        state_path = specification_root / "state.yaml"
        state_path.write_text(
            self_test_state_content(file_hash(request_path), file_hash(idea_path)),
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
        _, reserved_value = validate_state_path(state_path, fresh_root, "export-data")
        assert reserved_value["active_run"]["role"] == "requirements-author"
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
