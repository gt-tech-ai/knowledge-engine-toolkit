"""Translate a neutral ``FilterExpr`` into a Bedrock Knowledge Base ``RetrievalFilter``.

``eq`` → ``equals``, ``in`` → ``in``, ``and`` → ``andAll``. Bedrock requires at least two
``andAll`` members, so a one-clause ``and`` is its clause. ``expr_filter_builder`` adapts
a consumer's neutral scope function into the engine's ``filter_builder`` seam.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.retrieval import FilterEq, FilterIn

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

    from techai_webutils.clients.retrieval.bedrock.engine import FilterBuilder
    from techai_webutils.core.interfaces.retrieval import FilterExpr


def bedrock_filter(expr: FilterExpr) -> dict[str, object]:
    """Return ``expr`` as a Bedrock ``RetrievalFilter``.

    Returns:
        ``expr`` as a Bedrock ``RetrievalFilter``.

    Raises:
        AppValueError: An empty ``and`` or ``in`` (``INVALID_INPUT``).

    """
    if isinstance(expr, FilterEq):
        return {"equals": {"key": expr.key, "value": expr.value}}
    if isinstance(expr, FilterIn):
        if not expr.values:
            msg = f"an in filter on {expr.key!r} needs at least one value"
            raise AppValueError(msg)
        return {"in": {"key": expr.key, "value": list(expr.values)}}
    if not expr.clauses:
        msg = "an and filter needs at least one clause"
        raise AppValueError(msg)
    if len(expr.clauses) == 1:
        return bedrock_filter(expr.clauses[0])
    return {"andAll": [bedrock_filter(clause) for clause in expr.clauses]}


def expr_filter_builder(
    build: Callable[[Mapping[str, str]], FilterExpr | None],
) -> FilterBuilder:
    """Return a ``filter_builder`` that translates ``build``'s neutral filter.

    For Bedrock.

    Returns:
        A ``filter_builder`` that translates ``build``'s neutral filter.

    """

    def builder(request: Mapping[str, str]) -> dict[str, object] | None:
        expr = build(request)
        return None if expr is None else bedrock_filter(expr)

    return builder
