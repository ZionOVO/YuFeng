"""Strict JSON views of the ModelSide protocol.

The Go endpoint uses protobuf JSON.  This module deliberately does not invent a
second policy format: every runtime decision is read from the modelProfile sent
by an Edge that already verified the signed generation.
"""

from __future__ import annotations

import base64
import dataclasses
import math
import re
from datetime import datetime, timezone
from typing import Any

SCHEMA_VERSION = "normalized-http/v1"
DEDUPE_METHOD_ROUTE_HIGHEST = "MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE"
FULL_COVERAGE = {1, "COVERAGE_STATUS_FULL"}
ABSENT_COVERAGE = {3, "COVERAGE_STATUS_ABSENT"}
UNSAFE_HEADERS = {
    "authorization",
    "cookie",
    "proxy-authorization",
    "proxy-authenticate",
    "set-cookie",
    "x-api-key",
}
TOKEN = re.compile(r"^[!#$%&'*+\-.^_`|~0-9a-z]+$")


class ContractError(ValueError):
    """A protocol item cannot safely enter inference."""


def field(value: dict[str, Any], camel: str, snake: str | None = None, default: Any = None) -> Any:
    if camel in value:
        return value[camel]
    if snake and snake in value:
        return value[snake]
    return default


def protobuf_int(value: Any, name: str) -> int:
    if isinstance(value, bool):
        raise ContractError(f"{name} is invalid")
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise ContractError(f"{name} is invalid") from exc
    return parsed


def protobuf_float(value: Any, name: str) -> float:
    if isinstance(value, bool):
        raise ContractError(f"{name} is invalid")
    try:
        parsed = float(value)
    except (TypeError, ValueError) as exc:
        raise ContractError(f"{name} is invalid") from exc
    if not math.isfinite(parsed):
        raise ContractError(f"{name} is invalid")
    return parsed


@dataclasses.dataclass(frozen=True)
class ModelProfile:
    profile_id: str
    model_group: str
    model_type: str
    model_version: str
    alert_threshold: float
    review_floor: float
    review_window_seconds: int
    max_review_per_unit: int
    max_review_per_route: int
    allowed_headers: tuple[str, ...]
    max_body_bytes: int
    review_new_routes: bool
    review_insufficient_coverage: bool

    @classmethod
    def parse(cls, raw: Any) -> "ModelProfile":
        if not isinstance(raw, dict):
            raise ContractError("model profile is required")
        profile_id = str(field(raw, "profileId", "profile_id", "")).strip()
        model_group = str(field(raw, "modelGroup", "model_group", "")).strip()
        model_type = str(field(raw, "modelType", "model_type", "")).strip()
        model_version = str(field(raw, "modelVersion", "model_version", "")).strip()
        if not all((profile_id, model_group, model_type, model_version)):
            raise ContractError("model profile coordinates are required")
        alert = protobuf_float(field(raw, "alertThreshold", "alert_threshold"), "alert threshold")
        floor = protobuf_float(field(raw, "reviewFloor", "review_floor"), "review floor")
        if not 0 <= floor < alert <= 1:
            raise ContractError("model profile thresholds are invalid")
        window = protobuf_int(field(raw, "reviewWindowSeconds", "review_window_seconds"), "review window")
        unit_limit = protobuf_int(field(raw, "maxReviewPerUnit", "max_review_per_unit"), "unit review limit")
        route_limit = protobuf_int(field(raw, "maxReviewPerRoute", "max_review_per_route"), "route review limit")
        if window <= 0 or unit_limit <= 0 or route_limit != 1 or route_limit > unit_limit:
            raise ContractError("model profile sampling limits are invalid")
        dedupe = field(raw, "dedupeRule", "dedupe_rule")
        if dedupe not in (1, "1", DEDUPE_METHOD_ROUTE_HIGHEST):
            raise ContractError("model profile dedupe rule is unsupported")
        body_limit = protobuf_int(field(raw, "maxBodyBytes", "max_body_bytes"), "body limit")
        if body_limit <= 0 or body_limit > 64 * 1024:
            raise ContractError("model profile body limit is invalid")
        headers_raw = field(raw, "allowedHeaders", "allowed_headers", [])
        if not isinstance(headers_raw, list):
            raise ContractError("allowed headers are invalid")
        headers: set[str] = set()
        for item in headers_raw:
            name = str(item).strip().lower()
            if not TOKEN.fullmatch(name) or name in UNSAFE_HEADERS:
                raise ContractError("allowed header is invalid")
            headers.add(name)
        return cls(
            profile_id=profile_id,
            model_group=model_group,
            model_type=model_type,
            model_version=model_version,
            alert_threshold=alert,
            review_floor=floor,
            review_window_seconds=window,
            max_review_per_unit=unit_limit,
            max_review_per_route=route_limit,
            allowed_headers=tuple(sorted(headers)),
            max_body_bytes=body_limit,
            review_new_routes=bool(field(raw, "reviewNewRoutes", "review_new_routes", False)),
            review_insufficient_coverage=bool(
                field(raw, "reviewInsufficientCoverage", "review_insufficient_coverage", False)
            ),
        )

    @property
    def key(self) -> tuple[str, str, str, str]:
        return self.profile_id, self.model_group, self.model_type, self.model_version


@dataclasses.dataclass(frozen=True)
class InferenceItem:
    profile: ModelProfile
    profile_digest: str
    traffic: dict[str, Any]
    model_text: str
    occurred_at: str


def _required_string(raw: dict[str, Any], camel: str, snake: str | None = None) -> str:
    value = str(field(raw, camel, snake, "")).strip()
    if not value:
        raise ContractError(f"{camel} is required")
    return value


def _string_values(raw: Any, allowed_names: set[str] | None = None) -> list[tuple[str, tuple[str, ...]]]:
    if not isinstance(raw, list):
        raise ContractError("string values are invalid")
    result: list[tuple[str, tuple[str, ...]]] = []
    seen: set[str] = set()
    for entry in raw:
        if not isinstance(entry, dict):
            raise ContractError("string value entry is invalid")
        name = _required_string(entry, "name").lower()
        if name in seen or (allowed_names is not None and name not in allowed_names):
            raise ContractError("string value name is not allowed")
        values = field(entry, "values", default=[])
        if not isinstance(values, list) or any(not isinstance(value, str) for value in values):
            raise ContractError("string value list is invalid")
        seen.add(name)
        result.append((name, tuple(values)))
    return result


def _parse_timestamp(raw: Any) -> str:
    if not isinstance(raw, str) or not raw.strip():
        raise ContractError("occurredAt is required")
    value = raw.strip()
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ContractError("occurredAt is invalid") from exc
    if parsed.tzinfo is None:
        raise ContractError("occurredAt must include a timezone")
    return parsed.astimezone(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def parse_traffic(profile: ModelProfile, profile_digest: str, raw: Any) -> InferenceItem:
    if not isinstance(raw, dict):
        raise ContractError("traffic item is invalid")
    if field(raw, "schemaVersion", "schema_version") != SCHEMA_VERSION:
        raise ContractError("traffic schema is unsupported")
    digest = _required_string(raw, "modelProfileDigest", "model_profile_digest")
    if digest != profile_digest or _required_string(raw, "modelProfileId", "model_profile_id") != profile.profile_id:
        raise ContractError("traffic model profile does not match")
    for camel, snake in (
        ("requestId", "request_id"),
        ("unitId", "unit_id"),
        ("assetId", "asset_id"),
        ("generationId", "generation_id"),
        ("method", None),
        ("route", None),
    ):
        _required_string(raw, camel, snake)
    if protobuf_int(field(raw, "generationSeq", "generation_seq"), "generation sequence") <= 0:
        raise ContractError("generation sequence is invalid")
    method = _required_string(raw, "method").upper()
    route = _required_string(raw, "route")
    headers = _string_values(field(raw, "headers", default=[]), set(profile.allowed_headers))
    query = _string_values(field(raw, "queryParameters", "query_parameters", []))
    body_raw = field(raw, "body", default="")
    if not isinstance(body_raw, str):
        raise ContractError("body is invalid")
    try:
        body = base64.b64decode(body_raw, validate=True) if body_raw else b""
    except (ValueError, base64.binascii.Error) as exc:
        raise ContractError("body is not canonical base64") from exc
    if len(body) > profile.max_body_bytes:
        raise ContractError("body exceeds signed model limit")
    body_length = protobuf_int(field(raw, "bodyLength", "body_length", 0), "body length")
    if body_length < len(body):
        raise ContractError("body length is inconsistent")
    coverage = field(raw, "coverage", default=[])
    if not isinstance(coverage, list) or any(not isinstance(item, dict) for item in coverage):
        raise ContractError("inspection coverage is invalid")
    occurred_at = _parse_timestamp(field(raw, "occurredAt", "occurred_at"))
    pieces = [method, route]
    for name, values in query:
        pieces.extend((f"query:{name}", *values))
    for name, values in headers:
        pieces.extend((f"header:{name}", *values))
    if body:
        pieces.append(body.decode("utf-8", errors="replace"))
    return InferenceItem(profile, profile_digest, raw, "\n".join(pieces), occurred_at)


def insufficient_coverage(raw: dict[str, Any]) -> bool:
    for item in field(raw, "coverage", default=[]):
        status = field(item, "status")
        if status not in FULL_COVERAGE and status not in ABSENT_COVERAGE:
            return True
    return False


def public_coverage(raw: dict[str, Any]) -> list[dict[str, Any]]:
    return [dict(item) for item in field(raw, "coverage", default=[])]
