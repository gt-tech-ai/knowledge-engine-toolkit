"""Unit tests for the semantic cache's match tiers and the ``TieredLookup``.

That chains them.

The backend is a ``SemanticCache`` mock whose ``exact`` / ``nearest`` primitives return
fixed candidates, so each test pins a tier decision, not a search.
"""

from __future__ import annotations

import asyncio
from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.semantic_cache.tiers import (
    CacheTierConfig,
    CacheTierKind,
    cache_lookup_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.semantic_cache import (
    CachedAnswer,
    CacheNamespace,
    CacheVerifier,
    SemanticCache,
)

_NS = CacheNamespace("ws-1", "m", "v1")


def _answer(similarity: float) -> CachedAnswer:
    """Return a candidate.

    At ``similarity`` whose cached question is "the cached question".

    Returns:
        A candidate.

    """
    return CachedAnswer(
        payload=b"a",
        similarity=similarity,
        source_query="the cached question",
        embedding_model="m",
    )


def _backend(
    exact: CachedAnswer | None = None, nearest: CachedAnswer | None = None
) -> MagicMock:
    """Return a ``SemanticCache`` mock whose primitives return the given candidates.

    Returns:
        A ``SemanticCache`` mock whose primitives return the given candidates.

    """
    cache = MagicMock(spec=SemanticCache)
    cache.exact = AsyncMock(return_value=exact)
    cache.nearest = AsyncMock(return_value=nearest)
    return cache


def _verifier(*, same: bool = True, delay: float = 0.0) -> MagicMock:
    """Return a ``CacheVerifier`` mock answering ``same`` after ``delay`` seconds.

    Returns:
        A ``CacheVerifier`` mock answering ``same`` after ``delay`` seconds.

    """

    async def same_question(_cached: str, _query: str) -> bool:
        await asyncio.sleep(delay)
        return same

    verifier = MagicMock(spec=CacheVerifier)
    verifier.same_question = AsyncMock(side_effect=same_question)
    return verifier


_ALL = (
    CacheTierConfig(kind=CacheTierKind.EXACT),
    CacheTierConfig(kind=CacheTierKind.SEMANTIC, min_similarity=0.95),
    CacheTierConfig(kind=CacheTierKind.VERIFIED, min_similarity=0.95, floor=0.88),
)


@pytest.mark.asyncio
async def test_tiers_serve_in_configured_order_and_record_the_tier() -> None:
    """Test that the first serving tier wins and the hit records which tier served it.

    **Why this test is important:**
      - Tier order is configuration: an exact repeat must not pay for an embedding, and
        the metrics and spans report the serving tier.

    **What it tests:**
      - with an exact candidate, the hit's ``tier`` is ``exact`` and ``nearest`` is never
        awaited
      - without one, a neighbour at 0.97 is served by ``semantic``
    """
    backend = _backend(exact=_answer(1.0), nearest=_answer(0.97))
    lookup = cache_lookup_from_config(_ALL, cache=backend, verifier=_verifier())

    hit = await lookup.lookup("q", _NS)

    assert hit is not None
    assert hit.tier == "exact"
    cast("MagicMock", cast("AsyncMock", backend.nearest).assert_not_awaited)()

    backend = _backend(nearest=_answer(0.97))
    hit = await cache_lookup_from_config(
        _ALL, cache=backend, verifier=_verifier()
    ).lookup("q", _NS)
    assert hit is not None
    assert hit.tier == "semantic"


@pytest.mark.asyncio
async def test_semantic_and_verified_share_one_neighbour_search() -> None:
    """Test that the semantic and verified tiers read the neighbour once per lookup.

    **Why this test is important:**
      - Each ``nearest`` is an embedding call plus a vector search; listing both tiers
        must not double the cost of a miss.

    **What it tests:**
      - a neighbour at 0.80 (below the band) misses through both tiers with one
        ``nearest`` call
    """
    backend = _backend(nearest=_answer(0.80))
    lookup = cache_lookup_from_config(_ALL, cache=backend, verifier=_verifier())

    assert await lookup.lookup("q", _NS) is None
    assert (cast("AsyncMock", backend.nearest).await_count) == 1


@pytest.mark.asyncio
async def test_verified_band_serves_only_what_the_verifier_confirms() -> None:
    """Test the verifier band: confirm serves, deny misses, a slow verifier misses.

    **Why this test is important:**
      - The band is where near-miss questions ("Q3 revenue" vs "Q4 revenue") land; only an
        explicit confirmation may serve one, and a hung verifier must not stall the query.

    **What it tests:**
      - a neighbour at 0.90 with a confirming verifier is served by ``verified``, and the
        verifier saw the cached question and the new one
      - the same neighbour with a denying verifier, or one slower than
        ``timeout_seconds``, misses
    """
    configs = (
        CacheTierConfig(kind=CacheTierKind.SEMANTIC, min_similarity=0.95),
        CacheTierConfig(
            kind=CacheTierKind.VERIFIED,
            min_similarity=0.95,
            floor=0.88,
            timeout_seconds=0.05,
        ),
    )
    confirming = _verifier(same=True)
    hit = await cache_lookup_from_config(
        configs, cache=_backend(nearest=_answer(0.90)), verifier=confirming
    ).lookup("new question", _NS)

    assert hit is not None
    assert hit.tier == "verified"
    cast(
        "MagicMock", cast("AsyncMock", confirming.same_question).assert_awaited_once_with
    )("the cached question", "new question")

    for verifier in (_verifier(same=False), _verifier(same=True, delay=1.0)):
        lookup = cache_lookup_from_config(
            configs, cache=_backend(nearest=_answer(0.90)), verifier=verifier
        )
        assert await lookup.lookup("new question", _NS) is None


@pytest.mark.asyncio
async def test_per_call_similarity_overrides_the_configured_threshold() -> None:
    """Test that a lookup's ``min_similarity`` replaces each tier's configured threshold.

    **Why this test is important:**
      - Similarity is a per-workspace setting; 1.0 must mean "exact repeats only", and the
        verifier band's ceiling follows the setting.

    **What it tests:**
      - a 0.97 neighbour hits at the configured 0.95 and misses at an override of 1.0
      - with an override of 0.92, a 0.90 neighbour goes to the verifier band ([0.88,
        0.92))
    """
    backend = _backend(nearest=_answer(0.97))
    lookup = cache_lookup_from_config(_ALL, cache=backend, verifier=_verifier(same=False))

    assert await lookup.lookup("q", _NS) is not None
    assert await lookup.lookup("q", _NS, min_similarity=1.0) is None

    confirming = _verifier(same=True)
    lookup = cache_lookup_from_config(
        _ALL, cache=_backend(nearest=_answer(0.90)), verifier=confirming
    )
    hit = await lookup.lookup("q", _NS, min_similarity=0.92)
    assert hit is not None
    assert hit.tier == "verified"


@pytest.mark.asyncio
async def test_an_empty_tier_list_never_looks_up_and_a_backend_error_misses() -> None:
    """Test that no tiers means no lookup, and a raising backend is a miss.

    **Why this test is important:**
      - An empty list is how a deployment turns matching off; and the cache is fail-open,
        so an outage must look exactly like a miss.

    **What it tests:**
      - with no tiers, ``lookup`` returns ``None`` and neither primitive is awaited
      - a raising ``exact`` makes the lookup return ``None`` instead of raising
    """
    backend = _backend(exact=_answer(1.0))
    assert await cache_lookup_from_config((), cache=backend).lookup("q", _NS) is None
    cast("MagicMock", cast("AsyncMock", backend.exact).assert_not_awaited)()

    broken = _backend()
    broken.exact = AsyncMock(side_effect=RuntimeError("down"))
    lookup = cache_lookup_from_config(_ALL[:1], cache=broken)
    assert await lookup.lookup("q", _NS) is None


@pytest.mark.parametrize(
    ("configs", "verifier"),
    [
        ((CacheTierConfig(kind="fuzzy"),), None),
        ((CacheTierConfig(kind="exact"), CacheTierConfig(kind="exact")), None),
        ((CacheTierConfig(kind="verified"),), None),
        (
            (CacheTierConfig(kind="verified", floor=0.95, min_similarity=0.95),),
            "verifier",
        ),
        ((CacheTierConfig(kind="semantic", min_similarity=1.5),), None),
    ],
    ids=[
        "unknown",
        "duplicate",
        "verified-without-verifier",
        "empty-band",
        "out-of-range",
    ],
)
def test_cache_lookup_factory_refuses_an_invalid_tier_list(
    configs: tuple[CacheTierConfig, ...], verifier: str | None
) -> None:
    """Test that the factory fails loudly on a tier list it cannot run.

    **Why this test is important:**
      - A misconfigured tier list must stop the service at startup, not silently serve
        nothing (or everything).

    **What it tests:**
      - an unknown kind, a duplicate kind, ``verified`` without a verifier, a band whose
        floor is not below its ceiling, and a similarity above 1.0 each raise
        ``INVALID_INPUT``
    """
    with pytest.raises(AppError) as caught:
        _ = cache_lookup_from_config(
            configs,
            cache=_backend(),
            verifier=_verifier() if verifier else None,
        )
    assert caught.value.code is ErrorCode.INVALID_INPUT
