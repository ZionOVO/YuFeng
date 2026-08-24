#!/usr/bin/env python3
"""Seal and verify the immutable YuFeng software release artifact set."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
import sys
import tarfile
from typing import Any


SCHEMA = "yufeng.software-release/v1"
ARCHIVE_KINDS = (
    "linux-amd64",
    "linux-arm64",
    "linux-mips",
    "windows-amd64",
    "darwin-amd64",
    "darwin-arm64",
    "console",
    "modelside-python",
    "deployment",
    "edge-image-linux-amd64",
    "modelside-image-linux-amd64",
)
VERSION_PATTERN = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
COMMIT_PATTERN = re.compile(r"^[0-9a-f]{40}$")
SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")
WORKFLOW_RUN_PATTERN = re.compile(r"^[A-Za-z0-9._-]+$")


class ArtifactError(ValueError):
    """ArtifactError reports a release artifact contract violation."""


def archive_names(version: str) -> tuple[str, ...]:
    return tuple(f"yufeng-{version}-{kind}.tar.gz" for kind in ARCHIVE_KINDS)


def manifest_name(version: str) -> str:
    return f"yufeng-{version}-release-manifest.json"


def checksums_name(version: str) -> str:
    return f"yufeng-{version}-checksums.txt"


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def validate_version(version: str) -> None:
    if not VERSION_PATTERN.fullmatch(version):
        raise ArtifactError(f"invalid release version: {version!r}")


def validate_commit(commit: str) -> None:
    if not COMMIT_PATTERN.fullmatch(commit):
        raise ArtifactError(f"invalid source commit: {commit!r}")


def validate_generated_at(value: str) -> None:
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ArtifactError("generated-at must be an ISO 8601 timestamp") from exc
    if parsed.tzinfo is None:
        raise ArtifactError("generated-at must include a time zone")


def safe_member_name(name: str) -> None:
    normalized = name.replace("\\", "/")
    path = pathlib.PurePosixPath(normalized)
    if (
        not normalized
        or normalized.startswith("/")
        or path.is_absolute()
        or ".." in path.parts
        or re.match(r"^[A-Za-z]:", normalized)
    ):
        raise ArtifactError(f"unsafe archive member path: {name!r}")


def safe_link_target(member_name: str, target: str) -> None:
    normalized = target.replace("\\", "/")
    target_path = pathlib.PurePosixPath(normalized)
    if not normalized or normalized.startswith("/") or target_path.is_absolute():
        raise ArtifactError(f"unsafe archive link target in {member_name!r}: {target!r}")
    resolved = pathlib.PurePosixPath(member_name.replace("\\", "/")).parent.joinpath(target_path)
    depth = 0
    for part in resolved.parts:
        if part == "..":
            depth -= 1
        elif part not in ("", "."):
            depth += 1
        if depth < 0:
            raise ArtifactError(f"archive link escapes root in {member_name!r}: {target!r}")


def validate_tar(path: pathlib.Path) -> None:
    try:
        with tarfile.open(path, mode="r:gz") as archive:
            count = 0
            for member in archive:
                count += 1
                if count > 100_000:
                    raise ArtifactError(f"archive has too many members: {path.name}")
                safe_member_name(member.name)
                if member.issym() or member.islnk():
                    safe_link_target(member.name, member.linkname)
    except (OSError, tarfile.TarError) as exc:
        raise ArtifactError(f"cannot read tar archive {path.name}: {exc}") from exc


def directory_entries(directory: pathlib.Path) -> set[str]:
    try:
        entries = list(directory.iterdir())
    except OSError as exc:
        raise ArtifactError(f"cannot read release directory: {exc}") from exc
    non_files = sorted(entry.name for entry in entries if entry.is_symlink() or not entry.is_file())
    if non_files:
        raise ArtifactError(f"release directory contains non-files: {', '.join(non_files)}")
    return {entry.name for entry in entries}


def require_exact_entries(directory: pathlib.Path, expected: set[str]) -> None:
    actual = directory_entries(directory)
    missing = sorted(expected - actual)
    extra = sorted(actual - expected)
    if missing or extra:
        details = []
        if missing:
            details.append("missing=" + ",".join(missing))
        if extra:
            details.append("extra=" + ",".join(extra))
        raise ArtifactError("release file set mismatch: " + " ".join(details))


def load_manifest(path: pathlib.Path) -> dict[str, Any]:
    try:
        raw = path.read_text(encoding="utf-8")
        document = json.loads(raw)
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ArtifactError(f"cannot read release manifest: {exc}") from exc
    if not isinstance(document, dict):
        raise ArtifactError("release manifest must be an object")
    return document


def write_new(path: pathlib.Path, content: str) -> None:
    try:
        with path.open("x", encoding="utf-8", newline="\n") as stream:
            stream.write(content)
    except FileExistsError as exc:
        raise ArtifactError(f"refusing to overwrite {path.name}") from exc
    except OSError as exc:
        raise ArtifactError(f"cannot write {path.name}: {exc}") from exc


def seal(args: argparse.Namespace) -> None:
    directory = pathlib.Path(args.directory).resolve()
    validate_version(args.version)
    validate_commit(args.source_commit)
    validate_generated_at(args.generated_at)
    if not WORKFLOW_RUN_PATTERN.fullmatch(args.workflow_run):
        raise ArtifactError("workflow-run has an invalid format")

    archives = archive_names(args.version)
    require_exact_entries(directory, set(archives))
    assets: list[dict[str, Any]] = []
    for name in archives:
        path = directory / name
        validate_tar(path)
        assets.append({"name": name, "bytes": path.stat().st_size, "sha256": sha256(path)})

    manifest = {
        "schema": SCHEMA,
        "version": args.version,
        "source-commit": args.source_commit,
        "workflow-run": args.workflow_run,
        "generated-at": args.generated_at,
        "assets": assets,
    }
    manifest_path = directory / manifest_name(args.version)
    write_new(manifest_path, json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")

    checksum_paths = [directory / name for name in archives] + [manifest_path]
    checksum_lines = [f"{sha256(path)}  {path.name}\n" for path in checksum_paths]
    write_new(directory / checksums_name(args.version), "".join(checksum_lines))
    verify(args)


def parse_checksums(path: pathlib.Path) -> dict[str, str]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except (OSError, UnicodeError) as exc:
        raise ArtifactError(f"cannot read checksums: {exc}") from exc
    parsed: dict[str, str] = {}
    for line in lines:
        match = re.fullmatch(r"([0-9a-f]{64})  ([^/\\]+)", line)
        if match is None:
            raise ArtifactError(f"invalid checksum line: {line!r}")
        digest, name = match.groups()
        if name in parsed:
            raise ArtifactError(f"duplicate checksum entry: {name}")
        parsed[name] = digest
    return parsed


def verify(args: argparse.Namespace) -> None:
    directory = pathlib.Path(args.directory).resolve()
    validate_version(args.version)
    validate_commit(args.source_commit)
    archives = archive_names(args.version)
    manifest_filename = manifest_name(args.version)
    checksum_filename = checksums_name(args.version)
    require_exact_entries(directory, set(archives) | {manifest_filename, checksum_filename})

    manifest_path = directory / manifest_filename
    manifest = load_manifest(manifest_path)
    expected_keys = {"schema", "version", "source-commit", "workflow-run", "generated-at", "assets"}
    if set(manifest) != expected_keys:
        raise ArtifactError("release manifest fields do not match the schema")
    if manifest["schema"] != SCHEMA:
        raise ArtifactError("release manifest schema mismatch")
    if manifest["version"] != args.version:
        raise ArtifactError("release manifest version mismatch")
    if manifest["source-commit"] != args.source_commit:
        raise ArtifactError("release manifest source commit mismatch")
    if args.workflow_run and manifest["workflow-run"] != args.workflow_run:
        raise ArtifactError("release manifest workflow run mismatch")
    if not isinstance(manifest["workflow-run"], str) or not WORKFLOW_RUN_PATTERN.fullmatch(manifest["workflow-run"]):
        raise ArtifactError("release manifest workflow run has an invalid format")
    if not isinstance(manifest["generated-at"], str):
        raise ArtifactError("release manifest generated-at must be a string")
    validate_generated_at(manifest["generated-at"])

    assets = manifest["assets"]
    if not isinstance(assets, list) or len(assets) != len(archives):
        raise ArtifactError("release manifest asset count mismatch")
    expected_assets: dict[str, dict[str, Any]] = {}
    for asset in assets:
        if not isinstance(asset, dict) or set(asset) != {"name", "bytes", "sha256"}:
            raise ArtifactError("release manifest asset fields do not match the schema")
        name = asset["name"]
        size = asset["bytes"]
        digest = asset["sha256"]
        if not isinstance(name, str) or name in expected_assets:
            raise ArtifactError("release manifest contains an invalid or duplicate asset name")
        if not isinstance(size, int) or isinstance(size, bool) or size < 1:
            raise ArtifactError(f"release manifest contains an invalid size for {name!r}")
        if not isinstance(digest, str) or not SHA256_PATTERN.fullmatch(digest):
            raise ArtifactError(f"release manifest contains an invalid digest for {name!r}")
        expected_assets[name] = asset
    if set(expected_assets) != set(archives):
        raise ArtifactError("release manifest archive names mismatch")

    for name in archives:
        path = directory / name
        validate_tar(path)
        asset = expected_assets[name]
        if path.stat().st_size != asset["bytes"]:
            raise ArtifactError(f"release asset size mismatch: {name}")
        if sha256(path) != asset["sha256"]:
            raise ArtifactError(f"release asset digest mismatch: {name}")

    expected_checksum_names = set(archives) | {manifest_filename}
    checksums = parse_checksums(directory / checksum_filename)
    if set(checksums) != expected_checksum_names:
        raise ArtifactError("checksum file set mismatch")
    for name, expected_digest in checksums.items():
        if sha256(directory / name) != expected_digest:
            raise ArtifactError(f"checksum mismatch: {name}")

    result = {
        "schema": SCHEMA,
        "version": args.version,
        "source-commit": args.source_commit,
        "workflow-run": manifest["workflow-run"],
        "files": len(archives) + 2,
        "manifest-sha256": sha256(manifest_path),
    }
    print(json.dumps(result, sort_keys=True))


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    subparsers = result.add_subparsers(dest="command", required=True)
    for command in ("seal", "verify"):
        subparser = subparsers.add_parser(command)
        subparser.add_argument("--directory", required=True)
        subparser.add_argument("--version", required=True)
        subparser.add_argument("--source-commit", required=True)
        subparser.add_argument("--workflow-run", default="")
        if command == "seal":
            subparser.add_argument("--generated-at", required=True)
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        if args.command == "seal":
            seal(args)
        else:
            verify(args)
    except ArtifactError as exc:
        print(f"release artifact verification failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
