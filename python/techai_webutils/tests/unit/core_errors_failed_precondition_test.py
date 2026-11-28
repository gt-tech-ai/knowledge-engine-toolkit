"""Unit tests for the FAILED_PRECONDITION code and ``FailedPreconditionError``.

A request the system's state cannot honour yet must reach callers as HTTP 400 / gRPC
FAILED_PRECONDITION (not INVALID_ARGUMENT, not a server fault) and must never be retried.
"""

from techai_webutils.core.errors import ErrorCode, FailedPreconditionError

_EXPECTED_HTTP_STATUS = 400
_EXPECTED_GRPC_STATUS = 9


def test_failed_precondition_keeps_its_own_wire_status() -> None:
    """Test that ``FailedPreconditionError`` carries the precondition code and details.

    **Why this test is important:**
      - A caller tells "fix the request" from "the system is not ready for it" by the wire
        status alone; folding it into INVALID_ARGUMENT or a 500 sends the caller the
        wrong way.

    **What it tests:**
      - ``ErrorCode.FAILED_PRECONDITION`` has the wire value ``"FAILED_PRECONDITION"``
      - ``http_status`` is 400 and ``grpc_status`` is 9
      - extra keyword args become the error's details
      - the error is neither transient nor permanent
    """
    err = FailedPreconditionError("revision is stale", expected="r2")

    assert ErrorCode.FAILED_PRECONDITION.value == "FAILED_PRECONDITION"
    assert err.code is ErrorCode.FAILED_PRECONDITION
    assert err.http_status == _EXPECTED_HTTP_STATUS
    assert err.grpc_status == _EXPECTED_GRPC_STATUS
    assert err.details == {"expected": "r2"}
    assert not err.is_transient
    assert not err.is_permanent
