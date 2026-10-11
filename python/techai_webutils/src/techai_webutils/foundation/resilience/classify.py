"""Transient vs permanent error classification for retry/DLQ routing.

Centralises the "should this be retried or dead-lettered?" decision across the error
families the ingestion path sees: ``AppError`` (delegates to its ``is_transient`` flag),
botocore ``ClientError`` (throttling / 5xx) + connection errors, gRPC ``UNAVAILABLE`` /
``DEADLINE_EXCEEDED``, ``httpx`` transport/5xx/429 (the local-vector path's Ollama embedder),
and the ``qdrant_client`` transport/5xx/429 family (the local-vector store). Anything
unrecognised defaults to **permanent** so unknown failures fail fast to the DLQ instead of
looping.
"""

from __future__ import annotations

import grpc
import httpx
from botocore.exceptions import (
    BotoCoreError,
    ClientError,
    ConnectionClosedError,
    ConnectTimeoutError,
    EndpointConnectionError,
    ReadTimeoutError,
)

from techai_webutils.core.errors.errors import AppError

# AWS error codes that signal throttling / rate limiting (retriable regardless of HTTP status).
_THROTTLE_CODES = frozenset(
    {
        "Throttling",
        "ThrottlingException",
        "ThrottledException",
        "RequestThrottled",
        "RequestThrottledException",
        "TooManyRequestsException",
        "RequestLimitExceeded",
        "ProvisionedThroughputExceededException",
        "TransactionInProgressException",
        "SlowDown",
        "EC2ThrottledException",
    },
)
"""AWS error codes signalling throttling/rate-limiting (retriable regardless of HTTP status)."""

# botocore connection-level failures that are always retriable.
_TRANSIENT_BOTOCORE = (
    EndpointConnectionError,
    ConnectTimeoutError,
    ReadTimeoutError,
    ConnectionClosedError,
)
"""botocore connection-level exception types that are always retriable."""

# gRPC status codes treated as transient (peer briefly down / deadline hit).
_TRANSIENT_GRPC = frozenset({
    grpc.StatusCode.UNAVAILABLE,
    grpc.StatusCode.DEADLINE_EXCEEDED,
})
"""gRPC status codes treated as transient (peer briefly down / deadline hit)."""

# HTTP 5xx codes that are genuinely retriable (transient server/gateway faults). 501
# (Not Implemented) and 505 are permanent, so we match specific codes rather than a
# blanket ``>= 500``.
_TRANSIENT_HTTP_STATUS = frozenset({500, 502, 503, 504})
"""HTTP 5xx codes that are genuinely retriable (transient server/gateway faults)."""

# For the raw-HTTP families (httpx/qdrant) also treat 429 (Too Many Requests) as retriable —
# it is a rate-limit backpressure signal, not a permanent client error.
_TRANSIENT_HTTP_RETRY_STATUS = _TRANSIENT_HTTP_STATUS | {429}
"""Retriable HTTP statuses for raw-HTTP families (httpx/qdrant): the 5xx set plus 429."""


def _botocore_transient(err: BaseException) -> bool:
    """Return True for retriable botocore errors (connection faults, throttling, 5xx)."""
    if isinstance(err, _TRANSIENT_BOTOCORE):
        return True
    if isinstance(err, ClientError):
        response = err.response or {}
        code = response.get("Error", {}).get("Code", "")
        status = response.get("ResponseMetadata", {}).get("HTTPStatusCode", 0)
        return code in _THROTTLE_CODES or (
            isinstance(status, int) and status in _TRANSIENT_HTTP_STATUS
        )
    # A bare BotoCoreError with no more specific type is treated as transient (I/O layer).
    return isinstance(err, BotoCoreError)


def _grpc_transient(err: BaseException) -> bool:
    """Return True for retriable gRPC errors (UNAVAILABLE / DEADLINE_EXCEEDED)."""
    if not isinstance(err, grpc.RpcError):
        return False
    code_fn = getattr(err, "code", None)
    if not callable(code_fn):
        return False
    return code_fn() in _TRANSIENT_GRPC


def _httpx_transient(err: BaseException) -> bool:
    """Return True for retriable httpx errors (transport faults + 5xx/429 responses).

    The local-vector Ollama embedder calls ``httpx.AsyncClient.post`` + ``raise_for_status()``,
    so a cold/unavailable Ollama surfaces as ``TransportError`` (connect/read/write/pool) or a
    ``HTTPStatusError`` carrying a 5xx/429 — all of which must redrive, not dead-letter.
    """
    if isinstance(err, httpx.TransportError):
        return True
    if isinstance(err, httpx.HTTPStatusError):
        return err.response.status_code in _TRANSIENT_HTTP_RETRY_STATUS
    return False


def _qdrant_transient(err: BaseException) -> bool:
    """Return True for retriable qdrant-client errors (transport faults + 5xx/429 responses).

    Matched structurally by module + class name (rather than importing ``qdrant_client`` into
    this foundation module): ``ResponseHandlingException`` wraps a transport-level failure
    reaching Qdrant (connect/timeout); ``UnexpectedResponse`` carries a ``status_code`` we treat
    as retriable on 5xx/429.
    """
    if not type(err).__module__.startswith("qdrant_client"):
        return False
    name = type(err).__name__
    if name == "ResponseHandlingException":
        return True
    if name == "UnexpectedResponse":
        status = getattr(err, "status_code", None)
        return isinstance(status, int) and status in _TRANSIENT_HTTP_RETRY_STATUS
    return False


def is_transient(err: BaseException) -> bool:
    """Return True if *err* is a transient failure that should be retried.

    ``AppError`` delegates to its own ``is_transient`` flag; botocore, gRPC, httpx, and
    qdrant-client infrastructure errors are classified structurally; everything else is
    non-transient.
    """
    if isinstance(err, AppError):
        return err.is_transient
    return (
        _botocore_transient(err)
        or _grpc_transient(err)
        or _httpx_transient(err)
        or _qdrant_transient(err)
    )


def is_permanent(err: BaseException) -> bool:
    """Return True if *err* is permanent (poison) and should be dead-lettered without retry."""
    return not is_transient(err)
