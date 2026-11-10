"""``OAuthTokenSource``: mints and caches an OAuth 2.0 access token from a token endpoint.

The source posts a configured grant (client credentials, password, or a provider's
extension grant such as Auth0's ``password-realm``) as a form, caches the ``access_token``
for its ``expires_in``, and mints a new one once the cached token is within
``refresh_margin_seconds`` of expiry. Concurrent callers share one mint. Errors carry
codes: a timeout is ``TIMEOUT``,
    another
transport failure or a 429/5xx is ``UNAVAILABLE`` (retried), a refused grant (400, 401,
403) is ``UNAUTHORIZED``, and a reply without a token is ``UPSTREAM``.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, cast, final, override

import httpx

from techai_webutils.core.errors import (
    AppError,
    AppTimeoutError,
    ErrorCode,
    UnauthorizedError,
    UnavailableError,
)
from techai_webutils.core.interfaces.token_source import TokenSource

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

_REFUSED_STATUSES = frozenset({400, 401, 403})
"""Statuses of a grant the server refused: the credentials or the grant are wrong."""
_TRANSIENT_FLOOR = 500
"""5xx statuses (and 429) may clear on a later attempt."""
_THROTTLED = 429
"""The server is rate limiting token requests."""
_OK = 200
"""The only success status."""


@final
class OAuthTokenSource(TokenSource):
    """Mints access tokens from ``token_url`` with the ``grant`` form fields."""

    def __init__(
        self,
        client: httpx.AsyncClient,
        token_url: str,
        grant: Mapping[str, str],
        *,
        refresh_margin_seconds: float = 60.0,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        """Post ``grant`` to ``token_url`` through ``client``.

        The token is refreshed within the margin of expiry.

        ``clock`` returns monotonic seconds; tests inject one to step time.

        Args:
            client: The httpx client.
            token_url: The token endpoint.
            grant: The grant's form fields.
            refresh_margin_seconds: How long before expiry a token is refreshed.
            clock: Reads the monotonic time.

        """
        self._client = client
        self._token_url = token_url
        self._grant = dict(grant)
        self._margin = refresh_margin_seconds
        self._clock = clock
        self._token: str | None = None
        self._expires_at = 0.0
        self._lock = asyncio.Lock()

    @override
    async def token(self) -> str:
        """Return the cached token.

        A new one is minted when it is within the margin of expiry.

        Returns:
            The cached token.

        """
        if (cached := self._fresh()) is not None:
            return cached
        async with self._lock:
            if (cached := self._fresh()) is not None:
                return cached
            return await self._mint()

    def _fresh(self) -> str | None:
        """Return the cached token while it outlives the refresh margin.

        Returns:
            The cached token, or ``None`` when there is none or it is near expiry.

        """
        if self._token and self._clock() < self._expires_at - self._margin:
            return self._token
        return None

    async def _mint(self) -> str:
        """Request a new token and cache it with its expiry.

        A callee raises ``UnauthorizedError``: The endpoint refused the grant.

        A callee raises ``AppError``: Any other status, or a reply without a token
        (``UPSTREAM``).

        Returns:
            The new token.

        Raises:
            AppTimeoutError: The request timed out.
            UnavailableError: The endpoint was unreachable, throttled, or failed (5xx).

        """
        started = self._clock()
        try:
            resp = await self._client.post(self._token_url, data=self._grant)
        except httpx.TimeoutException as exc:
            msg = "token request timed out"
            raise AppTimeoutError(msg) from exc
        except httpx.HTTPError as exc:
            msg = f"token request failed: {type(exc).__name__}"
            raise UnavailableError(msg) from exc
        _check_status(resp.status_code)
        token, expires_in = _parse(resp)
        self._token = token
        self._expires_at = started + expires_in
        return token


def _check_status(status: int) -> None:
    """Raise the coded error a non-200 token response maps to.

    Args:
        status: The response's HTTP status.

    Raises:
        UnauthorizedError: The grant was refused (400, 401, 403).
        UnavailableError: Throttled (429) or a server error (5xx).
        AppError: Any other non-200 status (``UPSTREAM``).

    """
    if status == _OK:
        return
    msg = f"token endpoint returned HTTP {status}"
    if status in _REFUSED_STATUSES:
        raise UnauthorizedError(msg)
    if status == _THROTTLED or status >= _TRANSIENT_FLOOR:
        raise UnavailableError(msg)
    raise AppError(ErrorCode.UPSTREAM, msg)


def _parse(resp: httpx.Response) -> tuple[str, float]:
    """Return the ``access_token`` and its positive ``expires_in`` from a token response.

    Args:
        resp: The token response.

    Returns:
        The ``access_token`` and its positive ``expires_in`` from a token response.

    Raises:
        AppError: The body is not JSON or lacks a token or a positive lifetime
            (``UPSTREAM``).

    """
    try:
        body = cast("object", resp.json())
    except ValueError as exc:
        msg = "token response is not JSON"
        raise AppError(ErrorCode.UPSTREAM, msg) from exc
    fields = cast("dict[str, object]", body) if isinstance(body, dict) else {}
    token = fields.get("access_token")
    expires_in = fields.get("expires_in")
    if not isinstance(token, str) or not token:
        msg = "token response has no access_token"
        raise AppError(ErrorCode.UPSTREAM, msg)
    if (
        not isinstance(expires_in, int | float)
        or isinstance(expires_in, bool)
        or expires_in <= 0
    ):
        msg = "token response has no positive expires_in"
        raise AppError(ErrorCode.UPSTREAM, msg)
    return token, float(expires_in)
