from __future__ import annotations

import base64
import pathlib
import sys
import time
import unittest

PACKAGE_ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PACKAGE_ROOT))

from yufeng_modelside.contracts import ContractError, ModelProfile, parse_traffic
from yufeng_modelside.inference import encode_character_classes
from yufeng_modelside.runtime import Metrics, ModelSideRuntime, Result, ResultQueue, ReviewSampler


def profile_payload(**changes):
    value = {
        "profileId": "http/default",
        "modelGroup": "http",
        "modelType": "PVM",
        "modelVersion": "v1",
        "alertThreshold": 0.9,
        "reviewFloor": 0.5,
        "reviewWindowSeconds": 300,
        "maxReviewPerUnit": 4,
        "maxReviewPerRoute": 1,
        "dedupeRule": "MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE",
        "allowedHeaders": ["content-type", "user-agent"],
        "maxBodyBytes": 1024,
        "reviewNewRoutes": True,
        "reviewInsufficientCoverage": True,
    }
    value.update(changes)
    return value


def profile(**changes):
    return ModelProfile.parse(profile_payload(**changes))


def traffic(request_id="request-1", route="/items", occurred="2026-08-24T00:00:01Z"):
    return {
        "schemaVersion": "normalized-http/v1",
        "requestId": request_id,
        "unitId": "unit-1",
        "assetId": "asset-1",
        "generationId": "generation-1",
        "generationSeq": "1",
        "modelProfileId": "http/default",
        "modelProfileDigest": "sha256:profile",
        "method": "POST",
        "route": route,
        "headers": [{"name": "content-type", "values": ["application/json"]}],
        "queryParameters": [{"name": "page", "values": ["1"]}],
        "body": base64.b64encode(b'{"name":"demo"}').decode(),
        "contentType": "application/json",
        "bodyLength": "15",
        "coverage": [{"target": "INSPECTION_SURFACE_BODY", "status": "COVERAGE_STATUS_FULL"}],
        "occurredAt": occurred,
    }


class ContractTests(unittest.TestCase):
    def test_profile_controls_input_and_sensitive_headers_are_rejected(self):
        item = parse_traffic(profile(), "sha256:profile", traffic())
        self.assertIn("application/json", item.model_text)
        with self.assertRaises(ContractError):
            profile(allowedHeaders=["authorization"])

    def test_character_encoder_matches_reference_classes(self):
        self.assertEqual(encode_character_classes("a 1/?\u2603"), [1, 2, 1, 4, 4, 0])


class QueueAndSamplingTests(unittest.TestCase):
    def test_alert_displaces_review_when_result_queue_is_full(self):
        metrics = Metrics()
        results = ResultQueue(1, metrics)
        self.assertTrue(results.offer(Result({"resultId": "review"}, False)))
        self.assertTrue(results.offer(Result({"resultId": "alert"}, True)))
        self.assertEqual(results.take(1, 0)[0].payload["resultId"], "alert")
        self.assertEqual(metrics.snapshot()["review_results_dropped"], 1)

    def test_review_window_keeps_highest_method_route_and_four_routes(self):
        metrics = Metrics()
        results = ResultQueue(16, metrics)
        sampler = ReviewSampler(results, metrics)
        signed = profile()
        for index, score in enumerate((0.51, 0.6, 0.7, 0.8, 0.85)):
            item = parse_traffic(signed, "sha256:profile", traffic(f"request-{index}", f"/route/{index}"))
            sampler.classify(item, score)
        replacement = parse_traffic(signed, "sha256:profile", traffic("request-high", "/route/2"))
        sampler.classify(replacement, 0.89)
        sampler.flush_expired(time.time(), force=True)
        batch = results.take(16, 0)
        self.assertEqual(len(batch), 4)
        route_two = [entry for entry in batch if entry.payload["route"] == "/route/2"]
        self.assertEqual(len(route_two), 1)
        self.assertEqual(route_two[0].payload["score"], 0.89)
        self.assertTrue(all("body" not in entry.payload for entry in batch))

    def test_alert_marks_route_seen_before_a_lower_score_arrives(self):
        metrics = Metrics()
        results = ResultQueue(16, metrics)
        sampler = ReviewSampler(results, metrics)
        signed = profile()
        sampler.classify(parse_traffic(signed, "sha256:profile", traffic("alert")), 0.95)
        sampler.classify(parse_traffic(signed, "sha256:profile", traffic("later")), 0.1)
        sampler.flush_expired(time.time(), force=True)
        batch = results.take(16, 0)
        self.assertEqual(len(batch), 1)
        self.assertTrue(batch[0].alert)


class OfflineBrain:
    def __init__(self):
        self.calls = 0

    def upload(self, _modelside_id, _results):
        self.calls += 1
        raise ConnectionError("offline")


class AlertBackend:
    def score_batch(self, _profile, items):
        return [0.95] * len(items)


class RuntimeIsolationTests(unittest.TestCase):
    def test_ingress_capacity_rejects_a_persistent_business_window(self):
        with self.assertRaises(ValueError):
            ModelSideRuntime(
                "modelside-1",
                AlertBackend(),
                OfflineBrain(),
                ingress_capacity=65,
            )

    def test_ingress_capacity_counts_submitted_batches_not_individual_traffic(self):
        runtime = ModelSideRuntime(
            "modelside-1",
            AlertBackend(),
            OfflineBrain(),
            ingress_capacity=2,
            result_capacity=4,
            shutdown_timeout=0,
        )
        first = runtime.submit(
            {
                "modelProfile": profile_payload(),
                "modelProfileDigest": "sha256:profile",
                "traffic": [traffic("request-1"), traffic("request-2")],
            }
        )
        second = runtime.submit(
            {
                "modelProfile": profile_payload(),
                "modelProfileDigest": "sha256:profile",
                "traffic": [traffic("request-3"), traffic("request-4")],
            }
        )
        full = runtime.submit(
            {
                "modelProfile": profile_payload(),
                "modelProfileDigest": "sha256:profile",
                "traffic": [traffic("request-5"), traffic("request-6")],
            }
        )
        self.assertEqual(first["accepted"], 2)
        self.assertEqual(second["accepted"], 2)
        self.assertEqual(runtime.ingress.qsize(), 2)
        self.assertEqual(full["accepted"], 0)
        self.assertEqual(len(full["dropped"]), 2)
        self.assertTrue(all(item["code"] == "ingress_queue_full" for item in full["dropped"]))
        self.assertEqual(len(runtime.ingress.get_nowait()), 2)
        self.assertEqual(len(runtime.ingress.get_nowait()), 2)

    def test_brain_disconnect_does_not_stop_local_inference(self):
        brain = OfflineBrain()
        runtime = ModelSideRuntime(
            "modelside-1",
            AlertBackend(),
            brain,
            ingress_capacity=4,
            result_capacity=4,
            shutdown_timeout=0.15,
        )
        runtime.start()
        try:
            response = runtime.submit(
                {
                    "modelProfile": profile_payload(),
                    "modelProfileDigest": "sha256:profile",
                    "traffic": [traffic()],
                }
            )
            self.assertEqual(response["accepted"], 1)
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                counters = runtime.metrics.snapshot()
                if counters.get("inference_completed") == 1 and counters.get("result_upload_retries", 0) >= 1:
                    break
                time.sleep(0.01)
            counters = runtime.metrics.snapshot()
            self.assertEqual(counters.get("inference_completed"), 1)
            self.assertGreaterEqual(counters.get("result_upload_retries", 0), 1)
            self.assertGreaterEqual(brain.calls, 1)
            self.assertEqual(runtime.ingress.qsize(), 0)
            self.assertEqual(runtime.results.depth(), 1)
        finally:
            runtime.stop()
        self.assertEqual(runtime.metrics.snapshot().get("shutdown_results_dropped"), 1)


if __name__ == "__main__":
    unittest.main()
