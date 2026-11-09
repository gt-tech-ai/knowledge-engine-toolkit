"""``bulk_ingest``: hold an index in bulk-ingest mode for one backfill.

Restored however it ends.

A backfill re-index writes far faster with refresh off and no replicas, but an index left
that way serves stale reads and loses data with its node. ``bulk_ingest`` puts the index
into that mode on entry and restores (and verifies) the recorded settings on exit, whether
the backfill finished, raised or was cancelled. It works over a single-domain store and a
multi-domain one alike.
"""

from __future__ import annotations

from contextlib import asynccontextmanager
from typing import TYPE_CHECKING, Protocol

if TYPE_CHECKING:
    from collections.abc import AsyncGenerator


class BulkIngestible(Protocol):
    """A store that can switch one of its indices into bulk-ingest mode and back."""

    async def begin_bulk_ingest(self, index: str) -> None:
        """Turn refresh off and replicas to zero on ``index``.

        Recording the previous settings.
        """
        ...

    async def end_bulk_ingest(self, index: str) -> None:
        """Restore and verify ``index``'s recorded settings.

        A no-op when none are recorded.
        """
        ...


@asynccontextmanager
async def bulk_ingest(store: BulkIngestible, index: str) -> AsyncGenerator[None]:
    """Run the body with ``index`` in bulk-ingest mode; restore it on every exit.

    The restore also runs when entering the mode failed partway, since the store records
    the previous settings before changing them and restoring nothing recorded is a no-op.
    When the body and the restore both fail, the restore's error is raised with the body's
    as its context.

    A callee raises ``AppValueError``: The restored settings did not take (from
    ``end_bulk_ingest``).

    """
    try:
        await store.begin_bulk_ingest(index)
        yield
    finally:
        await store.end_bulk_ingest(index)
