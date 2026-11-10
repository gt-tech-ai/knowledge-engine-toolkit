"""Unit tests for the in-flight claims and their factory.

(the async Redis client is mocked).
"""

from __future__ import annotations

from typing import cast
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from prometheus_client import CollectorRegistry
from redis.asyncio import Redis
from redis.exceptions import ConnectionError as RedisConnectionError

from techai_webutils.clients.inflight_claim import (
    InflightClaimConfig,
    InflightClaimKind,
    inflight_claim_from_config,
)
from techai_webutils.clients.inflight_claim.noop import NoopInflightClaim
from techai_webutils.clients.inflight_claim.redis import RedisInflightClaim
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.checkpoint import Stage
from techai_webutils.core.interfaces.inflight_claim import inflight_key
from techai_webutils.foundation.metrics.metrics import new_metrics


def _client(*, set_result: object = True) -> tuple[MagicMock, AsyncMock]:
    """Return a mocked async Redis client and the release script it registers.

    Args:
        set_result: What ``SET NX`` returns.

    Returns:
        A mocked async Redis client and the release script it registers.

    """
    script = AsyncMock(return_value=1)
    client = MagicMock(spec=Redis)
    cast("MagicMock", client.register_script).return_value = script
    client.set = AsyncMock(return_value=set_result)
    return client, script


def _redis_claim(client: MagicMock) -> RedisInflightClaim:
    """Build the redis claim through the factory, prefix ``inflight``.

    Args:
        client: The mocked Redis client.

    Returns:
        The redis claim through the factory, prefix ``inflight``.

    """
    claim = inflight_claim_from_config(
        InflightClaimConfig(kind=InflightClaimKind.REDIS), redis=client
    )
    assert isinstance(claim, RedisInflightClaim)
    return claim


@pytest.mark.asyncio
async def test_default_kind_is_noop_and_grants_every_claim() -> None:
    """Test that the default config builds the noop claim, which never refuses.

    **Why this test is important:**
      - The default must keep today's behaviour and cost: no Redis round-trip, every
        duplicate runs.

    **What it tests:**
      - ``InflightClaimConfig()`` builds ``NoopInflightClaim``
      - two claims of the same key both return True, and release is a no-op
    """
    claim = inflight_claim_from_config(InflightClaimConfig())

    assert isinstance(claim, NoopInflightClaim)
    assert await claim.claim("k", 30)
    assert await claim.claim("k", 30)
    await claim.release("k")


@pytest.mark.asyncio
async def test_redis_claim_sets_key_nx_with_ttl_in_ms() -> None:
    """Test that a redis claim is one ``SET NX PX`` on the prefixed key.

    **Why this test is important:**
      - ``NX`` is what makes the claim exclusive; ``PX`` makes a crashed holder's claim
        lapse.

    **What it tests:**
      - ``claim("k", 1.5)`` calls ``set("inflight:k", <token>, nx=True, px=1500)`` and
        returns True
    """
    client, _ = _client()
    claim = _redis_claim(client)

    assert await claim.claim("k", 1.5)

    cast("MagicMock", cast("AsyncMock", client.set).assert_awaited_once)()
    args, kwargs = cast(
        "tuple[tuple[object, ...], dict[str, object]]",
        cast("AsyncMock", client.set).await_args,
    )
    assert args[0] == "inflight:k"
    assert kwargs == {"nx": True, "px": 1500}


@pytest.mark.asyncio
async def test_redis_claim_lost_returns_false_and_release_skips() -> None:
    """Test that a claim another consumer holds is refused.

    And its release touches nothing.

    **Why this test is important:**
      - A loser must never delete the winner's key, or a third consumer would run the
        stage too.

    **What it tests:**
      - ``set`` returning None makes ``claim`` False
      - ``release`` of that key never runs the release script
    """
    client, script = _client(set_result=None)
    claim = _redis_claim(client)

    assert not await claim.claim("k", 10)
    await claim.release("k")

    script.assert_not_awaited()


@pytest.mark.asyncio
async def test_redis_release_deletes_only_with_own_token() -> None:
    """Test that release runs the compare-and-delete script with the token the claim set.

    **Why this test is important:**
      - A holder whose claim lapsed must not free a newer holder's key; the token check
        does that.

    **What it tests:**
      - the script is awaited once with ``keys=["inflight:k"]`` and the token passed to
        ``set``
      - a second release of the same key does nothing
    """
    client, script = _client()
    claim = _redis_claim(client)
    _ = await claim.claim("k", 10)
    token = cast(
        "object", cast("MagicMock", cast("AsyncMock", client.set).await_args).args[1]
    )

    await claim.release("k")
    await claim.release("k")

    script.assert_awaited_once_with(keys=["inflight:k"], args=[token])


@pytest.mark.asyncio
async def test_redis_errors_fail_open() -> None:
    """Test that a Redis outage grants the claim and a failed release is swallowed.

    **Why this test is important:**
      - The claim only saves cost; an outage must never stop ingestion (the stages are
        idempotent).

    **What it tests:**
      - ``set`` raising makes ``claim`` True
      - a release whose script raises returns without raising
    """
    client, script = _client()
    client.set = AsyncMock(side_effect=RedisConnectionError("down"))
    claim = _redis_claim(client)
    assert await claim.claim("k", 10)

    client.set = AsyncMock(return_value=True)
    script.side_effect = RedisConnectionError("down")
    assert await claim.claim("j", 10)
    await claim.release("j")


def test_redis_kind_without_client_and_unknown_kind_raise() -> None:
    """Test that the factory fails loudly with neither a client nor a host.

    Or an unknown kind.

    **Why this test is important:**
      - A misconfigured kind must stop the app at boot, not silently run without a claim.

    **What it tests:**
      - ``kind=redis`` with no client and no host raises ``AppValueError``
      - an unknown kind raises ``AppValueError``
    """
    with pytest.raises(AppValueError, match="client or host"):
        _ = inflight_claim_from_config(InflightClaimConfig(kind=InflightClaimKind.REDIS))
    bogus = MagicMock(spec=InflightClaimConfig)
    bogus.kind = "bogus"
    with pytest.raises(AppValueError):
        _ = inflight_claim_from_config(bogus)


def test_inflight_key_names_content_and_stage() -> None:
    """Test that the claim key names the org, content, stage and fingerprint.

    Not the document.

    **Why this test is important:**
      - Two uploads of the same bytes in one org must contend for one claim, so the key
        ignores the document id; the org keeps tenants apart and the fingerprint separates
        versions.

    **What it tests:**
      - ``inflight_key("o", "abc", Stage.PARSE, "fp")`` is ``"o:abc:parse:fp"``
    """
    assert inflight_key("o", "abc", Stage.PARSE, "fp") == "o:abc:parse:fp"


@pytest.mark.asyncio
async def test_inflight_claim_fails_open_on_redis_outage_and_counts_it() -> None:
    """Test that a claim opening its own client absorbs a Redis outage.

    Counts it and closes it.

    **Why this test is important:**
      - A cache node outage must never block ingestion, yet an operator has to see that
        the dedup claim stopped working, and a client the factory opened must not leak.

    **What it tests:**
      - with no client, the factory opens one on ``config.host``
      - ``set`` raising grants the claim and counts
        ``inflight_claim_errors_total{op="claim"}``
      - ``aclose`` closes the client the factory opened
    """
    client, _ = _client()
    client.set = AsyncMock(side_effect=RedisConnectionError("down"))
    client.aclose = AsyncMock()
    registry = CollectorRegistry()
    with patch(
        "techai_webutils.clients.inflight_claim.builder.Redis", return_value=client
    ) as opened:
        claim = inflight_claim_from_config(
            InflightClaimConfig(kind=InflightClaimKind.REDIS, host="redis"),
            metrics=new_metrics(registry),
        )

    assert await claim.claim("k", 10)
    errors = registry.get_sample_value("inflight_claim_errors_total", {"op": "claim"})
    assert errors == 1
    await claim.aclose()

    assert opened.call_args.kwargs["host"] == "redis"
    cast("MagicMock", cast("AsyncMock", client.aclose).assert_awaited_once)()
