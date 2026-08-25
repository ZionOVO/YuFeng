"""Command-line entry point for the independently managed ModelSide process."""

from __future__ import annotations

import argparse
import pathlib
import signal
import threading

from .inference import DeterministicBackend, TensorFlowBackend
from .runtime import BrainClient, INGRESS_BATCH_SLOTS_MAX, ModelSideRuntime


def _secret(value: str, path: str, name: str) -> str:
    if value and path:
        raise SystemExit(f"{name} and {name}-file are mutually exclusive")
    if path:
        try:
            value = pathlib.Path(path).read_text(encoding="utf-8").strip()
        except OSError as exc:
            raise SystemExit(f"cannot read {name}-file") from exc
    if not value.strip():
        raise SystemExit(f"{name} is required")
    return value.strip()


def _ingress_capacity(value: str) -> int:
    try:
        parsed = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("ingress capacity must be an integer") from exc
    if parsed != 0 and not 2 <= parsed <= INGRESS_BATCH_SLOTS_MAX:
        raise argparse.ArgumentTypeError(
            f"ingress capacity must be 0 or between 2 and {INGRESS_BATCH_SLOTS_MAX}"
        )
    return parsed


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(prog="yufeng-modelside")
    result.add_argument("--listen", default="unix:///run/yufeng/modelside.sock")
    result.add_argument("--modelside-id", required=True)
    result.add_argument("--brain", required=True)
    result.add_argument("--brain-token", default="")
    result.add_argument("--brain-token-file", default="")
    result.add_argument("--brain-ca", default="")
    result.add_argument("--brain-cert", default="")
    result.add_argument("--brain-key", default="")
    result.add_argument("--listen-ca", default="")
    result.add_argument("--listen-cert", default="")
    result.add_argument("--listen-key", default="")
    result.add_argument("--weights", default="")
    result.add_argument("--workers", type=int, default=1)
    result.add_argument("--ingress-capacity", type=_ingress_capacity, default=0)
    result.add_argument("--dev-insecure", action="store_true")
    result.add_argument("--dev-deterministic", action="store_true")
    return result


def main() -> None:
    args = parser().parse_args()
    from .server import make_server

    if args.dev_deterministic and not args.dev_insecure:
        raise SystemExit("deterministic inference is restricted to development mode")
    token = _secret(args.brain_token, args.brain_token_file, "brain-token")
    backend = DeterministicBackend() if args.dev_deterministic else TensorFlowBackend(args.weights)
    brain = BrainClient(
        args.brain,
        token,
        args.brain_ca,
        args.brain_cert,
        args.brain_key,
        args.dev_insecure,
    )
    runtime = ModelSideRuntime(
        args.modelside_id,
        backend,
        brain,
        ingress_capacity=args.ingress_capacity or None,
        workers=args.workers,
    )
    server = make_server(
        args.listen,
        runtime,
        args.listen_ca,
        args.listen_cert,
        args.listen_key,
        args.dev_insecure,
    )
    stopped = threading.Event()

    def stop(_number: int, _frame: object) -> None:
        if not stopped.is_set():
            stopped.set()
            threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    runtime.start()
    try:
        server.serve_forever(poll_interval=0.2)
    finally:
        server.server_close()
        runtime.stop()
