"""Unit tests for the chunk contextualizers.

(``none``, ``header``, ``llm`` with a mocked model).
"""

from __future__ import annotations

from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.chunking.context import (
    ContextualizerConfig,
    ContextualizerKind,
    contextualizer_from_config,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.chunk_contextualizer import ContextDocument
from techai_webutils.core.interfaces.chunker import StructuredChunk
from techai_webutils.core.interfaces.llm import LLMProvider, LLMResponse

_EXPECTED_AWAIT_COUNT = 3

_DOCUMENT = ContextDocument("Annual report 2026", "Revenue grew. Costs fell. " * 10)
_CHUNKS = [
    StructuredChunk("Revenue grew 12.5%.", 0, 19, ("Results", "Q3")),
    StructuredChunk("| Q1 | 10 |\n", 19, 31, ("Costs",), "table", "Table 2: Costs"),
    StructuredChunk("Preface.", 31, 39),
]


@pytest.mark.asyncio
async def test_none_is_the_default_and_header_is_deterministic() -> None:
    """Test that ``none``.

    (the default) leaves chunks alone and ``header`` prepends their place.

    **Why this test is important:**
      - The default must index exactly what is indexed today (no cost, no change);
        ``header`` must give the same text on every run, or re-indexing churns every
        chunk.

    **What it tests:**
      - ``ContextualizerConfig()`` returns the chunk texts unchanged
      - ``header`` prepends ``document > section path`` and a table's caption, and two
        runs agree
    """
    plain = await contextualizer_from_config(ContextualizerConfig()).contextualize(
        _DOCUMENT, _CHUNKS
    )
    header = contextualizer_from_config(
        ContextualizerConfig(kind=ContextualizerKind.HEADER)
    )

    first = await header.contextualize(_DOCUMENT, _CHUNKS)
    second = await header.contextualize(_DOCUMENT, _CHUNKS)

    assert plain == [c.text for c in _CHUNKS]
    assert (
        first
        == second
        == [
            "Annual report 2026 > Results > Q3\n\nRevenue grew 12.5%.",
            "Annual report 2026 > Costs\nTable 2: Costs\n\n| Q1 | 10 |\n",
            "Annual report 2026\n\nPreface.",
        ]
    )
    nameless = await header.contextualize(ContextDocument("", ""), [_CHUNKS[2]])
    assert nameless == ["Preface."]


@pytest.mark.asyncio
async def test_llm_kind_calls_the_model_once_per_chunk_with_the_document() -> None:
    """Test that the ``llm`` kind asks the model once per chunk.

    With a bounded document excerpt.

    **Why this test is important:**
      - Each call is paid; the call count and the excerpt bound are the cost the consumer
        budgets.

    **What it tests:**
      - three chunks make three ``complete`` calls at temperature 0 within
        ``max_context_tokens``
      - each prompt holds the first ``max_document_chars`` of the document and the chunk
      - the model's context leads each chunk's text; an empty reply leaves the chunk
        unchanged
    """
    llm = MagicMock(spec=LLMProvider)
    replies = iter(["Q3 results of the 2026 report.", "", "The preface."])
    llm.complete = AsyncMock(
        side_effect=lambda *_: LLMResponse(next(replies), "m", 1, 1, "stop")
    )
    contextualizer = contextualizer_from_config(
        ContextualizerConfig(
            kind=ContextualizerKind.LLM, max_document_chars=20, max_context_tokens=50
        ),
        llm=cast("LLMProvider", llm),
    )

    texts = await contextualizer.contextualize(_DOCUMENT, _CHUNKS)

    assert llm.complete.await_count == _EXPECTED_AWAIT_COUNT
    messages, config = llm.complete.await_args_list[0].args
    assert (config.temperature, config.max_tokens) == (0.0, 50)
    assert _DOCUMENT.text[:20] + "\n</document>" in messages[1].content
    assert _DOCUMENT.text[:21] not in messages[1].content
    assert "Revenue grew 12.5%." in messages[1].content
    assert texts == [
        "Q3 results of the 2026 report.\n\nRevenue grew 12.5%.",
        "| Q1 | 10 |\n",
        "The preface.\n\nPreface.",
    ]


def test_factory_refuses_what_it_cannot_build() -> None:
    """Test that an unknown kind, or ``llm`` without a model or with a zero limit, fails.

    **Why this test is important:**
      - A paid dial misconfigured must stop the deployable at start-up.

    **What it tests:**
      - ``llm`` without a model, ``llm`` with ``concurrency=0`` and an unknown kind raise
        ``AppValueError``
    """
    llm = cast("LLMProvider", MagicMock(spec=LLMProvider))
    with pytest.raises(AppValueError):
        contextualizer_from_config(ContextualizerConfig(kind=ContextualizerKind.LLM))
    with pytest.raises(AppValueError):
        contextualizer_from_config(
            ContextualizerConfig(kind=ContextualizerKind.LLM, concurrency=0), llm=llm
        )
    with pytest.raises(AppValueError):
        contextualizer_from_config(
            ContextualizerConfig(kind=cast("ContextualizerKind", "summary"))
        )
