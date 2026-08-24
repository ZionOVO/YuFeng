"""Connect-compatible HTTP server over a Unix socket or mutually authenticated TLS."""

from __future__ import annotations

import http.server
import json
import os
import pathlib
import socketserver
import ssl
import stat
import urllib.parse
from typing import Any

from .contracts import ContractError
from .runtime import ModelSideRuntime

INGRESS_PATH = "/yufeng.modelside.v1.ModelSideIngressService/SubmitTraffic"
MAX_REQUEST_BYTES = 10 << 20


class _ThreadingUnixServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


class _ThreadingHTTPServer(http.server.ThreadingHTTPServer):
    daemon_threads = True


class RequestHandler(http.server.BaseHTTPRequestHandler):
    runtime: ModelSideRuntime

    def log_message(self, _format: str, *_args: object) -> None:
        # Request paths are fixed and request bodies contain sensitive traffic.
        return

    def do_GET(self) -> None:
        if self.path not in ("/healthz", "/metrics"):
            self.send_error(404)
            return
        self._json(200, self.runtime.health())

    def do_POST(self) -> None:
        if self.path != INGRESS_PATH:
            self.send_error(404)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self._connect_error(400, "invalid_argument", "content length is invalid")
            return
        if length <= 0 or length > MAX_REQUEST_BYTES:
            self._connect_error(413, "resource_exhausted", "request exceeds modelside limit")
            return
        try:
            payload = json.loads(self.rfile.read(length))
            response = self.runtime.submit(payload)
        except (json.JSONDecodeError, UnicodeDecodeError, ContractError):
            self._connect_error(400, "invalid_argument", "normalized traffic request is invalid")
            return
        self._json(200, response)

    def _connect_error(self, status: int, code: str, message: str) -> None:
        self._json(status, {"code": code, "message": message})

    def _json(self, status: int, payload: dict[str, Any]) -> None:
        raw = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def make_server(
    listen: str,
    runtime: ModelSideRuntime,
    ca_file: str = "",
    cert_file: str = "",
    key_file: str = "",
    dev_insecure: bool = False,
) -> socketserver.BaseServer:
    parsed = urllib.parse.urlparse(listen)
    handler = type("BoundRequestHandler", (RequestHandler,), {"runtime": runtime})
    if parsed.scheme == "unix":
        path = pathlib.Path(parsed.path)
        if not path.is_absolute():
            raise ValueError("modelside Unix socket path must be absolute")
        path.parent.mkdir(mode=0o750, parents=True, exist_ok=True)
        try:
            mode = path.lstat().st_mode
            if not stat.S_ISSOCK(mode):
                raise ValueError("modelside socket path exists and is not a socket")
            path.unlink()
        except FileNotFoundError:
            pass
        server = _ThreadingUnixServer(str(path), handler)
        os.chmod(path, 0o660)
        return server
    if parsed.scheme not in ("https", "http") or not parsed.hostname or parsed.port is None:
        raise ValueError("modelside listen address must use unix, https, or development http")
    if parsed.scheme == "http" and not dev_insecure:
        raise ValueError("production remote modelside requires mutual tls")
    server = _ThreadingHTTPServer((parsed.hostname, parsed.port), handler)
    if parsed.scheme == "https":
        if not all((ca_file, cert_file, key_file)):
            raise ValueError("modelside mutual tls certificate files are required")
        context = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH)
        context.verify_mode = ssl.CERT_REQUIRED
        context.load_verify_locations(cafile=ca_file)
        context.load_cert_chain(certfile=cert_file, keyfile=key_file)
        server.socket = context.wrap_socket(server.socket, server_side=True)
    return server
