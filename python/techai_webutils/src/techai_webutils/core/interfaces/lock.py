"""Single-writer distributed-lock port.

When a resource tolerates only one writer at a time (e.g. a backend that allows one running job per
index), only one worker may drive it. ``acquire()`` returns whether the caller won the lock; only the
winner calls ``release()`` (a loser must never free the holder's lock). Implementations live in
``clients/lock/`` (``InMemoryLock`` for single-process / local; ``PostgresAdvisoryLock`` for cross-pod
stage/prod).
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, Self, runtime_checkable

if TYPE_CHECKING:
    from types import TracebackType


@runtime_checkable
class DistributedLock(Protocol):
    """A single-writer lock across workers/pods.

    Guarantee strength depends on the backend: ``InMemoryLock`` is single-process only, while
    ``PostgresAdvisoryLock`` provides true cross-pod mutual exclusion.
    """

    async def acquire(self) -> bool:
        """Try to acquire the lock; return True iff this caller now holds it."""
        ...

    async def release(self) -> None:
        """Release a lock this caller previously acquired."""
        ...


@runtime_checkable
class ManagedLock(DistributedLock, Protocol):
    """A ``DistributedLock`` that also owns a resource lifecycle as an async context manager.

    Connection-owning backends (e.g. the Postgres advisory lock) open and close their session via the
    async context-manager protocol so the composition root can manage the resource with an
    ``AsyncExitStack``; ``new_lock_from_config`` returns this type so the wiring is uniform across backends
    (the in-memory lock's context methods are no-ops).
    """

    async def __aenter__(self) -> Self:
        """Enter the async context; return the lock."""
        ...

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> bool | None:
        """Exit the async context, releasing any owned resource."""
        ...
