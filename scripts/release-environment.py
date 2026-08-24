#!/usr/bin/env python3
"""生成并校验本机发布环境的确定性指纹。"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import sys
from pathlib import Path


SCHEMA = "yufeng.release-environment/v1"


class EnvironmentError(ValueError):
    """EnvironmentError 表示发布环境无法被安全识别或与预期不一致。"""


def run(*command: str, root: Path | None = None) -> str:
    try:
        completed = subprocess.run(
            command,
            cwd=root,
            check=True,
            capture_output=True,
            text=True,
            env=os.environ,
        )
    except (OSError, subprocess.CalledProcessError) as error:
        raise EnvironmentError(f"cannot run {' '.join(command)}") from error
    return completed.stdout.strip()


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def fingerprint(value: dict[str, object]) -> str:
    canonical = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(canonical).hexdigest()


def model_weights(directory: Path) -> list[dict[str, object]]:
    if not directory.is_dir():
        raise EnvironmentError("model weights directory is unavailable")
    records: list[dict[str, object]] = []
    for path in sorted(directory.rglob("*")):
        if path.is_symlink():
            raise EnvironmentError("model weights directory contains a symbolic link")
        if not path.is_file():
            continue
        records.append(
            {
                "path": path.relative_to(directory).as_posix(),
                "sha256": file_sha256(path),
                "bytes": path.stat().st_size,
            }
        )
    if not records:
        raise EnvironmentError("model weights directory is empty")
    return records


def capture(arguments: argparse.Namespace) -> str:
    root = arguments.root.resolve()
    weights = arguments.weights_dir.resolve()
    edge_unit = os.environ.get("YUFENG_EDGE_UNIT", "").strip()
    modelside_id = os.environ.get("YUFENG_MODELSIDE_ID", "").strip()
    edge_asset = os.environ.get("YUFENG_EDGE_ASSET", "").strip() or "asset-local-1"
    if not edge_unit or not modelside_id:
        raise EnvironmentError("release deployment identities are unavailable")
    node_version = run("node", "--version")
    if not node_version.startswith(f"v{arguments.require_node_major}."):
        raise EnvironmentError(
            f"release environment requires Node.js {arguments.require_node_major}"
        )
    try:
        hardware_raw = json.loads(run("system_profiler", "SPHardwareDataType", "-json"))
        docker_version = json.loads(run("docker", "version", "--format", "{{json .}}"))
    except json.JSONDecodeError as error:
        raise EnvironmentError("environment command returned invalid JSON") from error
    hardware_rows = hardware_raw.get("SPHardwareDataType", [])
    if not isinstance(hardware_rows, list) or not hardware_rows or not isinstance(hardware_rows[0], dict):
        raise EnvironmentError("hardware profile is unavailable")
    hardware = hardware_rows[0]
    chip = str(hardware.get("chip_type", ""))
    if arguments.require_chip and chip != arguments.require_chip:
        raise EnvironmentError(f"release environment requires {arguments.require_chip}")

    compose_files = (
        "deploy/compose.yaml",
        "deploy/compose.edge-modelside.yaml",
        "deploy/compose.test.yaml",
    )
    compose_command = ["docker", "compose"]
    for name in compose_files:
        compose_command.extend(("-f", name))
    body: dict[str, object] = {
        "schema": SCHEMA,
        "hardware": {
            "chip": chip,
            "memory": hardware.get("physical_memory", ""),
            "model-identifier": hardware.get("machine_model", ""),
            "model-name": hardware.get("machine_name", ""),
        },
        "operating-system": {
            "name": run("sw_vers", "-productName"),
            "version": run("sw_vers", "-productVersion"),
            "build": run("sw_vers", "-buildVersion"),
            "kernel": run("uname", "-r"),
            "architecture": run("uname", "-m"),
        },
        "deployment-identities": {
            "edge-unit": edge_unit,
            "edge-asset": edge_asset,
            "modelside-id": modelside_id,
        },
        "toolchain": {
            "go": run("go", "version"),
            "node": node_version,
            "npm": run("npm", "--version"),
            "buf": run("buf", "--version"),
            "docker-compose": run("docker", "compose", "version"),
        },
        "docker": docker_version,
        "compose": {
            "file-object-ids": {
                name: run("git", "hash-object", name, root=root) for name in compose_files
            },
            "configured-services": sorted(
                run(
                    *compose_command,
                    "config",
                    "--no-path-resolution",
                    "--services",
                    root=root,
                ).splitlines()
            ),
            "configured-images": sorted(
                run(
                    *compose_command,
                    "config",
                    "--no-path-resolution",
                    "--images",
                    root=root,
                ).splitlines()
            ),
            "configured-service-hashes": sorted(
                run(
                    *compose_command,
                    "config",
                    "--no-path-resolution",
                    "--hash",
                    "*",
                    root=root,
                ).splitlines()
            ),
        },
        "model-weights": model_weights(weights),
    }
    digest = fingerprint(body)
    body["fingerprint-sha256"] = digest
    arguments.output.parent.mkdir(parents=True, exist_ok=True)
    arguments.output.write_text(
        json.dumps(body, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return digest


def verify(arguments: argparse.Namespace) -> str:
    try:
        value = json.loads(arguments.file.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise EnvironmentError("cannot read release environment") from error
    if not isinstance(value, dict) or value.get("schema") != SCHEMA:
        raise EnvironmentError("release environment schema is unsupported")
    recorded = value.pop("fingerprint-sha256", None)
    if not isinstance(recorded, str) or recorded != fingerprint(value):
        raise EnvironmentError("release environment fingerprint is invalid")
    if arguments.expected_sha256 and recorded != arguments.expected_sha256:
        raise EnvironmentError("release environment fingerprint does not match")
    return recorded


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser()
    commands = root.add_subparsers(dest="command", required=True)
    capture_command = commands.add_parser("capture")
    capture_command.add_argument("--root", required=True, type=Path)
    capture_command.add_argument("--weights-dir", required=True, type=Path)
    capture_command.add_argument("--output", required=True, type=Path)
    capture_command.add_argument("--require-chip", default="Apple M4 Pro")
    capture_command.add_argument("--require-node-major", default="22")
    verify_command = commands.add_parser("verify")
    verify_command.add_argument("--file", required=True, type=Path)
    verify_command.add_argument("--expected-sha256")
    return root


def main() -> int:
    arguments = parser().parse_args()
    try:
        digest = capture(arguments) if arguments.command == "capture" else verify(arguments)
    except EnvironmentError as error:
        print(str(error), file=sys.stderr)
        return 1
    print(digest)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
