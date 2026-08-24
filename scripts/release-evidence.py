#!/usr/bin/env python3
"""扫描并复核发布静态预检与最终证据归档。"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import tarfile
from datetime import datetime, timedelta, timezone
from pathlib import Path, PurePosixPath


PREFLIGHT_MANIFEST_SCHEMA = "yufeng.release-preflight/v1"
PREFLIGHT_REPORT_SCHEMA = "yufeng.release-preflight-report/v1"
MANIFEST_SCHEMA = "yufeng.release-evidence/v2"
REPORT_SCHEMA = "yufeng.release-evidence-report/v2"
ENVIRONMENT_SCHEMA = "yufeng.release-environment/v1"
METADATA_FIELDS = (
    "release-version",
    "evidence-commit",
    "evidence-tree",
    "evidence-sha256",
    "evidence-result",
)
SECRET_NAME = re.compile(r"(?:KEY|TOKEN|SECRET|PASSWORD|PASS|PRIVATE)", re.IGNORECASE)
VERSION_PATTERN = re.compile(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)")
OBJECT_PATTERN = re.compile(r"[0-9a-f]{40,64}")
DIGEST_PATTERN = re.compile(r"[0-9a-f]{64}")
GENERIC_SECRET_PATTERNS = {
    "private-key": re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"),
    "bearer-header": re.compile(rb"(?i)authorization\s*:\s*bearer\s+\S+"),
    "bearer-token": re.compile(rb"(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{20,}"),
    "assigned-secret": re.compile(
        rb'''(?ix)(?:api[_-]?key|access[_-]?token|refresh[_-]?token|secret|password)\s*["']?\s*[:=]\s*["']?[A-Za-z0-9._~+/=-]{8,}'''
    ),
}
BENCHMARK_FAMILIES = (
    b"BenchmarkReleaseSetImmutableExecutionPlan",
    b"BenchmarkEvidenceRingConstantTimePrototype",
    b"BenchmarkReleaseProxySelectiveObservation",
    b"BenchmarkReleaseProxyParallelHotPath",
    b"BenchmarkCorazaReleaseProxyParallel",
    b"BenchmarkCorazaSharedCanonicalRequestPrototype",
    b"BenchmarkCorazaParallelCapacity",
    b"BenchmarkCorazaRegexPrefilter",
    b"BenchmarkCorazaBodyProcessorCost",
)


class EvidenceError(ValueError):
    """EvidenceError 表示证据包不满足公开格式或完整性约束。"""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def json_digest(value: dict[str, object]) -> str:
    canonical = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(canonical).hexdigest()


def secret_values() -> list[bytes]:
    values: list[bytes] = []
    for name, raw in os.environ.items():
        value = raw.strip()
        if SECRET_NAME.search(name) and len(value) >= 8:
            values.append(value.encode())
    return values


def scan_root(root: Path) -> None:
    if not root.is_dir():
        raise EvidenceError("secret scan root is not a directory")
    configured = secret_values()
    failures: list[tuple[str, str]] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.is_symlink():
            continue
        raw = path.read_bytes()
        label = path.relative_to(root).as_posix()
        if any(value in raw for value in configured):
            failures.append((label, "configured-secret"))
        for pattern_name, pattern in GENERIC_SECRET_PATTERNS.items():
            if pattern.search(raw):
                failures.append((label, pattern_name))
    if failures:
        for filename, pattern_name in failures:
            print(f"secret scan rejected {filename}: {pattern_name}", file=sys.stderr)
        raise EvidenceError("release evidence contains secret material")


def load_json(path: Path) -> dict[str, object]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise EvidenceError(f"cannot read JSON {path.name}") from error
    if not isinstance(value, dict):
        raise EvidenceError(f"{path.name} must contain a JSON object")
    return value


def parse_json(raw: bytes, label: str) -> dict[str, object]:
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as error:
        raise EvidenceError(f"{label} is not JSON") from error
    if not isinstance(value, dict):
        raise EvidenceError(f"{label} must contain a JSON object")
    return value


def required_string(value: dict[str, object], field: str) -> str:
    raw = value.get(field)
    if not isinstance(raw, str) or not raw:
        raise EvidenceError(f"manifest field {field} is missing")
    return raw


def require_pattern(value: str, pattern: re.Pattern[str], field: str) -> str:
    if not pattern.fullmatch(value):
        raise EvidenceError(f"{field} is invalid")
    return value


def timestamp(value: object, field: str) -> datetime:
    if not isinstance(value, str) or not value:
        raise EvidenceError(f"{field} is missing")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise EvidenceError(f"{field} is invalid") from error
    if parsed.tzinfo is None:
        raise EvidenceError(f"{field} must include a timezone")
    return parsed.astimezone(timezone.utc)


def metadata_from(value: dict[str, object]) -> dict[str, str]:
    metadata = {field: required_string(value, field) for field in METADATA_FIELDS}
    if metadata["evidence-result"] != "passed":
        raise EvidenceError("evidence-result must be passed")
    require_pattern(metadata["release-version"], VERSION_PATTERN, "release-version")
    require_pattern(metadata["evidence-commit"], OBJECT_PATTERN, "evidence-commit")
    require_pattern(metadata["evidence-tree"], OBJECT_PATTERN, "evidence-tree")
    require_pattern(metadata["evidence-sha256"], DIGEST_PATTERN, "evidence-sha256")
    return metadata


def validate_checksum(path: Path, archive: Path, digest: str) -> None:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as error:
        raise EvidenceError("cannot read checksum") from error
    if lines != [f"{digest}  {archive.name}"]:
        raise EvidenceError("checksum sidecar does not exactly describe the evidence archive")


def read_archive_members(archive: Path, required: set[str]) -> dict[str, bytes]:
    try:
        with tarfile.open(archive, "r:gz") as bundle:
            seen: set[str] = set()
            for member in bundle.getmembers():
                name = PurePosixPath(member.name)
                if name.is_absolute() or ".." in name.parts or not name.parts or name.parts[0] != "yufeng-evidence":
                    raise EvidenceError("evidence archive contains an unsafe path")
                if not member.isfile() and not member.isdir():
                    raise EvidenceError("evidence archive contains an unsupported link or device")
                if member.name in seen:
                    raise EvidenceError("evidence archive contains a duplicate path")
                seen.add(member.name)
            values: dict[str, bytes] = {}
            for path in required:
                try:
                    member = bundle.getmember(path)
                except KeyError as error:
                    raise EvidenceError(f"evidence archive member is missing: {path}") from error
                extracted = bundle.extractfile(member)
                if not member.isfile() or extracted is None:
                    raise EvidenceError(f"evidence archive member is not a regular file: {path}")
                values[path] = extracted.read()
            return values
    except (OSError, tarfile.TarError) as error:
        raise EvidenceError("cannot inspect evidence archive") from error


def validate_environment(raw: bytes, expected: str) -> None:
    environment = parse_json(raw, "environment summary")
    if environment.get("schema") != ENVIRONMENT_SCHEMA:
        raise EvidenceError("environment summary schema is unsupported")
    recorded = environment.pop("fingerprint-sha256", None)
    if recorded != expected or json_digest(environment) != expected:
        raise EvidenceError("environment summary fingerprint does not match")


def validate_commands(
    report: dict[str, object],
    archive: Path,
    report_path: str,
    extra_paths: set[str] | None = None,
) -> tuple[dict[str, dict[str, object]], dict[str, bytes]]:
    commands = report.get("commands")
    if not isinstance(commands, list) or not commands:
        raise EvidenceError("internal report has no command results")
    by_name: dict[str, dict[str, object]] = {}
    required_paths = {report_path}
    if extra_paths:
        required_paths.update(extra_paths)
    for command in commands:
        if not isinstance(command, dict) or command.get("result") != "passed" or command.get("exit-code") != 0:
            raise EvidenceError("an archived command did not pass")
        name = command.get("name")
        log = command.get("log")
        if not isinstance(name, str) or name in by_name or not isinstance(log, str) or not log.startswith("logs/"):
            raise EvidenceError("archived command names or logs are invalid")
        by_name[name] = command
        required_paths.add("yufeng-evidence/" + log)
    archived = read_archive_members(archive, required_paths)
    for command in by_name.values():
        log_path = "yufeng-evidence/" + str(command["log"])
        log_sha = command.get("log-sha256")
        if not isinstance(log_sha, str) or hashlib.sha256(archived[log_path]).hexdigest() != log_sha:
            raise EvidenceError("archived command log digest does not match the report")
    return by_name, archived


def validate_preflight_report(
    report: dict[str, object],
    archive: Path,
    report_path: str,
    version: str,
    base_commit: str,
    source_commit: str,
    tree: str,
    environment_fingerprint: str,
) -> None:
    if report.get("schema") != PREFLIGHT_REPORT_SCHEMA:
        raise EvidenceError("preflight report schema is unsupported")
    expected = {
        "release-version": version,
        "base-commit": base_commit,
        "source-commit": source_commit,
        "preflight-tree": tree,
        "preflight-result": "passed",
        "environment-fingerprint": environment_fingerprint,
    }
    if any(report.get(field) != wanted for field, wanted in expected.items()):
        raise EvidenceError("preflight report metadata does not match")
    if "live-results" in report or "source-backup" in report:
        raise EvidenceError("static preflight report contains live evidence")
    git_state = report.get("git")
    if not isinstance(git_state, dict) or git_state.get("branch") != f"release/{version}" or \
            git_state.get("base-commit") != base_commit or git_state.get("source-commit") != source_commit or \
            git_state.get("tree") != tree or git_state.get("worktree") != "clean":
        raise EvidenceError("preflight report does not prove the clean release tree")
    environment_path = report.get("environment-summary")
    secret_scan = report.get("secret-scan")
    if not isinstance(environment_path, str) or not environment_path.startswith("environment/"):
        raise EvidenceError("environment summary path is invalid")
    if not isinstance(secret_scan, dict) or secret_scan.get("result") != "passed":
        raise EvidenceError("preflight secret scan did not pass")
    secret_record = secret_scan.get("record")
    if not isinstance(secret_record, str) or not secret_record.startswith("environment/"):
        raise EvidenceError("preflight secret scan record is invalid")
    extra = {
        "yufeng-evidence/" + environment_path,
        "yufeng-evidence/" + secret_record,
    }
    by_name, archived = validate_commands(report, archive, report_path, extra)
    if set(by_name) != {"release-static", "hot-path-benchmarks"}:
        raise EvidenceError("preflight commands are incomplete or duplicated")
    static_command = by_name["release-static"].get("command")
    if not isinstance(static_command, str) or "delivery-evidence.sh static" not in static_command:
        raise EvidenceError("preflight static command is invalid")
    benchmark_command = by_name["hot-path-benchmarks"].get("command")
    if not isinstance(benchmark_command, str) or "-benchmem" not in benchmark_command or \
            "-benchtime=250ms" not in benchmark_command or "-count=5" not in benchmark_command:
        raise EvidenceError("hot-path benchmark command does not contain five 250ms benchmem runs")
    static_log = archived["yufeng-evidence/" + str(by_name["release-static"]["log"])]
    if b"delivery static evidence passed" not in static_log:
        raise EvidenceError("static log is missing its completion marker")
    benchmark_log = archived["yufeng-evidence/" + str(by_name["hot-path-benchmarks"]["log"])]
    for benchmark in BENCHMARK_FAMILIES:
        if benchmark not in benchmark_log:
            raise EvidenceError("benchmark log is missing one of the nine hot-path benchmark families")
    validate_environment(archived["yufeng-evidence/" + environment_path], environment_fingerprint)
    if archived["yufeng-evidence/" + secret_record] != b"secret scan passed\n":
        raise EvidenceError("preflight secret scan record is invalid")


def preflight_values(manifest: dict[str, object]) -> dict[str, str]:
    if manifest.get("schema") != PREFLIGHT_MANIFEST_SCHEMA:
        raise EvidenceError("preflight manifest schema is unsupported")
    values = {
        "release-version": require_pattern(required_string(manifest, "release-version"), VERSION_PATTERN, "release-version"),
        "base-commit": require_pattern(required_string(manifest, "base-commit"), OBJECT_PATTERN, "base-commit"),
        "source-commit": require_pattern(required_string(manifest, "source-commit"), OBJECT_PATTERN, "source-commit"),
        "preflight-tree": require_pattern(required_string(manifest, "preflight-tree"), OBJECT_PATTERN, "preflight-tree"),
        "preflight-sha256": require_pattern(required_string(manifest, "preflight-sha256"), DIGEST_PATTERN, "preflight-sha256"),
        "environment-fingerprint": require_pattern(required_string(manifest, "environment-fingerprint"), DIGEST_PATTERN, "environment-fingerprint"),
    }
    if manifest.get("preflight-result") != "passed":
        raise EvidenceError("preflight-result must be passed")
    return values


def verify_preflight(arguments: argparse.Namespace) -> None:
    manifest = load_json(arguments.manifest)
    values = preflight_values(manifest)
    digest = sha256_file(arguments.archive)
    if digest != values["preflight-sha256"]:
        raise EvidenceError("preflight archive digest does not match")
    if manifest.get("archive-asset") != arguments.archive.name or manifest.get("checksum-asset") != arguments.checksum.name:
        raise EvidenceError("preflight asset names do not match supplied files")
    validate_checksum(arguments.checksum, arguments.archive, digest)
    generated_at = timestamp(manifest.get("generated-at"), "generated-at")
    expires_at = timestamp(manifest.get("expires-at"), "expires-at")
    if expires_at <= generated_at or expires_at - generated_at > timedelta(hours=72):
        raise EvidenceError("preflight validity window must not exceed 72 hours")
    now = timestamp(arguments.now, "now") if arguments.now else datetime.now(timezone.utc)
    if now > expires_at:
        raise EvidenceError("release preflight has expired")
    report_path = required_string(manifest, "report-path")
    report_sha = require_pattern(required_string(manifest, "report-sha256"), DIGEST_PATTERN, "report-sha256")
    report_raw = read_archive_members(arguments.archive, {report_path})[report_path]
    if hashlib.sha256(report_raw).hexdigest() != report_sha:
        raise EvidenceError("preflight report digest does not match")
    report = parse_json(report_raw, "preflight report")
    validate_preflight_report(
        report,
        arguments.archive,
        report_path,
        values["release-version"],
        values["base-commit"],
        values["source-commit"],
        values["preflight-tree"],
        values["environment-fingerprint"],
    )
    expected = {
        "release-version": arguments.expected_version,
        "base-commit": arguments.expected_base_commit,
        "source-commit": arguments.expected_source_commit,
        "preflight-tree": arguments.expected_tree,
        "environment-fingerprint": arguments.expected_environment_fingerprint,
    }
    for field, wanted in expected.items():
        if wanted is not None and values[field] != wanted:
            raise EvidenceError(f"{field} does not match the expected preflight metadata")


def validate_live_results(archive: Path, report: dict[str, object], report_path: str) -> None:
    source_backup = report.get("source-backup")
    if not isinstance(source_backup, dict) or source_backup.get("included-in-archive") is not False or \
            not source_backup.get("sha256") or int(source_backup.get("bytes", 0)) <= 0:
        raise EvidenceError("final report does not prove a separate non-empty source backup")
    secret_scan = report.get("secret-scan")
    if not isinstance(secret_scan, dict) or secret_scan.get("result") != "passed":
        raise EvidenceError("final secret scan did not pass")
    secret_record = secret_scan.get("record")
    if not isinstance(secret_record, str) or not secret_record.startswith("environment/"):
        raise EvidenceError("final secret scan record is invalid")
    live_results = report.get("live-results")
    if not isinstance(live_results, dict):
        raise EvidenceError("live result paths are missing")
    required_paths = {"yufeng-evidence/" + secret_record}
    for result_name in ("performance", "backup-restore", "traffic-review"):
        result_path = live_results.get(result_name)
        if not isinstance(result_path, str) or not result_path.startswith("results/"):
            raise EvidenceError("a live result path is invalid")
        required_paths.add("yufeng-evidence/" + result_path)
    by_name, archived = validate_commands(report, archive, report_path, required_paths)
    if set(by_name) != {"live-evidence"} or "delivery-evidence.sh live" not in str(by_name["live-evidence"].get("command")):
        raise EvidenceError("final report must contain exactly one live evidence command")
    if archived["yufeng-evidence/" + secret_record] != b"secret scan passed\n":
        raise EvidenceError("final secret scan record is invalid")
    try:
        performance = json.loads(archived["yufeng-evidence/" + str(live_results["performance"])])
        backup_restore = json.loads(archived["yufeng-evidence/" + str(live_results["backup-restore"])])
        traffic_review = json.loads(archived["yufeng-evidence/" + str(live_results["traffic-review"])])
    except json.JSONDecodeError as error:
        raise EvidenceError("a live result is not JSON") from error
    expected_scenarios = [
        "bypass_disabled",
        "modelside_idle",
        "modelside_saturated",
        "brain_disconnected",
        "brain_disk_slow",
    ]
    workload = performance.get("workload", {})
    model_bypass = performance.get("model_bypass", {})
    scenario_results = model_bypass.get("scenarios", []) if isinstance(model_bypass, dict) else []
    if performance.get("schema_version") != "model-bypass-capacity/v1" or \
            performance.get("budgets", {}).get("edge_throughput_rps") != 2000 or \
            performance.get("budgets", {}).get("model_bypass_p99_micros") != 1000 or \
            workload.get("target_requests_per_second") != 2000 or workload.get("scenarios") != expected_scenarios or \
            performance.get("throughput_budget_met") is not True or performance.get("p99_budget_met") is not True or \
            [item.get("name") for item in scenario_results if isinstance(item, dict)] != expected_scenarios:
        raise EvidenceError("performance result does not prove the five model bypass capacity scenarios")
    for item in scenario_results:
        if not isinstance(item, dict) or item.get("throughput_requests_per_second", 0) < 2000 or \
                item.get("p99_increase_micros", 1001) > 1000:
            raise EvidenceError("a model bypass scenario exceeds throughput or p99 budget")
    if scenario_results[2].get("ingress_dropped", 0) <= 0 or \
            scenario_results[3].get("result_depth", 0) <= 0 or scenario_results[3].get("result_upload_retries", 0) <= 0 or \
            scenario_results[4].get("result_depth", 0) <= 0 or scenario_results[4].get("result_upload_retries", 0) <= 0:
        raise EvidenceError("model bypass queue saturation and retry evidence is incomplete")
    if backup_restore.get("backup_restore_deadline_met") is not True or backup_restore.get("committed_row_loss") != 0 or \
            backup_restore.get("source_database_preserved") is not True:
        raise EvidenceError("backup restore result does not prove deadline and zero committed-row loss")
    expected_cleanup = {
        "release": "RELEASE_STATE_RETIRED",
        "policy": "TRAFFIC_REVIEW_MODE_OFF",
        "profile": "AGENT_PROFILE_STATE_DISABLED",
        "case": "INVESTIGATION_CASE_STATE_RESOLVED",
    }
    if traffic_review.get("result") != "passed" or traffic_review.get("worker_id") != "agentd-central" or \
            traffic_review.get("verified_release_state") != "RELEASE_STATE_SHADOW" or \
            not traffic_review.get("assigned_run_id") or not traffic_review.get("finding_disposition") or \
            traffic_review.get("cleanup") != expected_cleanup:
        raise EvidenceError("traffic review result does not prove the real-model Shadow-only closure")
    live_log = archived["yufeng-evidence/" + str(by_name["live-evidence"]["log"])]
    for marker in (
        b"traffic review live ok",
        b'"throughput_budget_met": true',
        b'"committed_row_loss": 0',
        b'"source_database_preserved": true',
        b"delivery live evidence passed",
    ):
        if marker not in live_log:
            raise EvidenceError("live log is missing a required result")


def verify(arguments: argparse.Namespace) -> None:
    manifest = load_json(arguments.manifest)
    if manifest.get("schema") != MANIFEST_SCHEMA:
        raise EvidenceError("evidence manifest schema is unsupported")
    metadata = metadata_from(manifest)
    digest = sha256_file(arguments.archive)
    if metadata["evidence-sha256"] != digest:
        raise EvidenceError("archive digest does not match the manifest")
    if manifest.get("archive-asset") != arguments.archive.name or manifest.get("checksum-asset") != arguments.checksum.name:
        raise EvidenceError("manifest asset names do not match supplied files")
    validate_checksum(arguments.checksum, arguments.archive, digest)
    timestamp(manifest.get("generated-at"), "generated-at")
    required_string(manifest, "ci-url")
    parents = manifest.get("merge-parents")
    if not isinstance(parents, list) or len(parents) != 2 or any(not isinstance(item, str) or not OBJECT_PATTERN.fullmatch(item) for item in parents):
        raise EvidenceError("merge-parents must contain exactly two Git object ids")
    preflight = manifest.get("preflight")
    if not isinstance(preflight, dict):
        raise EvidenceError("preflight binding is missing")
    preflight_expected = {
        "base-commit": parents[0],
        "source-commit": parents[1],
        "tree": metadata["evidence-tree"],
    }
    if any(preflight.get(field) != wanted for field, wanted in preflight_expected.items()):
        raise EvidenceError("preflight binding does not match merge parents or tree")
    environment_fingerprint = require_pattern(required_string(preflight, "environment-fingerprint"), DIGEST_PATTERN, "environment-fingerprint")
    preflight_manifest_sha = require_pattern(required_string(preflight, "manifest-sha256"), DIGEST_PATTERN, "manifest-sha256")
    preflight_report_sha = require_pattern(required_string(preflight, "report-sha256"), DIGEST_PATTERN, "preflight report-sha256")
    preflight_archive_sha = require_pattern(required_string(preflight, "archive-sha256"), DIGEST_PATTERN, "preflight archive-sha256")
    preflight_generated = timestamp(preflight.get("generated-at"), "preflight generated-at")
    preflight_expires = timestamp(preflight.get("expires-at"), "preflight expires-at")
    promoted_at = timestamp(manifest.get("generated-at"), "generated-at")
    if preflight_expires <= preflight_generated or promoted_at > preflight_expires:
        raise EvidenceError("evidence was promoted outside the preflight validity window")
    report_path = required_string(manifest, "report-path")
    report_sha = require_pattern(required_string(manifest, "report-sha256"), DIGEST_PATTERN, "report-sha256")
    report_raw = read_archive_members(arguments.archive, {report_path})[report_path]
    if hashlib.sha256(report_raw).hexdigest() != report_sha:
        raise EvidenceError("internal report digest does not match the manifest")
    report = parse_json(report_raw, "internal evidence report")
    if report.get("schema") != REPORT_SCHEMA:
        raise EvidenceError("internal evidence report schema is unsupported")
    for field in ("release-version", "evidence-commit", "evidence-tree", "evidence-result"):
        if report.get(field) != metadata[field]:
            raise EvidenceError("internal report metadata does not match the external manifest")
    if report.get("ci-url") != manifest.get("ci-url") or report.get("merge-parents") != parents:
        raise EvidenceError("internal report integration binding does not match")
    git_state = report.get("git")
    if not isinstance(git_state, dict) or git_state.get("branch") != "develop" or git_state.get("commit") != metadata["evidence-commit"] or \
            git_state.get("tree") != metadata["evidence-tree"] or git_state.get("worktree") != "clean":
        raise EvidenceError("internal report does not prove a clean develop worktree")
    continuous_integration = report.get("continuous-integration")
    if not isinstance(continuous_integration, dict) or continuous_integration.get("conclusion") != "success" or \
            continuous_integration.get("status") != "completed" or \
            continuous_integration.get("head-sha") != metadata["evidence-commit"] or \
            continuous_integration.get("head-branch") != "develop" or \
            continuous_integration.get("event") != "push" or \
            continuous_integration.get("url") != manifest.get("ci-url") or \
            not str(continuous_integration.get("workflow-path", "")).startswith(".github/workflows/ci.yml"):
        raise EvidenceError("internal report does not prove successful exact-commit integration")
    static_binding = report.get("static-preflight")
    if not isinstance(static_binding, dict):
        raise EvidenceError("internal report has no static preflight binding")
    preflight_report_path = static_binding.get("report")
    preflight_manifest_path = static_binding.get("manifest")
    if not isinstance(preflight_report_path, str) or not preflight_report_path.startswith("provenance/") or \
            not isinstance(preflight_manifest_path, str) or not preflight_manifest_path.startswith("provenance/"):
        raise EvidenceError("static preflight provenance paths are invalid")
    if static_binding.get("manifest-sha256") != preflight_manifest_sha or \
            static_binding.get("report-sha256") != preflight_report_sha or \
            static_binding.get("archive-sha256") != preflight_archive_sha or \
            static_binding.get("environment-fingerprint") != environment_fingerprint:
        raise EvidenceError("internal static preflight binding differs from the manifest")
    promotion_environment_path = report.get("promotion-environment-summary")
    if not isinstance(promotion_environment_path, str) or not promotion_environment_path.startswith("environment/"):
        raise EvidenceError("promotion environment summary path is invalid")
    provenance_paths = {
        "yufeng-evidence/" + preflight_report_path,
        "yufeng-evidence/" + preflight_manifest_path,
        "yufeng-evidence/" + promotion_environment_path,
    }
    provenance = read_archive_members(arguments.archive, provenance_paths)
    preflight_report_raw = provenance["yufeng-evidence/" + preflight_report_path]
    preflight_manifest_raw = provenance["yufeng-evidence/" + preflight_manifest_path]
    if hashlib.sha256(preflight_report_raw).hexdigest() != preflight_report_sha or \
            hashlib.sha256(preflight_manifest_raw).hexdigest() != preflight_manifest_sha:
        raise EvidenceError("static preflight provenance digest does not match")
    preflight_manifest = parse_json(preflight_manifest_raw, "archived preflight manifest")
    archived_preflight_values = preflight_values(preflight_manifest)
    if archived_preflight_values["release-version"] != metadata["release-version"] or \
            archived_preflight_values["base-commit"] != parents[0] or \
            archived_preflight_values["source-commit"] != parents[1] or \
            archived_preflight_values["preflight-tree"] != metadata["evidence-tree"] or \
            archived_preflight_values["preflight-sha256"] != preflight_archive_sha or \
            archived_preflight_values["environment-fingerprint"] != environment_fingerprint or \
            preflight_manifest.get("report-sha256") != preflight_report_sha or \
            preflight_manifest.get("generated-at") != preflight.get("generated-at") or \
            preflight_manifest.get("expires-at") != preflight.get("expires-at"):
        raise EvidenceError("archived preflight manifest differs from the final binding")
    validate_environment(provenance["yufeng-evidence/" + promotion_environment_path], environment_fingerprint)
    preflight_report = parse_json(preflight_report_raw, "archived preflight report")
    validate_preflight_report(
        preflight_report,
        arguments.archive,
        "yufeng-evidence/" + preflight_report_path,
        metadata["release-version"],
        parents[0],
        parents[1],
        metadata["evidence-tree"],
        environment_fingerprint,
    )
    validate_live_results(arguments.archive, report, report_path)
    expected = {
        "release-version": arguments.expected_version,
        "evidence-commit": arguments.expected_commit,
        "evidence-tree": arguments.expected_tree,
        "evidence-sha256": arguments.expected_sha256,
        "base-commit": arguments.expected_base_commit,
        "source-commit": arguments.expected_source_commit,
    }
    actual = metadata | {"base-commit": str(parents[0]), "source-commit": str(parents[1])}
    for field, wanted in expected.items():
        if wanted is not None and actual[field] != wanted:
            raise EvidenceError(f"{field} does not match the expected release metadata")


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser()
    commands = root.add_subparsers(dest="command", required=True)
    scan = commands.add_parser("scan")
    scan.add_argument("--root", required=True, type=Path)
    preflight = commands.add_parser("verify-preflight")
    preflight.add_argument("--manifest", required=True, type=Path)
    preflight.add_argument("--archive", required=True, type=Path)
    preflight.add_argument("--checksum", required=True, type=Path)
    preflight.add_argument("--expected-version")
    preflight.add_argument("--expected-base-commit")
    preflight.add_argument("--expected-source-commit")
    preflight.add_argument("--expected-tree")
    preflight.add_argument("--expected-environment-fingerprint")
    preflight.add_argument("--now")
    check = commands.add_parser("verify")
    check.add_argument("--manifest", required=True, type=Path)
    check.add_argument("--archive", required=True, type=Path)
    check.add_argument("--checksum", required=True, type=Path)
    check.add_argument("--expected-version")
    check.add_argument("--expected-commit")
    check.add_argument("--expected-tree")
    check.add_argument("--expected-sha256")
    check.add_argument("--expected-base-commit")
    check.add_argument("--expected-source-commit")
    return root


def main() -> int:
    arguments = parser().parse_args()
    try:
        if arguments.command == "scan":
            scan_root(arguments.root)
        elif arguments.command == "verify-preflight":
            verify_preflight(arguments)
        else:
            verify(arguments)
    except (EvidenceError, OSError) as error:
        print(str(error), file=sys.stderr)
        return 1
    if arguments.command == "scan":
        print("secret scan passed")
    elif arguments.command == "verify-preflight":
        print("release preflight verified")
    else:
        print("release evidence verified")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
