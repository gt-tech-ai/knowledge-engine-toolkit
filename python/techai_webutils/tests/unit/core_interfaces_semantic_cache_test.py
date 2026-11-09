"""Unit tests for the ``SemanticCache`` contract's namespace and answer value types."""

from __future__ import annotations

import dataclasses
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.cache import CacheInvalidator
from techai_webutils.core.interfaces.semantic_cache import (
    CacheAdmission,
    CacheCandidates,
    CachedAnswer,
    CacheLookup,
    CacheNamespace,
    CacheTier,
    CacheVerifier,
    SemanticCache,
)

_EXPECTED_TAG_COUNT = 16


def test_cache_namespace_isolation() -> None:
    """Test that namespaces differing in any field never share an identity or a tag.

    **Why this test is important:**
      - The namespace is the tenancy and embedding-space partition; two workspaces, two
        embedding models or two clearance scopes sharing a tag would serve one tenant's
        cached answer to another.

    **What it tests:**
      - changing each of the four fields yields an unequal namespace with a distinct
        ``tag()``
      - equal namespaces share a 16-hex-char tag
    """
    base = CacheNamespace("ws-1", "nomic-embed-text", "v1", "internal")
    variants = [
        dataclasses.replace(base, workspace_id="ws-2"),
        dataclasses.replace(base, embedding_model="titan-embed"),
        dataclasses.replace(base, embedding_version="v2"),
        dataclasses.replace(base, scope="confidential"),
    ]

    tags = {base.tag(), *(variant.tag() for variant in variants)}

    assert all(variant != base for variant in variants)
    assert len(tags) == len(variants) + 1
    assert (
        base.tag() == CacheNamespace("ws-1", "nomic-embed-text", "v1", "internal").tag()
    )
    assert len(base.tag()) == _EXPECTED_TAG_COUNT
    assert not CacheNamespace("ws-1", "m", "v1").scope


def test_cache_namespace_rejects_a_separator_in_the_workspace() -> None:
    """Test that a workspace id carrying the key separator ``:`` is invalid input.

    **Why this test is important:**
      - Backends key and evict by ``<workspace>:``; a workspace ``ws:a`` would be evicted
        with ``ws`` and could collide with another workspace's keys.

    **What it tests:**
      - ``CacheNamespace("ws:a", …)`` raises a ``ValueError`` coded ``INVALID_INPUT``
    """
    with pytest.raises(ValueError, match="workspace_id") as caught:
        CacheNamespace("ws:a", "m", "v1")

    assert isinstance(caught.value, AppError)
    assert caught.value.code is ErrorCode.INVALID_INPUT


def test_cached_answer_is_frozen_and_cache_composes_invalidator() -> None:
    """Test that a cached answer is immutable and the cache is a ``CacheInvalidator``.

    **Why this test is important:**
      - A hit's payload is decoded and audited by different layers; and namespace eviction
        must be the same capability every cache tier exposes.

    **What it tests:**
      - assigning ``CachedAnswer.payload`` raises ``FrozenInstanceError``
      - ``SemanticCache`` is a subclass of ``CacheInvalidator``
    """
    answer = CachedAnswer(
        payload=b"x", similarity=1.0, source_query="q", embedding_model="m"
    )

    with pytest.raises(dataclasses.FrozenInstanceError):
        answer.payload = b"y"  # pyright: ignore[reportAttributeAccessIssue]
    assert issubclass(SemanticCache, CacheInvalidator)


def _answer(similarity: float, tier: str = "") -> CachedAnswer:
    """Return a cached answer at ``similarity``.

    Returns:
        A cached answer at ``similarity``.

    """
    return CachedAnswer(
        payload=b"a",
        similarity=similarity,
        source_query="q",
        embedding_model="m",
        tier=tier,
    )


def _backend(exact: CachedAnswer | None, nearest: CachedAnswer | None) -> MagicMock:
    """Return a ``SemanticCache`` mock whose primitives return ``exact``.

    And ``nearest``.

    Returns:
        A ``SemanticCache`` mock whose primitives return ``exact``.

    """
    cache = MagicMock(spec=SemanticCache)
    cache.exact = AsyncMock(return_value=exact)
    cache.nearest = AsyncMock(return_value=nearest)
    return cache


@pytest.mark.asyncio
async def test_lookup_default_serves_the_exact_repeat_before_the_neighbour() -> None:
    """Test that the default ``lookup`` composes ``exact`` then a thresholded ``nearest``.

    **Why this test is important:**
      - Every backend implements only the two primitives; ``lookup`` (what the proxy and
        older callers use) must keep today's semantics: an exact repeat first, then the
        closest neighbour only when it reaches the threshold.

    **What it tests:**
      - an exact hit is returned without consulting ``nearest``
      - a neighbour at 0.97 hits at threshold 0.95 and misses at 0.98
    """
    ns = CacheNamespace("ws-1", "m", "v1")
    exact_hit = _answer(1.0)
    backend = _backend(exact_hit, _answer(0.97))

    assert await SemanticCache.lookup(backend, "q", ns, min_similarity=0.95) is exact_hit
    backend.nearest.assert_not_awaited()

    backend = _backend(None, _answer(0.97))
    hit = await SemanticCache.lookup(backend, "q", ns, min_similarity=0.95)
    assert hit is not None
    assert hit.similarity == pytest.approx(0.97)
    assert await SemanticCache.lookup(backend, "q", ns, min_similarity=0.98) is None


def test_tier_contracts_are_abstract() -> None:
    """Test that the tier, lookup.

    Verifier and admission contracts cannot be instantiated.

    **Why this test is important:**
      - Each is a seam a ``Kind`` fills; an instantiable base would let a half-built
        backend slip into the graph and silently serve nothing.

    **What it tests:**
      - instantiating each contract raises ``TypeError``
      - a cached answer records the tier that served it, empty by default
    """
    for contract in (
        CacheCandidates,
        CacheTier,
        CacheLookup,
        CacheVerifier,
        CacheAdmission,
    ):
        with pytest.raises(TypeError):
            contract()  # pyright: ignore[reportAbstractUsage]
    assert _answer(1.0, tier="exact").tier == "exact"
    assert not CachedAnswer(b"a", 1.0, "q", "m").tier
