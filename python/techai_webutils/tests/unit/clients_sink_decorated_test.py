"""Unit tests for ``clients.sink.decorate``: a sink run through the client tier."""

from __future__ import annotations

from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.sink import decorate
from techai_webutils.core.interfaces.sink import ChunkPayload, Sink, SinkDocument
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations


@pytest.mark.asyncio
async def test_decorated_sink_delegates_every_call_and_its_lifecycle() -> None:
    """Test that the decorated sink forwards each call and its lifecycle.

    To the wrapped sink.

    **Why this test is important:**
      - The composition root decorates every sink (timeout, retry, breaker, metrics); a
        wrapper that dropped an argument would write chunks to the wrong org or lose their
        vectors, and one that skipped ``__aenter__`` would leave the backend's client
        closed.

    **What it tests:**
      - ``write``, ``delete`` and ``update_metadata`` reach the wrapped sink once each
        with the same arguments, and entering the decorator enters the wrapped sink.
    """
    inner = AsyncMock(spec=Sink)
    inner.__aenter__.return_value = inner
    decorations = Decorations(decoration.default_config(), Collaborators())
    sink = decorate(inner, decorations, "ingestion.sink")
    document = SinkDocument(
        org_id="org-1",
        document_id="doc-1",
        content_hash="h",
        embedding_model="m",
        embedding_version="1",
    )
    chunks = [ChunkPayload(text="hello")]

    async with sink:
        await sink.write(document, chunks, [[0.1, 0.2]])
        await sink.delete("org-1", "doc-1")
        await sink.update_metadata("org-1", "doc-1", {"clearance": "public"})

    inner.__aenter__.assert_awaited_once()
    inner.write.assert_awaited_once_with(document, chunks, [[0.1, 0.2]])
    inner.delete.assert_awaited_once_with("org-1", "doc-1")
    inner.update_metadata.assert_awaited_once_with(
        "org-1", "doc-1", {"clearance": "public"}
    )
