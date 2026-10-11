"""In-process single-writer lock (tests / single-replica local dev)."""

from __future__ import annotations

from typing import TYPE_CHECKING, Self

if TYPE_CHECKING:
    from types import TracebackType


class InMemoryLock:
    """Single-process ``DistributedLock`` (tests / single-replica local dev).

    The check-then-set in ``acquire`` is atomic only under a single asyncio event loop (no ``await``
    between the read and the write), so this is single-process / single-loop only — it gives no
    guarantee across threads or pods, which is precisely why stage/prod use ``PostgresAdvisoryLock``.
    Also an async context manager (a no-op resource) so it composes uniformly with
    ``AsyncExitStack.enter_async_context`` alongside the connection-owning ``PostgresAdvisoryLock``.
    """

    def __init__(self) -> None:
        """Start unheld."""
        self._held = False

    async def acquire(self) -> bool:
        """Acquire the lock unless already held in this process."""
        if self._held:
            return False
        self._held = True
        return True

    async def release(self) -> None:
        """Release the lock."""
        self._held = False

    async def __aenter__(self) -> Self:
        """Enter the async context (no resource to open); return self."""
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Exit the async context (nothing to release)."""
