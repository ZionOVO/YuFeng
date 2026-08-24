#!/usr/bin/env python3
"""校验发布拉取请求与注解标签共享的机器可读元数据。"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


FIELDS = (
    "release-version",
    "evidence-commit",
    "evidence-tree",
    "evidence-sha256",
    "evidence-result",
)
VERSION_PATTERN = re.compile(r"^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$")
OBJECT_PATTERN = re.compile(r"^[0-9a-f]{40,64}$")
SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")


class MetadataError(ValueError):
    """MetadataError 表示发布元数据不完整、重复或不匹配。"""


def load_metadata(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    counts = {field: 0 for field in FIELDS}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        for field in FIELDS:
            prefix = field + "="
            if line.startswith(prefix):
                counts[field] += 1
                values[field] = line[len(prefix) :]
                break
    missing = [field for field, count in counts.items() if count == 0]
    duplicate = [field for field, count in counts.items() if count > 1]
    if missing or duplicate:
        raise MetadataError(f"release metadata missing={missing} duplicate={duplicate}")
    if not VERSION_PATTERN.fullmatch(values["release-version"]):
        raise MetadataError("release-version is not a semantic version")
    if not OBJECT_PATTERN.fullmatch(values["evidence-commit"]):
        raise MetadataError("evidence-commit is not a Git object id")
    if not OBJECT_PATTERN.fullmatch(values["evidence-tree"]):
        raise MetadataError("evidence-tree is not a Git object id")
    if not SHA256_PATTERN.fullmatch(values["evidence-sha256"]):
        raise MetadataError("evidence-sha256 is not a SHA-256 digest")
    if values["evidence-result"] != "passed":
        raise MetadataError("evidence-result must be passed")
    return values


def require_expected(values: dict[str, str], arguments: argparse.Namespace) -> None:
    expected = {
        "release-version": arguments.expected_version,
        "evidence-commit": arguments.expected_commit,
        "evidence-tree": arguments.expected_tree,
        "evidence-sha256": arguments.expected_sha256,
    }
    for field, wanted in expected.items():
        if wanted is not None and values[field] != wanted:
            raise MetadataError(f"{field} does not match the expected value")


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser()
    commands = root.add_subparsers(dest="command", required=True)
    for name in ("verify", "get"):
        command = commands.add_parser(name)
        command.add_argument("--file", required=True, type=Path)
        command.add_argument("--expected-version")
        command.add_argument("--expected-commit")
        command.add_argument("--expected-tree")
        command.add_argument("--expected-sha256")
        if name == "get":
            command.add_argument("--field", required=True, choices=FIELDS)
    return root


def main() -> int:
    arguments = parser().parse_args()
    try:
        values = load_metadata(arguments.file)
        require_expected(values, arguments)
    except (OSError, MetadataError) as error:
        print(str(error), file=sys.stderr)
        return 1
    if arguments.command == "get":
        print(values[arguments.field])
    else:
        print(json.dumps(values, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
