#!/usr/bin/env python3
"""Deterministic helpers for the Stepan router."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from collections.abc import Callable
from pathlib import Path


SPEC_ID_PATTERN = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")
STAGES = ("idea", "requirements", "design", "plan")
ROLES = ("framer", "specifier", "designer", "planner", "reviewer")


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
        role_run_id("export-data", "design", "designer", 2)
        == "export-data--design--designer--2"
    )
    for arguments in (
        ("bad--id", "design", "designer", 1),
        ("export-data", "invalid", "designer", 1),
        ("export-data", "design", "invalid", 1),
        ("export-data", "design", "designer", 0),
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


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)

    identifier = commands.add_parser(
        "spec-id", help="Generate a Stepan spec ID from a semantic hint"
    )
    identifier.add_argument("--id-hint", required=True)
    identifier.add_argument("--root", type=Path, default=Path(".stepan/specs"))

    run_identifier = commands.add_parser("run-id", help="Generate a role run ID")
    run_identifier.add_argument("--spec-id", required=True)
    run_identifier.add_argument("--stage", required=True, choices=STAGES)
    run_identifier.add_argument("--role", required=True, choices=ROLES)
    run_identifier.add_argument("--sequence", required=True, type=int)

    hashing = commands.add_parser("hash", help="Hash canonical Markdown or YAML")
    hashing.add_argument("files", nargs="+", type=Path)

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
        elif args.command == "hash":
            output = {
                "files": [
                    {"path": path.as_posix(), "sha256": file_hash(path)}
                    for path in args.files
                ]
            }
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
