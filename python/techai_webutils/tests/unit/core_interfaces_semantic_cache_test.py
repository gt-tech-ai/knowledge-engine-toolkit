"""Unit tests for the ``SemanticCache`` contract's namespace and answer value types."""

from __future__ import annotations

import dataclasses

import pytest

from techai_webutils.core.interfaces.cache import CacheInvalidator
from techai_webutils.core.interfaces.semantic_cache import (
    CachedAnswer,
    CacheNamespace,
    SemanticCache,
)


def test_cache_namespace_isolation() -> None:
    """Test that namespaces differing in any field never share an identity or a tag.

    **Why this test is important:**
      - The namespace is the tenancy and embedding-space partition; two workspaces, two
        embedding models or two clearance scopes sharing a tag would serve one tenant's
        cached answer to another.

    **What it tests:**
      - changing each of the four fields yields an unequal namespace with a distinct ``tag()``
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
    assert len(base.tag()) == 16
    assert not CacheNamespace("ws-1", "m", "v1").scope


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
