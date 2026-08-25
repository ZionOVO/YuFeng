from __future__ import annotations

import sys
import threading
import time
import types
import unittest
from types import SimpleNamespace
from unittest import mock

from yufeng_modelside.inference import InferenceError, TensorFlowBackend


class Prediction:
    def __init__(self, size: int):
        self._size = size

    def numpy(self) -> list[list[float]]:
        return [[0.95] for _ in range(self._size)]


class ConcurrentModel:
    def __init__(self):
        self.active = 0
        self.maximum_active = 0
        self.lock = threading.Lock()

    def __call__(self, data: list[list[int]], training: bool) -> Prediction:
        self.assert_inference_mode(training)
        with self.lock:
            self.active += 1
            self.maximum_active = max(self.maximum_active, self.active)
        time.sleep(0.05)
        with self.lock:
            self.active -= 1
        return Prediction(len(data))

    @staticmethod
    def assert_inference_mode(training: bool) -> None:
        if training:
            raise AssertionError("inference must not enable training")


def fake_tensorflow_modules() -> dict[str, types.ModuleType]:
    tensorflow = types.ModuleType("tensorflow")
    keras = types.ModuleType("tensorflow.keras")
    preprocessing = types.ModuleType("tensorflow.keras.preprocessing")
    sequence = types.ModuleType("tensorflow.keras.preprocessing.sequence")
    sequence.pad_sequences = lambda rows, maxlen: rows  # type: ignore[attr-defined]
    return {
        "tensorflow": tensorflow,
        "tensorflow.keras": keras,
        "tensorflow.keras.preprocessing": preprocessing,
        "tensorflow.keras.preprocessing.sequence": sequence,
    }


class TensorFlowBackendConcurrencyTest(unittest.TestCase):
    def test_shared_keras_model_calls_are_serialized(self) -> None:
        backend = object.__new__(TensorFlowBackend)
        backend._inference_lock = threading.Lock()
        model = ConcurrentModel()
        backend._model = lambda _profile: model
        barrier = threading.Barrier(3)
        errors: list[Exception] = []

        def score() -> None:
            barrier.wait()
            try:
                backend.score_batch(SimpleNamespace(), [SimpleNamespace(model_text="payload")])
            except Exception as exc:  # 测试线程的异常必须回传主线程。
                errors.append(exc)

        with mock.patch.dict(sys.modules, fake_tensorflow_modules()):
            threads = [threading.Thread(target=score) for _ in range(2)]
            for thread in threads:
                thread.start()
            barrier.wait()
            for thread in threads:
                thread.join(timeout=2)

        self.assertEqual(errors, [])
        self.assertEqual(model.maximum_active, 1)

    def test_tensorflow_call_failure_is_normalized(self) -> None:
        backend = object.__new__(TensorFlowBackend)
        backend._inference_lock = threading.Lock()

        def fail(_data: object, training: bool) -> object:
            del training
            raise RuntimeError("accelerator failure")

        backend._model = lambda _profile: fail
        with mock.patch.dict(sys.modules, fake_tensorflow_modules()):
            with self.assertRaises(InferenceError):
                backend.score_batch(SimpleNamespace(), [SimpleNamespace(model_text="payload")])


if __name__ == "__main__":
    unittest.main()
