"""Character-class TensorFlow inference adapted from sentry-docker ModelSide."""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import string
import threading
from collections.abc import Sequence
from typing import Protocol

from .contracts import InferenceItem, ModelProfile

SEQUENCE_LENGTH = 115
SPECIAL = set("!\"#$%&'()*+,-./:;<=>?@[\\]^`{|}~")
WHITESPACE = {"\x00", "\x09", "\x0a", "\x0b", "\x0c", "\x0d", "\x20"}


class InferenceError(RuntimeError):
    """The signed model coordinates cannot currently be executed."""


class Backend(Protocol):
    def score_batch(self, profile: ModelProfile, items: Sequence[InferenceItem]) -> list[float]: ...


def encode_character_classes(value: str) -> list[int]:
    """Encode text with the five classes used by the reference weights."""
    encoded: list[int] = []
    for char in value:
        if char not in string.printable:
            encoded.append(0)
        elif char in WHITESPACE:
            encoded.append(2)
        elif "\x01" < char < "\x08" or "\x0e" < char < "\x1f" or char == "\x7f":
            encoded.append(3)
        elif char in SPECIAL:
            encoded.append(4)
        elif char.isdigit() or char.isalpha() or char == "_":
            encoded.append(1)
        else:
            encoded.append(0)
    return encoded


class DeterministicBackend:
    """Dependency-free backend for explicit development and contract tests."""

    def score_batch(self, profile: ModelProfile, items: Sequence[InferenceItem]) -> list[float]:
        del profile
        scores: list[float] = []
        for item in items:
            digest = hashlib.sha256(item.model_text.encode("utf-8", errors="replace")).digest()
            scores.append(int.from_bytes(digest[:8], "big") / float((1 << 64) - 1))
        return scores


class TensorFlowBackend:
    """Load immutable weight versions and execute the reference bidirectional LSTM."""

    def __init__(self, weights_root: str):
        self._root = pathlib.Path(weights_root).resolve()
        self._models: dict[tuple[str, str, str], object] = {}
        self._lock = threading.Lock()
        self._inference_lock = threading.Lock()
        try:
            raw = (self._root / "manifest.json").read_text(encoding="utf-8")
            manifest = json.loads(raw)
        except (OSError, json.JSONDecodeError) as exc:
            raise InferenceError("model weight manifest is unavailable") from exc
        entries = manifest.get("models") if isinstance(manifest, dict) else None
        if not isinstance(entries, list):
            raise InferenceError("model weight manifest is invalid")
        self._entries: dict[tuple[str, str, str], tuple[pathlib.Path, str]] = {}
        for entry in entries:
            if not isinstance(entry, dict):
                raise InferenceError("model weight entry is invalid")
            key = tuple(str(entry.get(name, "")).strip() for name in ("group", "type", "version"))
            relative = pathlib.Path(str(entry.get("weights", "")))
            expected = str(entry.get("sha256", "")).lower()
            if not all(key) or relative.is_absolute() or not expected.startswith("sha256:"):
                raise InferenceError("model weight coordinates are invalid")
            path = (self._root / relative).resolve()
            if self._root not in path.parents or len(expected) != 71:
                raise InferenceError("model weight path or digest is invalid")
            self._entries[key] = (path, expected)

    @staticmethod
    def _new_model() -> object:
        try:
            from tensorflow.keras.layers import LSTM, Bidirectional, Dense, Embedding
            from tensorflow.keras.models import Sequential
        except ImportError as exc:
            raise InferenceError("tensorflow runtime is not installed") from exc
        model = Sequential()
        model.add(Embedding(input_dim=5, output_dim=64))
        model.add(Bidirectional(LSTM(units=64, return_sequences=True)))
        model.add(Bidirectional(LSTM(units=64)))
        model.add(Dense(units=20, activation="tanh"))
        model.add(Dense(units=1, activation="sigmoid"))
        return model

    def _model(self, profile: ModelProfile) -> object:
        key = (profile.model_group, profile.model_type, profile.model_version)
        with self._lock:
            if key in self._models:
                return self._models[key]
            entry = self._entries.get(key)
            if entry is None:
                raise InferenceError("signed model version is not installed")
            path, expected = entry
            try:
                digest = "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()
            except OSError as exc:
                raise InferenceError("model weights are unavailable") from exc
            if not os.path.isfile(path) or digest != expected:
                raise InferenceError("model weight digest does not match manifest")
            model = self._new_model()
            model.load_weights(str(path))
            self._models[key] = model
            return model

    def score_batch(self, profile: ModelProfile, items: Sequence[InferenceItem]) -> list[float]:
        if not items:
            return []
        try:
            from tensorflow.keras.preprocessing.sequence import pad_sequences
        except ImportError as exc:
            raise InferenceError("tensorflow runtime is not installed") from exc
        data = pad_sequences(
            [encode_character_classes(item.model_text) for item in items],
            maxlen=SEQUENCE_LENGTH,
        )
        try:
            # Keras 的共享图形处理器模型调用不是线程安全的；批次并发由入口槽吸收，模型调用串行化。
            with self._inference_lock:
                predictions = self._model(profile)(data, training=False).numpy()
        except Exception as exc:
            raise InferenceError("tensorflow inference failed") from exc
        scores = [float(row[0]) for row in predictions]
        if any(score < 0 or score > 1 for score in scores):
            raise InferenceError("model returned an invalid score")
        return scores
