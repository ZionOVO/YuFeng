from __future__ import annotations

import contextlib
import io
import unittest

from yufeng_modelside.cli import parser


class CommandLineTest(unittest.TestCase):
    def test_ingress_capacity_accepts_default_and_bounded_override(self) -> None:
        required = [
            "--modelside-id",
            "modelside-1",
            "--brain",
            "https://brain:9050",
        ]
        self.assertEqual(parser().parse_args(required).ingress_capacity, 0)
        self.assertEqual(parser().parse_args(required + ["--ingress-capacity", "64"]).ingress_capacity, 64)
        for invalid in ("1", "65", "not-a-number"):
            with self.subTest(invalid=invalid):
                with contextlib.redirect_stderr(io.StringIO()):
                    with self.assertRaises(SystemExit):
                        parser().parse_args(required + ["--ingress-capacity", invalid])


if __name__ == "__main__":
    unittest.main()
