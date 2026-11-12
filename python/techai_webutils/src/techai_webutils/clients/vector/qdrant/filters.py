"""Translate a neutral ``FilterExpr`` into Qdrant payload conditions.

``eq`` → ``MatchValue``, ``in`` → ``MatchAny`` (on a list payload it matches when any
element is one of the values), and ``and`` contributes each of its clauses, since a
``Filter.must`` list is already a conjunction.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from qdrant_client import models

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.retrieval import FilterEq, FilterIn, equals_all

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.retrieval import FilterExpr


def qdrant_conditions(expr: FilterExpr) -> list[models.FieldCondition]:
    """Return ``expr`` as ``Filter.must`` conditions.

    Args:
        expr: The filter expression.

    Returns:
        One payload condition per ``eq`` or ``in`` leaf, in order.

    Raises:
        AppValueError: An ``in`` with no values (``INVALID_INPUT``).

    """
    if isinstance(expr, FilterEq):
        return [
            models.FieldCondition(key=expr.key, match=models.MatchValue(value=expr.value))
        ]
    if isinstance(expr, FilterIn):
        if not expr.values:
            msg = f"an in filter on {expr.key!r} needs at least one value"
            raise AppValueError(msg)
        return [
            models.FieldCondition(
                key=expr.key, match=models.MatchAny(any=list(expr.values))
            )
        ]
    return [condition for inner in expr.clauses for condition in qdrant_conditions(inner)]


def qdrant_filter(
    filters: Mapping[str, str] | None, where: FilterExpr | None
) -> models.Filter | None:
    """Return the payload filter for exact ``filters`` ANDed with ``where``.

    Args:
        filters: Metadata every result must equal; ``None`` adds nothing.
        where: A further filter expression; ``None`` adds nothing.

    Returns:
        The combined filter, or ``None`` when neither narrows the search; an ``in``
        with no values raises ``AppValueError`` (``INVALID_INPUT``).

    """
    must = qdrant_conditions(equals_all(filters or {}))
    if where is not None:
        must += qdrant_conditions(where)
    return models.Filter(must=list(must)) if must else None
