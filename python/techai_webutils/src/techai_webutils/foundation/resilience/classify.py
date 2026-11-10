"""Transient vs permanent error classification for retry/DLQ routing.

Centralises the "should this be retried or dead-lettered?" decision across the error
families the ingestion path sees: ``AppError`` (delegates to its ``is_transient`` flag),
botocore ``ClientError`` (throttling / 5xx) + connection errors, gRPC ``UNAVAILABLE`` /
``DEADLINE_EXCEEDED``, ``httpx`` transport/5xx/429 (the local-vector path's Ollama
embedder), and the ``qdrant_client`` transport/5xx/429 family (the local-vector store).
Anything unrecognised defaults to **permanent** so unknown failures fail fast to the DLQ
instead of looping.
"""

from __future__ import annotations

from typing import cast

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

from techai_webutils.core.errors.errors import AppError, ThrottledError

# AWS error codes that signal throttling / rate limiting (retriable regardless of HTTP
# status).
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
"""AWS error codes signalling throttling/rate-limiting (retriable regardless of HTTP
status)."""

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

# For the raw-HTTP families (httpx/qdrant) also treat 429 (Too Many Requests) as retriable
# — it is a rate-limit backpressure signal, not a permanent client error.
_QUOTA_CODES = frozenset({"ServiceQuotaExceededException"})
"""AWS error codes of a quota refusal (Bedrock's concurrent ingestion jobs, for one). A
throttle for ``is_throttled``, but not retried by itself: a resource-count quota does not
clear on retry."""

_TOO_MANY_REQUESTS = 429
"""The HTTP status of a rate-limit refusal."""

_TRANSIENT_HTTP_RETRY_STATUS = _TRANSIENT_HTTP_STATUS | {_TOO_MANY_REQUESTS}
"""Retriable HTTP statuses for raw-HTTP families (httpx/qdrant): the 5xx set plus 429."""


def _botocore_transient(err: BaseException) -> bool:
    """Return True for retriable botocore errors (connection faults, throttling, 5xx).

    Args:
        err: The error.

    Returns:
        True for retriable botocore errors (connection faults, throttling, 5xx).

    """
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
    """Return True for retriable gRPC errors (UNAVAILABLE / DEADLINE_EXCEEDED).

    Args:
        err: The error.

    Returns:
        True for retriable gRPC errors (UNAVAILABLE / DEADLINE_EXCEEDED).

    """
    if not isinstance(err, grpc.RpcError):
        return False
    code_fn = getattr(err, "code", None)
    if not callable(code_fn):
        return False
    return code_fn() in _TRANSIENT_GRPC


def _httpx_transient(err: BaseException) -> bool:
    """Return True for retriable httpx errors (transport faults + 5xx/429 responses).

    The local-vector Ollama embedder calls ``httpx.AsyncClient.post`` +
    ``raise_for_status()``, so a cold/unavailable Ollama surfaces as ``TransportError``
    (connect/read/write/pool) or a ``HTTPStatusError`` carrying a 5xx/429 — all of which
    must redrive, not dead-letter.

    Args:
        err: The error.

    Returns:
        True for retriable httpx errors (transport faults + 5xx/429 responses).

    """
    if isinstance(err, httpx.TransportError):
        return True
    if isinstance(err, httpx.HTTPStatusError):
        return err.response.status_code in _TRANSIENT_HTTP_RETRY_STATUS
    return False


def _qdrant_transient(err: BaseException) -> bool:
    """Return True for retriable qdrant-client errors.

    (transport faults + 5xx/429 responses).

    Matched structurally by module + class name (rather than importing ``qdrant_client``
    into this foundation module): ``ResponseHandlingException`` wraps a transport-level
    failure reaching Qdrant (connect/timeout); ``UnexpectedResponse`` carries a
    ``status_code`` we treat as retriable on 5xx/429.

    Args:
        err: The error.

    Returns:
        True for retriable qdrant-client errors.

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

    Args:
        err: The error.

    Returns:
        True if *err* is a transient failure that should be retried.

    """
    if isinstance(err, AppError):
        return err.is_transient
    return (
        _botocore_transient(err)
        or _grpc_transient(err)
        or _httpx_transient(err)
        or _qdrant_transient(err)
    )


def _throttle(err: BaseException) -> bool:
    """Return True if *err* itself is a rate-limit push-back.

    (not looking at its causes).

    Args:
        err: The error.

    Returns:
        True if *err* itself is a rate-limit push-back.

    """
    if isinstance(err, ThrottledError):
        return True
    if isinstance(err, ClientError):
        response = err.response or {}
        code = response.get("Error", {}).get("Code", "")
        return code in _THROTTLE_CODES or code in _QUOTA_CODES
    if isinstance(err, grpc.RpcError):
        code_fn = getattr(err, "code", None)
        return callable(code_fn) and code_fn() == grpc.StatusCode.RESOURCE_EXHAUSTED
    if isinstance(err, httpx.HTTPStatusError):
        return err.response.status_code == _TOO_MANY_REQUESTS
    if type(err).__module__.startswith("qdrant_client"):
        return cast("object", getattr(err, "status_code", None)) == _TOO_MANY_REQUESTS
    return False


def is_throttled(err: BaseException) -> bool:
    """Return True if *err*, or an error it was raised from.

    Is a provider's rate-limit push-back.

    A throttle is the provider refusing for rate or quota, not failing: an AWS throttle
        code
    (S3 ``SlowDown``, ``ThrottlingException``, …) or ``ServiceQuotaExceededException``,
    gRPC ``RESOURCE_EXHAUSTED``, an HTTP 429, or a ``ThrottledError`` (a push-back read
    from a response body, such as a rejected bulk item). Clients wrap SDK errors in an
    ``AppError``, so the explicit cause chain is searched too. The metrics layer counts
    these apart from other errors, the quota alerts' input.

    Args:
        err: The error.

    Returns:
        True if *err*, or an error it was raised from.

    """
    seen: set[int] = set()
    current: BaseException | None = err
    while current is not None and id(current) not in seen:
        if _throttle(current):
            return True
        seen.add(id(current))
        current = getattr(current, "__cause__", None)
    return False


def is_permanent(err: BaseException) -> bool:
    """Return True if *err* is permanent.

    (poison) and should be dead-lettered without retry.

    Args:
        err: The error.

    Returns:
        True if *err* is permanent.

    """
    return not is_transient(err)
