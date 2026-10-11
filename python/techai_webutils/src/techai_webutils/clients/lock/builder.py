"""Config-selected single-writer lock factory (the foundation/logger ``NewFromConfig`` pattern).

Selects the in-process ``InMemoryLock`` (single replica) or the cross-pod ``PostgresAdvisoryLock``
from config (``LockConfig.kind``). Unknown kinds fail loudly. The Postgres backend and
its ``asyncpg`` dependency are imported lazily so the dev/memory path never loads asyncpg.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING
from urllib.parse import quote

from techai_webutils.clients.lock.memory import InMemoryLock
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.lock import ManagedLock


class LockKind(StrEnum):
    """Which ``DistributedLock`` backend to build."""

    MEMORY = "memory"
    """In-process lock (dev / tests / single replica); no external dependency."""
    POSTGRES = "postgres"
    """Cross-pod Postgres advisory lock (stage/prod; needs the ``techai-webutils[postgres]`` extra)."""


@dataclass(frozen=True, slots=True)
class LockConfig:
    """Single-writer lock configuration: the backend, its connection, and the lock's identity."""

    kind: LockKind = LockKind.MEMORY
    """Selects the backend (``memory`` in dev / replica=1, ``postgres`` in stage/prod)."""
    host: str = "localhost"
    """Postgres host (unused by the memory backend)."""
    port: int = 5432
    """Postgres port."""
    user: str = ""
    """Postgres user (callers project it from DatabaseSettings; unused by the memory backend)."""
    password: str = ""
    """Postgres password."""
    database: str = ""
    """Postgres database name (callers project it from DatabaseSettings)."""
    sslmode: str = "disable"
    """libpq sslmode for the lock connection (``disable`` in dev; ``require``/``verify-*`` on RDS)."""
    key: str = ""
    """What the lock guards (e.g. one resource id); hashed into the advisory-lock key."""
    namespace: int | None = None
    """Signed-int32 namespace for the advisory lock, separating this lock class from others. Required
    for the postgres kind (it is half of the lock identity, so it has no default); unused by memory."""
    application_name: str = "advisory-lock"
    """Postgres ``application_name`` tagging the lock session in ``pg_stat_activity`` (postgres only)."""


def _postgres_dsn(config: LockConfig) -> str:
    """Build a libpq DSN URI for the lock connection.

    asyncpg has no ``sslmode`` kwarg but parses it from the DSN URI, so TLS travels in the query
    string; credentials are percent-encoded to survive special characters.
    """
    user = quote(config.user, safe="")
    password = quote(config.password, safe="")
    database = quote(config.database, safe="")
    return f"postgresql://{user}:{password}@{config.host}:{config.port}/{database}?sslmode={config.sslmode}"


def new_lock_from_config(config: LockConfig) -> ManagedLock:
    """Build the ``DistributedLock`` backend selected by ``config.kind`` (memory or postgres).

    The postgres kind requires ``config.namespace`` (``ValueError`` when unset) and a non-empty
    ``config.key``: both make up the advisory-lock identity, so neither may silently default.
    """
    if config.kind is LockKind.MEMORY:
        return InMemoryLock()
    if config.kind is LockKind.POSTGRES:
        if config.namespace is None:
            msg = "postgres lock kind requires LockConfig.namespace (it is half of the lock identity)"
            raise AppValueError(msg)
        # Lazy import: keep asyncpg off the dev/memory path (loaded only in stage/prod).
        from techai_webutils.clients.lock.postgres import PostgresAdvisoryLock  # noqa: PLC0415

        return PostgresAdvisoryLock(
            dsn=_postgres_dsn(config),
            key=config.key,
            namespace=config.namespace,
            application_name=config.application_name,
        )
    msg = f"unknown lock kind: {config.kind!r}"
    raise AppValueError(msg)
