"""``InflightClaim``: one consumer at a time pays for a costly stage of one content.

Two consumers that receive the same message at once (a duplicate delivery, or two uploads
of the same bytes) would both pay for parsing, enrichment, embedding or OCR. A consumer
claims the work's key first and runs only when it wins; the claim lapses after
``ttl_seconds`` (the message lease), so a consumer that died while holding it never blocks
the work for long.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


def inflight_key(org_id: str, content_hash: str, stage: str, fingerprint: str) -> str:
    """Return the claim key of one stage of one content within an org.

    Args:
        org_id: The tenant.
        content_hash: The hash of the content being processed.
        stage: The pipeline stage.
        fingerprint: The stage's configuration fingerprint, so a config change is new
            work.

    Returns:
        The claim key of one stage of one content within an org.

    """
    return f"{org_id}:{content_hash}:{stage}:{fingerprint}"


class InflightClaim(ABC):
    """Claims a unit of work so that concurrent consumers run it once."""

    @abstractmethod
    async def claim(self, key: str, ttl_seconds: float) -> bool:
        """Claim ``key`` for ``ttl_seconds``; return whether this caller won it.

        Args:
            key: The claim key (see ``inflight_key``).
            ttl_seconds: How long the claim holds before another caller may take it.

        Returns:
            ``True`` when this caller won the claim.

        """
        ...

    @abstractmethod
    async def release(self, key: str) -> None:
        """Release a claim this caller holds; a claim it does not hold is left alone.

        Args:
            key: The claim key this caller holds.

        """
        ...

    @abstractmethod
    async def aclose(self) -> None:
        """Close what the claim owns (a client it opened itself)."""
        ...
