"""httpx failure → coded ``AppError`` mapping, shared by every HTTP model client.

Raw ``httpx`` / JSON errors must not cross a provider boundary: the resilience decorators
(retry, circuit breaker) and the transport status mapping derive their behaviour from the
``ErrorCode``, so an unwrapped ``httpx.HTTPStatusError`` or ``ValueError`` would bypass
transient-vs-terminal classification (ARCHITECTURE.md#error-codes). The botocore sibling
is ``aws_boundary``.
"""

from __future__ import annotations

from http import HTTPStatus

import httpx

from techai_webutils.core.errors import AppError, ErrorCode


def httpx_error_to_app_error(exc: Exception, operation: str) -> AppError:
    """Map an HTTP transport.

    Status or decode failure of ``operation`` to a coded ``AppError``.

    A timeout → ``TIMEOUT``; an unreachable server or a 5xx/429 → ``UNAVAILABLE``
    (transient, retryable); any other status or an undecodable body → ``INTERNAL``
    (terminal). ``operation`` names the call in the message (e.g. ``"ollama chat"``).

    Returns:
        The coded error.

    """
    if isinstance(exc, httpx.TimeoutException):
        return AppError(ErrorCode.TIMEOUT, f"{operation} timed out: {exc}", cause=exc)
    if isinstance(exc, httpx.HTTPStatusError):
        status = exc.response.status_code
        transient = (
            status >= HTTPStatus.INTERNAL_SERVER_ERROR
            or status == HTTPStatus.TOO_MANY_REQUESTS
        )
        code = ErrorCode.UNAVAILABLE if transient else ErrorCode.INTERNAL
        return AppError(code, f"{operation} returned HTTP {status}", cause=exc)
    if isinstance(exc, httpx.RequestError):
        return AppError(
            ErrorCode.UNAVAILABLE, f"{operation} unreachable: {exc}", cause=exc
        )
    return AppError(ErrorCode.INTERNAL, f"{operation} decode error: {exc}", cause=exc)
