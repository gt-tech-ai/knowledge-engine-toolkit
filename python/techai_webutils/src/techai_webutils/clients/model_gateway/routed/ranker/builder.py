"""Config-selected ``CandidateRanker`` factory (``static_order`` today; ``cost_aware`` follows)."""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, Any

from techai_webutils.clients.model_gateway.routed.ranker.static_order import (
    StaticOrderRanker,
)
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.routing import CandidateRanker


class CandidateRankerKind(StrEnum):
    """The candidate ranker backends."""

    STATIC_ORDER = "static_order"
    """The route's models first, then catalog order (the default)."""


def candidate_ranker_from_config(raw: Mapping[str, Any]) -> CandidateRanker:
    """Build the ranker ``raw["kind"]`` selects (``static_order`` by default).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.

    """
    kind = str(raw.get("kind", CandidateRankerKind.STATIC_ORDER))
    if kind == CandidateRankerKind.STATIC_ORDER:
        return StaticOrderRanker()
    msg = f"unknown candidate ranker kind: {kind!r}"
    raise AppValueError(msg)
