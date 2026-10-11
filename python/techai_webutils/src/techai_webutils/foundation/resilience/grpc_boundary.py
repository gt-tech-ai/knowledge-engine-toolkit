"""Shared gRPC client-boundary error translation (ARCHITECTURE.md#error-codes).

Maps a raw ``grpc.aio.AioRpcError`` raised by a gRPC stub call into a coded ``AppError`` at the client
boundary, plus the ``wrap_grpc_errors`` decorator that applies it to an async client method. A gRPC
client wraps its stub calls with it so a transient status surfaces as a transient ``AppError`` the
composition-root resiliency stack (RetryProxy) can retry — instead of a raw SDK error escaping the
retry loop. It lives in foundation (which every client imports downward) so gRPC clients share ONE
boundary rather than a lateral clients→clients import.
"""

from __future__ import annotations

import functools
from typing import TYPE_CHECKING

import grpc
from grpc.aio import AioRpcError

from techai_webutils.core.errors import AppError, ErrorCode, InternalError

if TYPE_CHECKING:
    from collections.abc import Callable
    from types import CoroutineType
    from typing import Any

# gRPC status codes treated as transient/retryable at the client boundary (ARCHITECTURE.md#error-codes).
#
# This matches ``classify._grpc_transient`` (UNAVAILABLE + DEADLINE_EXCEEDED). RESOURCE_EXHAUSTED is
# deliberately NOT here: a server answers it when a quota or budget is spent, and retrying inside the
# quota window only re-sends the call into the same exhausted budget. It maps to the
# ``RESOURCE_EXHAUSTED`` code, which is neither transient nor permanent. The one exception is an
# explicit rate-limit pushback (``grpc-retry-pushback-ms`` or ``retry-after`` in the trailers): the
# server named a delay after which the call will succeed, so the boundary raises a transient
# ``UNAVAILABLE`` carrying that delay in ``details["retry_after_ms"]``, which ``RetryProxy`` and
# ``retry_transient_async`` wait out before the next attempt.
_TRANSIENT_GRPC_CODES = frozenset({
    grpc.StatusCode.UNAVAILABLE,
    grpc.StatusCode.DEADLINE_EXCEEDED,
})
"""gRPC status codes mapped to a transient (retryable) ``AppError`` at the client boundary."""

_PUSHBACK_MS_KEY = "grpc-retry-pushback-ms"
"""Trailer carrying a server-chosen retry delay in milliseconds (gRPC retry design)."""

_RETRY_AFTER_KEY = "retry-after"
"""Trailer carrying a server-chosen retry delay in whole seconds (HTTP ``Retry-After`` semantics)."""


def _retry_pushback_ms(exc: AioRpcError) -> int | None:
    """Return the server's retry delay in milliseconds from the trailers, or None when absent/invalid."""
    trailers = exc.trailing_metadata() or ()
    for key, value in trailers:
        raw = value.decode() if isinstance(value, bytes) else value
        try:
            delay = int(raw)
        except ValueError:
            continue
        if delay < 0:
            continue
        if key == _PUSHBACK_MS_KEY:
            return delay
        if key == _RETRY_AFTER_KEY:
            return delay * 1000
    return None


def grpc_error_to_app_error(exc: AioRpcError) -> AppError:
    """Map a raw gRPC ``AioRpcError`` to a coded ``AppError`` at the client boundary.

    Coded per ARCHITECTURE.md#error-codes: a transient status (UNAVAILABLE / DEADLINE_EXCEEDED) becomes
    an ``AppError`` with a transient ``ErrorCode`` — so the resiliency stack (RetryProxy, which only
    retries a transient ``AppError``) can absorb a brief endpoint flap. RESOURCE_EXHAUSTED becomes the
    non-retryable ``RESOURCE_EXHAUSTED`` code, unless the trailers carry an explicit retry pushback, in
    which case it is a transient ``UNAVAILABLE`` with ``details["retry_after_ms"]``. Every other status
    becomes a terminal ``INTERNAL`` error carrying the gRPC cause. Without this, the SDK error escapes
    the retry loop on the first attempt.
    """
    status = exc.code()
    detail = exc.details() or str(exc)
    message = f"gRPC upstream {status.name}: {detail}"
    if status in _TRANSIENT_GRPC_CODES:
        code = (
            ErrorCode.TIMEOUT
            if status is grpc.StatusCode.DEADLINE_EXCEEDED
            else ErrorCode.UNAVAILABLE
        )
        return AppError(code, message, cause=exc)
    if status is grpc.StatusCode.RESOURCE_EXHAUSTED:
        delay_ms = _retry_pushback_ms(exc)
        if delay_ms is not None:
            return AppError(
                ErrorCode.UNAVAILABLE,
                message,
                details={"retry_after_ms": str(delay_ms)},
                cause=exc,
            )
        return AppError(ErrorCode.RESOURCE_EXHAUSTED, message, cause=exc)
    return InternalError(message, cause=exc)


def wrap_grpc_errors[**P, R](
    fn: Callable[P, CoroutineType[Any, Any, R]],
) -> Callable[P, CoroutineType[Any, Any, R]]:
    """Translate a raw gRPC ``AioRpcError`` at an async client-method boundary into a coded ``AppError``.

    Wrap every gRPC stub call so a transient status surfaces as a transient ``AppError``
    the resiliency stack can classify + retry, instead of a raw SDK error that escapes the retry loop.
    The ``ParamSpec`` preserves the method's exact signature so the decorated client still satisfies
    its port Protocol.
    """

    @functools.wraps(fn)
    async def wrapper(*args: P.args, **kwargs: P.kwargs) -> R:
        try:
            return await fn(*args, **kwargs)
        except AioRpcError as exc:
            raise grpc_error_to_app_error(exc) from exc

    return wrapper
