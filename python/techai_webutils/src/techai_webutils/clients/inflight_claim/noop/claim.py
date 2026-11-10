"""``NoopInflightClaim``: every claim is granted (today's behaviour, no shared state)."""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.inflight_claim import InflightClaim


class NoopInflightClaim(InflightClaim):
    """Grants every claim, so concurrent duplicates all run, as without a claim."""

    @override
    async def claim(self, key: str, ttl_seconds: float) -> bool:
        """Grant the claim.

        Args:
            key: The claim key (see ``inflight_key``).
            ttl_seconds: How long the claim holds before another caller may take it.

        Returns:
            ``True``, always: the stub grants every claim.

        """
        return True

    @override
    async def release(self, key: str) -> None:
        """Nothing is held, so nothing is released.

        Args:
            key: The claim key this caller holds.

        """

    @override
    async def aclose(self) -> None:
        """Nothing is owned, so nothing is closed."""
