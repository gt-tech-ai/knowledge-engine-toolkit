"""Unit tests for the bearer-token sources (HTTP mocked with ``httpx.MockTransport``)."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, cast
from urllib.parse import parse_qs

import httpx
import pytest

from techai_webutils.clients.token_source import (
    TokenSourceConfig,
    TokenSourceKind,
    new_token_source_from_config,
)
from techai_webutils.clients.token_source.oauth.source import OAuthTokenSource
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Callable, Coroutine

_URL = "https://tenant.example/oauth/token"


class _Clock:
    """A monotonic clock the test steps by hand."""

    def __init__(self) -> None:
        self.now = 1000.0

    def __call__(self) -> float:
        return self.now


def _source(
    handler: Callable[[httpx.Request], Coroutine[None, None, httpx.Response]],
    clock: _Clock,
) -> OAuthTokenSource:
    """Build an oauth source over a mocked transport answering with ``handler``."""
    config = TokenSourceConfig(
        kind=TokenSourceKind.OAUTH,
        token_url=_URL,
        grant_type="http://auth0.com/oauth/grant-type/password-realm",
        client_id="cid",
        client_secret="csecret",
        audience="https://api.example",
        username="canary@example.com",
        password="pw",
        realm="Username-Password-Authentication",
    )
    client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
    return OAuthTokenSource(
        client, config.token_url, config.grant(), refresh_margin_seconds=60, clock=clock
    )


@pytest.mark.asyncio
async def test_oauth_token_source_refreshes_before_expiry() -> None:
    """The oauth source caches a token and mints a new one within the margin.

    Of its expiry.

    Why this test is important: A caller attaches ``token()`` to every request. Minting on
        every call would hammer the identity provider (Auth0 rate limits its token
        endpoint); refreshing only after expiry would send expired tokens and fail
        requests at every rollover.

    What it tests:
        - The first call posts the configured grant as a form and returns its
          ``access_token``.
        - A call while the token outlives the margin returns it with no request.
        - A call within the margin of ``expires_in`` mints a new token.
        - Concurrent callers on a stale token share one mint.

    """
    clock = _Clock()
    forms: list[dict[str, list[str]]] = []

    async def handler(request: httpx.Request) -> httpx.Response:
        forms.append(parse_qs(request.content.decode()))
        await asyncio.sleep(0)  # yield, so the concurrent callers below overlap the mint
        return httpx.Response(
            200, json={"access_token": f"t{len(forms)}", "expires_in": 600}
        )

    source = _source(handler, clock)
    assert await source.token() == "t1"
    assert forms[0]["grant_type"] == ["http://auth0.com/oauth/grant-type/password-realm"]
    assert forms[0]["username"] == ["canary@example.com"]
    assert forms[0]["realm"] == ["Username-Password-Authentication"]
    assert "scope" not in forms[0]

    clock.now += 539
    assert await source.token() == "t1"
    assert len(forms) == 1

    clock.now += 1
    tokens = await asyncio.gather(*(source.token() for _ in range(5)))
    assert tokens == ["t2"] * 5
    assert len(forms) == 2


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("response", "code"),
    [
        (httpx.Response(401, json={"error": "invalid_grant"}), ErrorCode.UNAUTHORIZED),
        (httpx.Response(403, json={}), ErrorCode.UNAUTHORIZED),
        (httpx.Response(429, json={}), ErrorCode.UNAVAILABLE),
        (httpx.Response(503, json={}), ErrorCode.UNAVAILABLE),
        (httpx.Response(302, json={}), ErrorCode.UPSTREAM),
        (httpx.Response(200, text="not json"), ErrorCode.UPSTREAM),
        (httpx.Response(200, json={"expires_in": 600}), ErrorCode.UPSTREAM),
        (
            httpx.Response(200, json={"access_token": "t", "expires_in": 0}),
            ErrorCode.UPSTREAM,
        ),
        (
            httpx.Response(200, json={"access_token": "t", "expires_in": True}),
            ErrorCode.UPSTREAM,
        ),
    ],
)
async def test_oauth_token_source_codes_each_failure(
    response: httpx.Response, code: ErrorCode
) -> None:
    """A failed mint raises the coded error its cause maps to.

    Why this test is important: The code decides the caller's reaction: a refused grant is
        a credential problem no retry fixes, while a throttle or outage clears on a later
        attempt.

    What it tests:
        - 400/401/403 is ``UNAUTHORIZED``; 429 and 5xx are ``UNAVAILABLE``.
        - Another status, a non-JSON body, or a reply without a token or positive lifetime
          is ``UPSTREAM``.

    """

    async def handler(_request: httpx.Request) -> httpx.Response:
        await asyncio.sleep(0)
        return response

    source = _source(handler, _Clock())
    with pytest.raises(AppError) as info:
        await source.token()
    assert info.value.code == code


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("exc", "code"),
    [
        (httpx.ReadTimeout("slow"), ErrorCode.TIMEOUT),
        (httpx.ConnectError("refused"), ErrorCode.UNAVAILABLE),
    ],
)
async def test_oauth_token_source_codes_transport_failures(
    exc: httpx.HTTPError, code: ErrorCode
) -> None:
    """A timeout is ``TIMEOUT`` and another transport failure ``UNAVAILABLE``.

    Why this test is important:
        Both are transient, so a caller's retry decorator must see them as such.

    What it tests:
        - The transport's exception maps to its code.

    """

    async def handler(_request: httpx.Request) -> httpx.Response:
        await asyncio.sleep(0)
        raise exc

    with pytest.raises(AppError) as info:
        await _source(handler, _Clock()).token()
    assert info.value.code == code


@pytest.mark.asyncio
async def test_token_source_factory_selects_kind() -> None:
    """The factory builds the static source by default.

    And refuses an incomplete oauth config.

    Why this test is important: The static kind keeps a composition root bootable with no
        identity provider; an oauth config without an endpoint must fail at startup, not
        at the first request.

    What it tests:
        - The default config hands out its configured token; ``oauth`` builds the oauth
          source.
        - ``oauth`` without ``token_url`` and an unknown kind raise ``AppValueError``.
        - The secrets stay out of the config's repr.

    """
    static = new_token_source_from_config(TokenSourceConfig(token="dev"))
    assert await static.token() == "dev"
    oauth = new_token_source_from_config(
        TokenSourceConfig(kind=TokenSourceKind.OAUTH, token_url=_URL)
    )
    assert isinstance(oauth, OAuthTokenSource)
    with pytest.raises(AppValueError, match="token_url"):
        new_token_source_from_config(TokenSourceConfig(kind=TokenSourceKind.OAUTH))
    with pytest.raises(AppValueError, match="unknown"):
        new_token_source_from_config(
            TokenSourceConfig(kind=cast("TokenSourceKind", "ldap"))
        )
    shown = repr(
        TokenSourceConfig(token="SECRET-T", client_secret="SECRET-C", password="SECRET-P")
    )
    assert "SECRET" not in shown
