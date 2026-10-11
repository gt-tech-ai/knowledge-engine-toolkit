"""Postgres advisory-lock backend (cross-pod single-writer; stage/prod)."""

from techai_webutils.clients.lock.postgres.lock import PostgresAdvisoryLock

__all__ = ["PostgresAdvisoryLock"]
