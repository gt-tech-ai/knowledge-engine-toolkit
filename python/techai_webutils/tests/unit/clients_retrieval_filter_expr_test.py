"""Unit tests for the neutral ``FilterExpr`` translations and the extended ``Citation``."""

from __future__ import annotations

import pytest

from techai_webutils.clients.retrieval.bedrock.filters import (
    bedrock_filter,
    expr_filter_builder,
)
from techai_webutils.clients.vector.opensearch.mapping import filter_clauses, term_filters
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.retrieval import (
    Citation,
    FilterAnd,
    FilterEq,
    FilterExpr,
    FilterIn,
    RetrievalResult,
    equals_all,
)
from techai_webutils.pipelines.citations import PassageCitationExtractor

_SCOPE = FilterAnd((FilterEq("org_id", "o1"), FilterIn("level", ("public", "internal"))))


@pytest.mark.parametrize(
    ("expr", "bedrock", "opensearch"),
    [
        (
            FilterEq("org_id", "o1"),
            {"equals": {"key": "org_id", "value": "o1"}},
            [{"term": {"org_id": "o1"}}],
        ),
        (
            FilterIn("level", ("public", "internal")),
            {"in": {"key": "level", "value": ["public", "internal"]}},
            [{"terms": {"level": ["public", "internal"]}}],
        ),
        (
            _SCOPE,
            {
                "andAll": [
                    {"equals": {"key": "org_id", "value": "o1"}},
                    {"in": {"key": "level", "value": ["public", "internal"]}},
                ]
            },
            [{"term": {"org_id": "o1"}}, {"terms": {"level": ["public", "internal"]}}],
        ),
        (
            FilterAnd((FilterEq("org_id", "o1"),)),
            {"equals": {"key": "org_id", "value": "o1"}},
            [{"term": {"org_id": "o1"}}],
        ),
    ],
)
def test_one_filter_translates_to_bedrock_and_opensearch(
    expr: FilterExpr, bedrock: dict[str, object], opensearch: list[dict[str, object]]
) -> None:
    """Test that one neutral filter becomes the matching Bedrock and OpenSearch fragments.

    **Why this test is important:**
      - A scope (org, clearance) is a security boundary; leaving Bedrock must not change which
        passages it admits, so both translations must mean the same filter.

    **What it tests:**
      - ``eq`` → ``equals`` / ``term``; ``in`` → ``in`` / ``terms``; ``and`` → ``andAll`` /
        the ``bool.filter`` list; a one-clause ``and`` is its clause (Bedrock's ``andAll`` needs
        two members)
    """
    assert bedrock_filter(expr) == bedrock
    assert filter_clauses(expr) == opensearch


def test_empty_filters_are_refused_and_builders_adapt() -> None:
    """Test that an empty ``and`` or ``in`` is refused and the adapters keep today's behaviour.

    **Why this test is important:**
      - An empty filter would match nothing on one backend and everything on the other; it is
        refused instead of guessed.

    **What it tests:**
      - an empty ``and`` and an empty ``in`` raise ``AppValueError``; ``term_filters`` of a map
        is unchanged; ``expr_filter_builder`` translates the scope and passes ``None`` through
    """
    with pytest.raises(AppValueError):
        bedrock_filter(FilterAnd(()))
    with pytest.raises(AppValueError):
        bedrock_filter(FilterIn("level", ()))
    with pytest.raises(AppValueError):
        filter_clauses(FilterIn("level", ()))

    builder = expr_filter_builder(
        lambda request: equals_all(request) if request else None
    )

    assert term_filters({"a": "1", "b": "2"}) == [
        {"term": {"a": "1"}},
        {"term": {"b": "2"}},
    ]
    assert builder({"org_id": "o1"}) == {"equals": {"key": "org_id", "value": "o1"}}
    assert builder({}) is None


@pytest.mark.asyncio
async def test_citations_name_their_chunk_and_keep_existing_fields() -> None:
    """Test that a citation carries the passage's chunk id and whole-chunk span, fields unchanged.

    **Why this test is important:**
      - Provenance must point at the chunk on any backend; existing consumers must see the same
        citation fields as before.

    **What it tests:**
      - a passage with ``chunk_id`` metadata yields a citation with that id and ``span`` ``None``;
        one without yields ``""``; a ``Citation`` built the old way defaults both
    """
    with_id = RetrievalResult("d1", "Doc", "text", 0.9, 2, {"chunk_id": "d1:0"})
    without = RetrievalResult("d2", "Doc 2", "text", 0.8, None, {})

    citations = await PassageCitationExtractor().extract("a [1]", [with_id, without])
    legacy = Citation("d1", "Doc", "text", 2, 0.9)

    assert [(c.chunk_id, c.span) for c in citations] == [("d1:0", None), ("", None)]
    assert (citations[0].document_id, citations[0].page_number) == ("d1", 2)
    assert (legacy.chunk_id, legacy.span, legacy.attributes) == ("", None, {})
