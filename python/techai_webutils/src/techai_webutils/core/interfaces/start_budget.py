"""``StartBudget``.

An account-wide cap on concurrent Bedrock ingestion jobs and their start rate.

Bedrock limits how many ingestion jobs an account runs at once and how fast they start.
Every worker that starts a job takes a slot first and holds it for the life of the job,
releasing it once the job is terminal; a worker that reattaches to a running job takes a
slot again. ``acquire`` also paces starts, so two starts in the account are at least the
configured interval apart.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass

from techai_webutils.core.interfaces.lifecycle import ManagedResource


@dataclass(frozen=True, slots=True)
class Slot:
    """One held start slot."""

    binding: str
    """What holds the slot (e.g. ``<knowledge base>:<data source>``), for logs and
    diagnosis."""
    index: int
    """The slot's number, ``0 … slots-1``."""


class StartBudget(ManagedResource, ABC):
    """Hands out the account's job slots and paces job starts."""

    @abstractmethod
    async def acquire(self, binding: str) -> Slot | None:
        """Wait for a free slot and the start interval.

        Return ``None`` when the wait times out.
        """
        ...

    @abstractmethod
    async def release(self, slot: Slot) -> None:
        """Free ``slot`` once its job is terminal.

        Releasing a slot this caller does not hold is a no-op.
        """
        ...
