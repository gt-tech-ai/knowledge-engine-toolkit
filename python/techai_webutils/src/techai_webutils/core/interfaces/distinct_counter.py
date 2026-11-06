"""``DistinctCounter``: how many distinct members each key has seen.

A consumer counts, per key, the distinct members it was observed with: how many documents of an
org carried a block of text, for example. Observing the same member again does not raise the
count, so a redelivered message counts once. Counts may be estimates (a probabilistic backend)
and expire with the counter's retention, so a key nobody observes stops costing memory.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Sequence


class DistinctCounter(ABC):
    """Counts the distinct members observed per key."""

    @abstractmethod
    async def observe(self, keys: Sequence[str], member: str) -> list[int]:
        """Record ``member`` under every key and return each key's distinct count, in order.

        A backend that cannot reach its store returns zeros for the keys it could not count
        (the counts are advisory), and never raises for that.
        """

    @abstractmethod
    async def aclose(self) -> None:
        """Release the counter's resources (a client the factory opened)."""
