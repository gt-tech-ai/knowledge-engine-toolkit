"""Server-side re-validating retrieval decorator (defense-in-depth).

Wraps any ``RetrievalEngine`` and drops passages that fail any of the consumer's ``PassagePolicy``
rules (the contract lives in ``core.interfaces.retrieval``) — regardless of the backend's own
filtering — so a mislabeled or out-of-scope passage cannot leak. Built-in rules:

- ``MinScore``: a relevance floor.
- ``MetadataEquals``: a passage metadata key must equal a request filter (e.g. a tenant scope); a
  request without the filter, or with an empty value, admits nothing (fail closed).
- ``OrdinalCeiling``: a passage label must rank at or below the caller's level on a consumer-supplied
  ladder (e.g. a passage's sensitivity label vs the caller's access level); unknown labels fail closed.

The decorator has no default rules: which scope a passage must match is the consumer's decision,
made explicitly.
"""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.retrieval import PassagePolicy, RetrievalEngine
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.retrieval import RetrievalResult

__all__ = [
    "FilteringRetrievalEngine",
    "MetadataEquals",
    "MinScore",
    "OrdinalCeiling",
    "PassagePolicy",
]

logger = get_logger(__name__)


@dataclass(frozen=True, slots=True)
class MinScore:
    """Admits passages whose relevance score is at least ``floor``."""

    floor: float
    """The lowest admitted score."""

    def admits(self, passage: RetrievalResult, request: Mapping[str, str]) -> bool:  # noqa: ARG002
        """Return True iff the passage scores at or above the floor."""
        return passage.score >= self.floor


@dataclass(frozen=True, slots=True)
class MetadataEquals:
    """Admits passages whose metadata ``key`` equals the request's ``request_key`` (default: ``key``).

    A request without the key, or with an empty value, admits nothing (fail closed), so a missing or
    blank scope never widens results — not even to passages stamped with an empty value.
    """

    key: str
    """The passage metadata key compared."""
    request_key: str = ""
    """The request key holding the required value; empty means ``key``."""

    def admits(self, passage: RetrievalResult, request: Mapping[str, str]) -> bool:
        """Return True iff the passage's metadata value equals the request's."""
        wanted = request.get(self.request_key or self.key)
        return bool(wanted) and passage.metadata.get(self.key) == wanted


@dataclass(frozen=True, slots=True)
class OrdinalCeiling:
    """Admits passages whose ``key`` label ranks at or below the caller's ``request_key`` label.

    ``ranks`` orders the labels from least to most sensitive and must not be empty (an empty ladder
    would rank every label and level alike, admitting everything). Both directions fail closed: an
    unknown or missing passage label ranks above every level (denied to everyone), and an unknown or
    missing caller level ranks as the lowest.
    """

    key: str
    """The passage metadata key holding its label."""
    request_key: str
    """The request key holding the caller's level."""
    ranks: tuple[str, ...]
    """The labels in increasing sensitivity."""
    _order: dict[str, int] = field(init=False, repr=False, compare=False)
    """Label → rank, built once from ``ranks``."""

    def __post_init__(self) -> None:
        """Reject an empty ladder, then index ``ranks`` once so ``admits`` is a dict lookup per passage."""
        if not self.ranks:
            msg = "OrdinalCeiling requires at least one label in ranks (an empty ladder admits everything)"
            raise AppValueError(msg)
        object.__setattr__(
            self, "_order", {label: i for i, label in enumerate(self.ranks)}
        )

    def admits(self, passage: RetrievalResult, request: Mapping[str, str]) -> bool:
        """Return True iff the passage's label ranks at or below the caller's level."""
        passage_rank = self._order.get(
            passage.metadata.get(self.key, ""), len(self.ranks)
        )
        return passage_rank <= self._order.get(request.get(self.request_key, ""), 0)


class FilteringRetrievalEngine(DelegatingAsyncResource[RetrievalEngine], RetrievalEngine):
    """RetrievalEngine decorator that keeps only the passages every policy admits."""

    def __init__(self, inner: RetrievalEngine, policies: Sequence[PassagePolicy]) -> None:
        """Wrap ``inner`` with ``policies`` (all must admit a passage; an empty list admits all)."""
        self._inner = inner
        self._policies = list(policies)

    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        index_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve from the inner engine, then re-validate every passage against the policies.

        ``index_id`` is forwarded to the inner engine unchanged; the policies see the call's filters,
        whichever index was queried.
        """
        results = await self._inner.retrieve(
            query, top_k=top_k, filters=filters, index_id=index_id
        )
        request = dict(filters or {})
        kept: list[RetrievalResult] = []
        dropped_by: Counter[str] = Counter()
        for result in results:
            rejecting = next(
                (p for p in self._policies if not p.admits(result, request)), None
            )
            if rejecting is None:
                kept.append(result)
            else:
                dropped_by[type(rejecting).__name__] += 1
        # Observability: a 0-result response is otherwise indistinguishable from an error. The
        # retrieved-vs-kept counts separate "nothing retrieved" from "all filtered out", and dropped_by
        # (each drop attributed to the first policy that rejected it) tells a score floor from a scope
        # mismatch or a missing label.
        logger.info(
            "retrieval passages filtered",
            index_id=index_id,
            retrieved=len(results),
            kept=len(kept),
            dropped=len(results) - len(kept),
            dropped_by=dict(dropped_by),
        )
        return kept
