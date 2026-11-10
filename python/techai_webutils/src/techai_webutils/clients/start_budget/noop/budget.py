"""``NoopStartBudget``: every acquire succeeds at once.

(no account-wide budget, today's behaviour).
"""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.start_budget import Slot, StartBudget
from techai_webutils.foundation.lifecycle import NoOpAsyncResource


class NoopStartBudget(NoOpAsyncResource, StartBudget):
    """Grants slot 0 to every caller immediately and never paces."""

    @override
    async def acquire(self, binding: str) -> Slot | None:
        """Return slot 0 at once.

        Args:
            binding: The account or data source the start counts against.

        Returns:
            Slot 0 at once.

        """
        return Slot(binding=binding, index=0)

    @override
    async def release(self, slot: Slot) -> None:
        """Do nothing; no slot is ever held.

        Args:
            slot: A slot ``acquire`` returned.

        """
