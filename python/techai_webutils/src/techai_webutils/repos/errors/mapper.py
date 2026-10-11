"""Map database errors to application errors.

Mirrors Go's ``repos/errors/postgres/mapper.go``.
Handles asyncpg and psycopg error codes.
"""

from __future__ import annotations

from techai_webutils.core.errors.errors import (
    AppError,
    ConflictError,
    InternalError,
    InvalidInputError,
    NotFoundError,
    UnavailableError,
)

_UNIQUE_VIOLATION = "23505"
"""PostgreSQL SQLSTATE for a unique-constraint violation (mapped to ConflictError)."""
_FOREIGN_KEY_VIOLATION = "23503"
"""PostgreSQL SQLSTATE for a foreign-key violation (mapped to InvalidInputError)."""
_NOT_NULL_VIOLATION = "23502"
"""PostgreSQL SQLSTATE for a not-null violation (mapped to InvalidInputError)."""
_CHECK_VIOLATION = "23514"
"""PostgreSQL SQLSTATE for a check-constraint violation (mapped to InvalidInputError)."""


def map_db_error(err: Exception) -> AppError:
    """Map a database exception to the appropriate AppError subclass.

    Recognizes asyncpg and psycopg2/psycopg3 error patterns.
    Unknown errors are wrapped as InternalError.
    """
    err_str = str(err)
    err_class = type(err).__name__

    # Check for PostgreSQL SQLSTATE code in the exception
    sqlstate = _extract_sqlstate(err)

    if sqlstate == _UNIQUE_VIOLATION:
        return ConflictError(f"duplicate record: {err_str}")

    if sqlstate == _FOREIGN_KEY_VIOLATION:
        return InvalidInputError(f"foreign key violation: {err_str}")

    if sqlstate in {_NOT_NULL_VIOLATION, _CHECK_VIOLATION}:
        return InvalidInputError(f"constraint violation: {err_str}")

    # Check for common not-found patterns
    if "no rows" in err_str.lower() or "not found" in err_str.lower():
        return NotFoundError(err_str)

    # Connection errors
    if "connection" in err_class.lower() or "timeout" in err_class.lower():
        return UnavailableError(f"database unavailable: {err_str}")

    return InternalError(f"database error: {err_str}", cause=err)


def _extract_sqlstate(err: Exception) -> str:
    """Try to extract a SQLSTATE code from a database exception."""
    # asyncpg stores it as .sqlstate
    if hasattr(err, "sqlstate"):
        return str(getattr(err, "sqlstate", ""))
    # psycopg2/3 stores it as .pgcode
    if hasattr(err, "pgcode"):
        return str(getattr(err, "pgcode", ""))
    return ""
