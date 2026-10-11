"""Cross-language error-code parity, read from the committed Go table.

``go/core/errors/testdata/codes.json`` is regenerated from the live Go maps by the Go test
``TestCodesJSON_MatchesClassifyMaps``; this test reads the same committed file (hermetic, no Go
toolchain needed) and compares it with the Python maps code by code.
"""

import json
from pathlib import Path

from techai_webutils.core.errors import AppError, ErrorCode

_CODES_JSON = (
    Path(__file__).resolve().parents[4]
    / "go"
    / "core"
    / "errors"
    / "testdata"
    / "codes.json"
)


def test_go_and_python_error_code_status_parity() -> None:
    """Test that every Go error code maps to the same gRPC/HTTP status and retry class in Python.

    **Why this test is important:**
      - Go and Python services exchange these codes on the wire; a code whose status or retry class
        differs between the languages is retried on one side and failed on the other.
      - RESOURCE_EXHAUSTED is the newest code, and the one whose classification (neither transient
        nor permanent) most easily drifts.

    **What it tests:**
      - the Go table's code set equals ``set(ErrorCode)``
      - for every code, Python's ``{grpc, http, transient, permanent}`` equals the Go row exactly
      - RESOURCE_EXHAUSTED is ``{grpc: 8, http: 429, transient: False, permanent: False}``
    """
    go_table = json.loads(_CODES_JSON.read_text(encoding="utf-8"))

    py_table = {
        code.value: {
            "grpc": AppError(code, "parity").grpc_status,
            "http": AppError(code, "parity").http_status,
            "transient": AppError(code, "parity").is_transient,
            "permanent": AppError(code, "parity").is_permanent,
        }
        for code in ErrorCode
    }

    assert set(go_table) == {c.value for c in ErrorCode}
    assert py_table == go_table
    assert go_table["RESOURCE_EXHAUSTED"] == {
        "grpc": 8,
        "http": 429,
        "transient": False,
        "permanent": False,
    }
