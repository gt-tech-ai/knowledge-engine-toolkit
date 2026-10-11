"""Tests for DB error -> AppError classification."""

from techai_webutils.core.errors.errors import (
    ConflictError,
    InternalError,
    InvalidInputError,
    NotFoundError,
    UnavailableError,
)
from techai_webutils.repos.errors.mapper import map_db_error


class TestMapDbError:
    """Test suite for database exception to application error classification."""

    def test_unique_violation(self) -> None:
        """Test that a unique constraint violation maps to ConflictError.

        **Why this test is important:**
          - Unique violations are the most common database constraint error in multi-tenant systems
          - Incorrect mapping would surface cryptic DB errors instead of user-friendly conflict messages
          - ConflictError triggers HTTP 409, allowing clients to implement retry or merge logic

        **What it tests:**
          - PostgreSQL sqlstate 23505 produces ConflictError
        """
        err = _make_pg_error("duplicate key", sqlstate="23505")
        result = map_db_error(err)
        assert isinstance(result, ConflictError)

    def test_foreign_key_violation(self) -> None:
        """Test that a foreign key violation maps to InvalidInputError.

        **Why this test is important:**
          - Foreign key violations indicate the client referenced a nonexistent parent entity
          - Mapping to InvalidInputError tells clients to fix their input, not retry
          - Incorrect mapping would mislead clients into thinking it is a server error

        **What it tests:**
          - PostgreSQL sqlstate 23503 produces InvalidInputError
        """
        err = _make_pg_error("fk violation", sqlstate="23503")
        result = map_db_error(err)
        assert isinstance(result, InvalidInputError)

    def test_not_null_violation(self) -> None:
        """Test that a not-null violation maps to InvalidInputError.

        **Why this test is important:**
          - Not-null violations mean a required field was omitted from the client request
          - Correct classification as InvalidInputError triggers HTTP 400 for clear client feedback
          - Misclassification would hide input validation failures behind server errors

        **What it tests:**
          - PostgreSQL sqlstate 23502 produces InvalidInputError
        """
        err = _make_pg_error("null value", sqlstate="23502")
        result = map_db_error(err)
        assert isinstance(result, InvalidInputError)

    def test_check_violation(self) -> None:
        """Test that a check constraint violation maps to InvalidInputError.

        **Why this test is important:**
          - Check constraints enforce domain invariants at the database level
          - Surfacing these as InvalidInputError gives clients actionable feedback
          - Correct mapping ensures the error hierarchy remains consistent across all constraint types

        **What it tests:**
          - PostgreSQL sqlstate 23514 produces InvalidInputError
        """
        err = _make_pg_error("check failed", sqlstate="23514")
        result = map_db_error(err)
        assert isinstance(result, InvalidInputError)

    def test_no_rows_message(self) -> None:
        """Test that a 'no rows' error message maps to NotFoundError.

        **Why this test is important:**
          - Some database drivers report missing rows via exception messages rather than sqlstate
          - The mapper must handle message-based detection for cross-driver compatibility
          - NotFoundError mapping is critical for correct HTTP 404 responses

        **What it tests:**
          - Exception with 'no rows in result set' produces NotFoundError
        """
        err = Exception("no rows in result set")
        result = map_db_error(err)
        assert isinstance(result, NotFoundError)

    def test_not_found_message(self) -> None:
        """Test that a 'record not found' error message maps to NotFoundError.

        **Why this test is important:**
          - ORM libraries like Ent use 'record not found' as their standard not-found message
          - The mapper must recognize this pattern for ORM compatibility
          - Consistent NotFoundError mapping ensures uniform API behavior regardless of data access method

        **What it tests:**
          - Exception with 'record not found' produces NotFoundError
        """
        err = Exception("record not found")
        result = map_db_error(err)
        assert isinstance(result, NotFoundError)

    def test_connection_error(self) -> None:
        """Test that a connection failure maps to UnavailableError.

        **Why this test is important:**
          - Connection failures indicate infrastructure problems, not client errors
          - UnavailableError triggers HTTP 503, signaling clients to retry with backoff
          - Incorrect mapping would cause clients to treat transient failures as permanent

        **What it tests:**
          - ConnectionError produces UnavailableError
        """
        err = ConnectionError("connection refused")
        result = map_db_error(err)
        assert isinstance(result, UnavailableError)

    def test_unknown_error(self) -> None:
        """Test that unrecognized errors map to InternalError with cause preserved.

        **Why this test is important:**
          - Unknown errors must be caught to prevent unhandled exceptions from crashing the service
          - Preserving the original cause enables debugging while presenting a safe error to clients
          - InternalError mapping ensures HTTP 500 with no sensitive details leaked

        **What it tests:**
          - RuntimeError produces InternalError
          - Original exception is set as the cause
        """
        err = RuntimeError("something unexpected")
        result = map_db_error(err)
        assert isinstance(result, InternalError)
        assert result.cause is err

    def test_pgcode_attribute(self) -> None:
        """Test that the mapper recognizes pgcode as an alternative to sqlstate.

        **Why this test is important:**
          - Different PostgreSQL drivers expose error codes via different attribute names
          - psycopg2 uses 'pgcode' while asyncpg uses 'sqlstate'
          - The mapper must support both to work with any Python PostgreSQL driver

        **What it tests:**
          - Exception with pgcode='23505' produces ConflictError
        """
        err = _make_pg_error("duplicate", pgcode="23505")
        result = map_db_error(err)
        assert isinstance(result, ConflictError)


def _make_pg_error(msg: str, sqlstate: str = "", pgcode: str = "") -> Exception:
    """Create an exception with sqlstate or pgcode attribute."""
    err = Exception(msg)
    if sqlstate:
        err.sqlstate = sqlstate  # type: ignore[attr-defined]
    if pgcode:
        err.pgcode = pgcode  # type: ignore[attr-defined]
    return err
