"""``FactPublisher`` — the port analytics facts leave a process through."""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.lifecycle import ManagedResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.types.fact import Fact


class FactPublisher(ManagedResource, ABC):
    """Publishes analytics facts, best-effort and without blocking the caller.

    Composes ``ManagedResource`` (ARCHITECTURE.md#interface-composition): a buffering backend runs its
    sender inside the async context and drains on exit. ``publish`` never blocks and never raises
    for a delivery problem; a fact that cannot be delivered is dropped and counted.
    """

    @abstractmethod
    def publish(self, facts: Sequence[Fact]) -> None:
        """Hand ``facts`` to the backend for delivery (non-blocking, fail-open)."""
