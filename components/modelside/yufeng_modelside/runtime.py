"""Bounded ingress, inference, signed sampling and Brain result upload."""

from __future__ import annotations

import collections
import dataclasses
import hashlib
import json
import queue
import ssl
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Sequence
from datetime import datetime, timezone
from typing import Any, Optional

from .contracts import (
    ContractError,
    InferenceItem,
    ModelProfile,
    field,
    insufficient_coverage,
    parse_traffic,
    public_coverage,
)
from .inference import Backend, InferenceError

RESULT_MAX = 1024
UPLOAD_MAX = 100
INFERENCE_BATCH = 32
INGRESS_BATCH_SLOTS_PER_WORKER = 2
SEEN_ROUTE_MAX = 8192


class Metrics:
    def __init__(self) -> None:
        self._values: collections.Counter[str] = collections.Counter()
        self._lock = threading.Lock()

    def add(self, name: str, value: int = 1) -> None:
        with self._lock:
            self._values[name] += value

    def snapshot(self) -> dict[str, int]:
        with self._lock:
            return dict(self._values)


@dataclasses.dataclass
class Result:
    payload: dict[str, Any]
    alert: bool


class ResultQueue:
    """A bounded queue where an alert may displace one unsent review sample."""

    def __init__(self, capacity: int, metrics: Metrics):
        self._capacity = capacity
        self._items: collections.deque[Result] = collections.deque()
        self._condition = threading.Condition()
        self._metrics = metrics

    def offer(self, result: Result) -> bool:
        with self._condition:
            if len(self._items) >= self._capacity:
                if result.alert:
                    for index, current in enumerate(self._items):
                        if not current.alert:
                            del self._items[index]
                            self._metrics.add("review_results_dropped")
                            break
                    else:
                        self._metrics.add("alert_results_dropped")
                        return False
                else:
                    self._metrics.add("review_results_dropped")
                    return False
            self._items.append(result)
            self._condition.notify()
            return True

    def take(self, maximum: int, timeout: float) -> list[Result]:
        with self._condition:
            if not self._items:
                self._condition.wait(timeout)
            batch: list[Result] = []
            while self._items and len(batch) < maximum:
                batch.append(self._items.popleft())
            return batch

    def requeue(self, batch: Sequence[Result]) -> None:
        for result in reversed(batch):
            with self._condition:
                if len(self._items) < self._capacity:
                    self._items.appendleft(result)
                    continue
            self.offer(result)

    def depth(self) -> int:
        with self._condition:
            return len(self._items)


@dataclasses.dataclass
class ReviewCandidate:
    result: Result
    score: float


class ReviewSampler:
    def __init__(self, results: ResultQueue, metrics: Metrics):
        self._results = results
        self._metrics = metrics
        self._windows: dict[tuple[str, str, int], tuple[ModelProfile, dict[tuple[str, str], ReviewCandidate]]] = {}
        self._seen: collections.OrderedDict[tuple[str, str, str, str], None] = collections.OrderedDict()
        self._lock = threading.Lock()

    def classify(self, item: InferenceItem, score: float) -> None:
        traffic = item.traffic
        method = str(field(traffic, "method", default="")).upper()
        route = str(field(traffic, "route", default=""))
        profile = item.profile
        route_key = (str(field(traffic, "unitId", "unit_id", "")), item.profile_digest, method, route)
        with self._lock:
            new_route = route_key not in self._seen
            self._seen[route_key] = None
            self._seen.move_to_end(route_key)
            while len(self._seen) > SEEN_ROUTE_MAX:
                self._seen.popitem(last=False)
        if score >= profile.alert_threshold:
            self._results.offer(Result(_result_payload(item, score, "MODEL_RESULT_KIND_MODEL_ALERT", []), True))
            self._metrics.add("model_alerts")
            return
        reasons: list[str] = []
        if score >= profile.review_floor:
            reasons.append("REVIEW_REASON_SCORE_FLOOR")
        if new_route and profile.review_new_routes:
            reasons.append("REVIEW_REASON_NEW_ROUTE")
        if profile.review_insufficient_coverage and insufficient_coverage(traffic):
            reasons.append("REVIEW_REASON_INSUFFICIENT_COVERAGE")
        if not reasons:
            self._metrics.add("model_results_below_review")
            return
        occurred = datetime.fromisoformat(item.occurred_at.replace("Z", "+00:00"))
        window = int(occurred.timestamp()) // profile.review_window_seconds * profile.review_window_seconds
        unit = str(field(traffic, "unitId", "unit_id", ""))
        key = (unit, item.profile_digest, window)
        candidate = ReviewCandidate(
            Result(_result_payload(item, score, "MODEL_RESULT_KIND_REVIEW_SAMPLE", reasons), False), score
        )
        with self._lock:
            profile_and_routes = self._windows.setdefault(key, (profile, {}))
            routes = profile_and_routes[1]
            route_pair = (method, route)
            previous = routes.get(route_pair)
            if previous is not None and previous.score >= score:
                self._metrics.add("review_candidates_deduped")
                return
            routes[route_pair] = candidate
            if len(routes) > profile.max_review_per_unit:
                lowest = min(routes, key=lambda candidate_key: routes[candidate_key].score)
                del routes[lowest]
                self._metrics.add("review_candidates_deduped")

    def flush_expired(self, now: float, force: bool = False) -> None:
        ready: list[ReviewCandidate] = []
        with self._lock:
            for key, (profile, routes) in list(self._windows.items()):
                if not force and key[2] + profile.review_window_seconds > now:
                    continue
                ready.extend(routes.values())
                del self._windows[key]
        for candidate in sorted(ready, key=lambda value: value.score, reverse=True):
            if self._results.offer(candidate.result):
                self._metrics.add("review_samples")


def _result_payload(item: InferenceItem, score: float, kind: str, reasons: list[str]) -> dict[str, Any]:
    traffic = item.traffic
    stable = "\x00".join(
        (
            str(field(traffic, "requestId", "request_id", "")),
            item.profile_digest,
            item.profile.model_version,
        )
    )
    return {
        "resultId": "mr_" + hashlib.sha256(stable.encode("utf-8")).hexdigest(),
        "requestId": field(traffic, "requestId", "request_id"),
        "unitId": field(traffic, "unitId", "unit_id"),
        "assetId": field(traffic, "assetId", "asset_id"),
        "generationId": field(traffic, "generationId", "generation_id"),
        "generationSeq": field(traffic, "generationSeq", "generation_seq"),
        "kind": kind,
        "score": score,
        "modelProfileId": item.profile.profile_id,
        "modelProfileDigest": item.profile_digest,
        "modelGroup": item.profile.model_group,
        "modelType": item.profile.model_type,
        "modelVersion": item.profile.model_version,
        "method": str(field(traffic, "method", default="")).upper(),
        "route": field(traffic, "route"),
        "coverage": public_coverage(traffic),
        "reviewReasons": reasons,
        "occurredAt": item.occurred_at,
    }


class BrainClient:
    def __init__(
        self,
        endpoint: str,
        token: str,
        ca_file: str = "",
        cert_file: str = "",
        key_file: str = "",
        dev_insecure: bool = False,
    ):
        parsed = urllib.parse.urlparse(endpoint)
        if not token.strip():
            raise ValueError("modelside result token is required")
        if dev_insecure:
            if parsed.scheme not in ("http", "https"):
                raise ValueError("brain endpoint must use http or https")
            context = None
        else:
            if parsed.scheme != "https" or not all((ca_file, cert_file, key_file)):
                raise ValueError("production brain connection requires mutual tls")
            context = ssl.create_default_context(cafile=ca_file)
            context.load_cert_chain(cert_file, key_file)
        self._url = endpoint.rstrip("/") + "/yufeng.modelside.v1.ModelResultService/UploadResults"
        self._token = token.strip()
        self._context = context

    def upload(self, modelside_id: str, results: Sequence[Result]) -> dict[str, Any]:
        body = json.dumps(
            {"modelsideId": modelside_id, "results": [result.payload for result in results]},
            separators=(",", ":"),
        ).encode("utf-8")
        request = urllib.request.Request(
            self._url,
            data=body,
            headers={
                "Authorization": "Bearer " + self._token,
                "Connect-Protocol-Version": "1",
                "Content-Type": "application/json",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, context=self._context, timeout=10) as response:
                raw = response.read(1 << 20)
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            raise ConnectionError("brain result upload is unavailable") from exc
        try:
            decoded = json.loads(raw or b"{}")
        except (json.JSONDecodeError, UnicodeDecodeError) as exc:
            raise ConnectionError("brain result response is invalid") from exc
        if not isinstance(decoded, dict):
            raise ConnectionError("brain result response is invalid")
        return decoded


class ModelSideRuntime:
    def __init__(
        self,
        modelside_id: str,
        backend: Backend,
        brain: BrainClient,
        ingress_capacity: Optional[int] = None,
        result_capacity: int = RESULT_MAX,
        workers: int = 1,
        shutdown_timeout: float = 5.0,
    ):
        if ingress_capacity is None:
            ingress_capacity = max(2, workers * INGRESS_BATCH_SLOTS_PER_WORKER)
        if (
            not modelside_id.strip()
            or ingress_capacity <= 0
            or result_capacity <= 0
            or workers <= 0
            or shutdown_timeout < 0
        ):
            raise ValueError("modelside runtime configuration is invalid")
        self.modelside_id = modelside_id.strip()
        self.metrics = Metrics()
        self.ingress: queue.Queue[list[InferenceItem]] = queue.Queue(maxsize=ingress_capacity)
        self.results = ResultQueue(result_capacity, self.metrics)
        self.sampler = ReviewSampler(self.results, self.metrics)
        self._backend = backend
        self._brain = brain
        self._worker_count = workers
        self._shutdown_timeout = shutdown_timeout
        self._stop = threading.Event()
        self._threads: list[threading.Thread] = []

    def start(self) -> None:
        for index in range(self._worker_count):
            self._threads.append(threading.Thread(target=self._infer_loop, name=f"model-infer-{index}", daemon=True))
        self._threads.append(threading.Thread(target=self._upload_loop, name="model-result-upload", daemon=True))
        for thread in self._threads:
            thread.start()

    def stop(self) -> None:
        deadline = time.monotonic() + self._shutdown_timeout
        while self.ingress.unfinished_tasks and time.monotonic() < deadline:
            time.sleep(0.01)
        self.sampler.flush_expired(time.time(), force=True)
        while self.results.depth() and time.monotonic() < deadline:
            time.sleep(0.01)
        self._stop.set()
        for thread in self._threads:
            thread.join(timeout=max(0.0, deadline - time.monotonic()) + 0.25)
        remaining = self.results.depth()
        if remaining:
            self.metrics.add("shutdown_results_dropped", remaining)

    def submit(self, payload: Any) -> dict[str, Any]:
        if not isinstance(payload, dict):
            raise ContractError("request is invalid")
        profile = ModelProfile.parse(field(payload, "modelProfile", "model_profile"))
        digest = str(field(payload, "modelProfileDigest", "model_profile_digest", "")).strip()
        traffic = field(payload, "traffic", default=[])
        if not digest or not isinstance(traffic, list) or not traffic:
            raise ContractError("model profile digest and traffic are required")
        if len(traffic) > INFERENCE_BATCH:
            self.metrics.add("ingress_dropped", len(traffic))
            return {
                "accepted": 0,
                "dropped": [
                    {
                        "requestId": str(field(raw, "requestId", "request_id", "")) if isinstance(raw, dict) else "",
                        "code": "ingress_batch_too_large",
                    }
                    for raw in traffic
                ],
            }
        items: list[InferenceItem] = []
        dropped: list[dict[str, str]] = []
        for raw in traffic:
            request_id = str(field(raw, "requestId", "request_id", "")) if isinstance(raw, dict) else ""
            try:
                items.append(parse_traffic(profile, digest, raw))
            except ContractError:
                dropped.append({"requestId": request_id, "code": "invalid_traffic"})
                self.metrics.add("ingress_invalid")
        accepted = 0
        if items:
            try:
                self.ingress.put_nowait(items)
                accepted = len(items)
            except queue.Full:
                dropped.extend(
                    {"requestId": str(field(item.traffic, "requestId", "request_id", "")), "code": "ingress_queue_full"}
                    for item in items
                )
                self.metrics.add("ingress_dropped", len(items))
        self.metrics.add("ingress_accepted", accepted)
        return {"accepted": accepted, "dropped": dropped}

    def _infer_loop(self) -> None:
        while not self._stop.is_set():
            try:
                batch = self.ingress.get(timeout=0.2)
            except queue.Empty:
                self.sampler.flush_expired(time.time())
                continue
            try:
                scores = self._backend.score_batch(batch[0].profile, batch)
                if len(scores) != len(batch):
                    raise InferenceError("model result count does not match input")
                for item, score in zip(batch, scores):
                    if not 0 <= score <= 1:
                        raise InferenceError("model returned an invalid score")
                    self.sampler.classify(item, score)
                    self.metrics.add("inference_completed")
            except (InferenceError, ValueError, OSError):
                self.metrics.add("inference_failed", len(batch))
            finally:
                self.ingress.task_done()
            self.sampler.flush_expired(time.time())

    def _upload_loop(self) -> None:
        backoff = 0.25
        while not self._stop.is_set():
            batch = self.results.take(UPLOAD_MAX, 0.2)
            if not batch:
                continue
            try:
                response = self._brain.upload(self.modelside_id, batch)
                rejected = response.get("rejected", [])
                accepted = int(response.get("accepted", 0))
                deduped = int(response.get("deduped", 0))
                if not isinstance(rejected, list) or accepted + deduped + len(rejected) != len(batch):
                    raise ConnectionError("brain result accounting is invalid")
                self.metrics.add("results_uploaded", accepted + deduped)
                self.metrics.add("results_rejected", len(rejected))
                backoff = 0.25
            except (ConnectionError, ValueError, TypeError):
                self.results.requeue(batch)
                self.metrics.add("result_upload_retries")
                self._stop.wait(backoff)
                backoff = min(backoff * 2, 10.0)

    def health(self) -> dict[str, Any]:
        return {
            "status": "ready",
            "modelsideId": self.modelside_id,
            "ingressDepth": self.ingress.qsize(),
            "resultDepth": self.results.depth(),
            "counters": self.metrics.snapshot(),
        }
