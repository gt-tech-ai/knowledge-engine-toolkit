"""``NearDuplicateDetector``: which earlier document a text nearly repeats.

A detector places each document it checks in a cluster: the cluster of the first earlier
document of the same scope whose text is similar above the detector's threshold (its
representative), or a new cluster the document represents. Exact copies are not its job
(a consumer dedups byte-identical documents by content hash before it gets here); a
near-duplicate is the same text re-exported, re-stamped or lightly edited.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class NearDuplicate:
    """Where one checked document sits: its cluster and that cluster's representative."""

    document_id: str
    """The checked document."""
    cluster_id: str
    """The cluster's id: its representative's document id."""
    representative_ref: str = ""
    """The opaque reference the representative was checked with (e.g. its content
    hash)."""
    similarity: float = 1.0
    """The checked document's estimated Jaccard similarity to the representative (1.0 for
    itself)."""
    signature: tuple[int, ...] = ()
    """The checked document's signature, for the caller to keep beside its output."""

    @property
    def is_member(self) -> bool:
        """True when another document represents the cluster.

        (this one nearly repeats it).
        """
        return self.cluster_id != self.document_id


class NearDuplicateDetector(ABC):
    """Places documents in near-duplicate clusters, per scope."""

    @abstractmethod
    async def check(
        self, scope: str, document_id: str, text: str, *, ref: str = ""
    ) -> NearDuplicate | None:
        """Return ``document_id``'s cluster within ``scope``.

        Recording the document for later checks.

        Checking the same document again returns the cluster it was placed in, so a retry
        never moves it.

        Args:
            scope: The partition documents are compared within (e.g. an organization).
            document_id: The document.
            text: The document's text.
            ref: An opaque reference stored with the document and returned to the members
                of its cluster (``NearDuplicate.representative_ref``).

        Returns: The cluster, or None when detection is off.

        Returns:
            The cluster ``document_id`` belongs to, or ``None`` when it has no near
                duplicate in ``scope``.

        """
