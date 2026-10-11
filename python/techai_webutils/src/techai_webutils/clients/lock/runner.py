"""The lock-guarded single-writer runner decorator.

``SingleWriterRunner`` runs any async operation under a ``DistributedLock`` and skips (returns
``None``) when another writer holds it — the cross-cutting concern, decoupled from what is being run
(mirrors the Go decorator stacks). Moved verbatim from the former single-module ``clients/lock.py``
when the package graduated to the client-package shape.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.lock import DistributedLock


class SingleWriterRunner[T]:
    """Runs an async operation under a single-writer lock; skips (``None``) if held elsewhere.

    The generic lock decorator: ``acquire → run → release``, returning the operation's result or
    ``None`` when the lock is lost this round. Decoupled from the operation (a thunk) so the same
    guard serves any single-writer job.
    """

    def __init__(self, run: Callable[[], Awaitable[T]], lock: DistributedLock) -> None:
        """Bind the operation thunk and the lock that guards it."""
        self._run = run
        self._lock = lock

    async def run(self) -> T | None:
        """Acquire the lock, run the operation, release; return None if another writer holds it."""
        if not await self._lock.acquire():
            return None
        try:
            return await self._run()
        finally:
            await self._lock.release()
