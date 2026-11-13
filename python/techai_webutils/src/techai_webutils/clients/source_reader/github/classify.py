"""Codes a GitHub REST failure, so retry, breaker and transport status derive from it.

Mirrors the Go github connector's classification: 5xx, 429 and a rate-limited 403 are
transient (``UNAVAILABLE``, as a throttle carrying GitHub's pushback when it gave one);
404, 401 and a plain 403 are permanent; any other status is ``UPSTREAM``. A transport
timeout is ``TIMEOUT`` and any other transport failure ``UNAVAILABLE``.
"""

from __future__ import annotations

import math
from http import HTTPStatus
from typing import TYPE_CHECKING, NoReturn

import httpx

from techai_webutils.core.errors import (
    AppError,
    AppTimeoutError,
    ErrorCode,
    ForbiddenError,
    NotFoundError,
    ThrottledError,
    UnauthorizedError,
    UnavailableError,
)
from techai_webutils.foundation.resilience.async_retry import RETRY_AFTER_MS_DETAIL

if TYPE_CHECKING:
    from collections.abc import Awaitable

_MS_PER_S = 1000


async def send(request: Awaitable[httpx.Response]) -> httpx.Response:
    """Await ``request``, coding a transport failure.

    Args:
        request: The pending HTTP call.

    Returns:
        The response, whatever its status.

    Raises:
        AppTimeoutError: The call timed out (``TIMEOUT``).
        UnavailableError: Any other transport failure (``UNAVAILABLE``).

    """
    try:
        return await request
    except httpx.TimeoutException as err:
        msg = "github: the request timed out"
        raise AppTimeoutError(msg) from err
    except httpx.HTTPError as err:
        msg = f"github: the request failed ({type(err).__name__})"
        raise UnavailableError(msg) from err


def raise_for(response: httpx.Response, *, expected: int, now: float) -> None:
    """Raise the coded error for ``response`` unless it has the ``expected`` status.

    Args:
        response: The GitHub response.
        expected: The success status.
        now: The wall clock in Unix seconds, against which ``X-RateLimit-Reset`` is read.

    A 429 or a rate-limited 403 raises a ``ThrottledError`` (``UNAVAILABLE``).

    Raises:
        NotFoundError: 404 (``NOT_FOUND``).
        UnauthorizedError: 401 (``UNAUTHENTICATED``).
        ForbiddenError: Any other 403 (``FORBIDDEN``).
        UnavailableError: 5xx (``UNAVAILABLE``).
        AppError: Any other status (``UPSTREAM``).

    """
    status = response.status_code
    if status == expected:
        return
    what = f"github: {response.request.method} answered {status}"
    if status == HTTPStatus.NOT_FOUND:
        raise NotFoundError(what)
    if status == HTTPStatus.UNAUTHORIZED:
        raise UnauthorizedError(what)
    if status == HTTPStatus.TOO_MANY_REQUESTS or (
        status == HTTPStatus.FORBIDDEN and _rate_limited(response)
    ):
        _raise_throttled(what, response, now)
    if status == HTTPStatus.FORBIDDEN:
        raise ForbiddenError(what)
    if status >= HTTPStatus.INTERNAL_SERVER_ERROR:
        raise UnavailableError(what)
    raise AppError(ErrorCode.UPSTREAM, what)


def _rate_limited(response: httpx.Response) -> bool:
    """Return whether a 403 is GitHub's primary or secondary rate limit.

    Args:
        response: The 403 response.

    Returns:
        True when it carries ``Retry-After``, ``X-RateLimit-Remaining: 0``, or a
        rate-limit message.

    """
    headers = response.headers
    return (
        "retry-after" in headers
        or headers.get("x-ratelimit-remaining") == "0"
        or "rate limit" in response.text.lower()
    )


def _raise_throttled(what: str, response: httpx.Response, now: float) -> NoReturn:
    """Raise a throttle carrying GitHub's pushback (``retry_after_ms``) if it gave one.

    ``Retry-After`` (whole seconds) wins; otherwise a spent primary limit waits until
    ``X-RateLimit-Reset`` (Unix seconds). The ``ThrottledError`` is ``UNAVAILABLE``, so
    the retry layer backs off.

    Args:
        what: The error message.
        response: The rate-limited response.
        now: The wall clock in Unix seconds.

    """
    err = ThrottledError(what)
    delay_s = _pushback_s(response, now)
    if delay_s is not None:
        err.details[RETRY_AFTER_MS_DETAIL] = str(math.ceil(delay_s * _MS_PER_S))
    raise err


def _pushback_s(response: httpx.Response, now: float) -> float | None:
    """Return GitHub's requested wait in seconds, or None when it named none.

    Args:
        response: The rate-limited response.
        now: The wall clock in Unix seconds.

    Returns:
        The wait, never negative, or None.

    """
    headers = response.headers
    retry_after = headers.get("retry-after") or ""
    if retry_after.isdigit():
        return float(retry_after)
    reset = headers.get("x-ratelimit-reset") or ""
    if headers.get("x-ratelimit-remaining") == "0" and reset.isdigit():
        return max(0.0, int(reset) - now)
    return None
