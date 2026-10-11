"""In-memory lock backend (single-process; dev / tests / replica=1)."""

from techai_webutils.clients.lock.memory.lock import InMemoryLock

__all__ = ["InMemoryLock"]
