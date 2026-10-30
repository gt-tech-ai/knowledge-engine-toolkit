"""Application errors over the shared error-code taxonomy.

``ErrorCode`` and its gRPC, HTTP and retry facts are rendered from
``taxonomy/error-codes.yaml`` into ``codes.py``, the same source as Go's
``go/core/errors/codes.go``, so both languages classify every code alike.
"""

from techai_webutils.core.errors.codes import (
    GRPC_STATUS,
    HTTP_STATUS,
    PERMANENT,
    TRANSIENT,
    ErrorCode,
)

__all__ = [
    "AppError",
    "AppFileNotFoundError",
    "AppRuntimeError",
    "AppTimeoutError",
    "AppTypeError",
    "AppValueError",
    "ConflictError",
    "ErrorCode",
    "ForbiddenError",
    "IngestionError",
    "InternalError",
    "InvalidInputError",
    "NotFoundError",
    "QuotaExceededError",
    "UnauthorizedError",
    "UnavailableError",
]

_GRPC_INTERNAL = 13
"""gRPC INTERNAL, the status of a code the taxonomy does not declare."""

_HTTP_INTERNAL = 500
"""HTTP 500, the status of a code the taxonomy does not declare."""


class AppError(Exception):
    """Base application error with structured error code and details."""

    def __init__(
        self,
        code: ErrorCode,
        message: str,
        details: dict[str, str] | None = None,
        cause: Exception | None = None,
    ) -> None:
        """Initialize the error with a code, message, optional details and cause."""
        super().__init__(message)
        self.code = code
        self.message = message
        self.details = details or {}
        self.cause = cause

    @property
    def grpc_status(self) -> int:
        """The corresponding gRPC status code."""
        return GRPC_STATUS.get(self.code, _GRPC_INTERNAL)

    @property
    def http_status(self) -> int:
        """The corresponding HTTP status code."""
        return HTTP_STATUS.get(self.code, _HTTP_INTERNAL)

    @property
    def is_transient(self) -> bool:
        """Whether this error may succeed on retry."""
        return self.code in TRANSIENT

    @property
    def is_permanent(self) -> bool:
        """Whether this error will not succeed on retry."""
        return self.code in PERMANENT

    def __repr__(self) -> str:
        """Return an unambiguous representation including the code and message."""
        return f"AppError(code={self.code.value}, message={self.message!r})"


# Concrete error subclasses for convenience


class NotFoundError(AppError):
    """Resource not found."""

    def __init__(self, message: str, **kwargs: str) -> None:
        """Initialize with a message; extra keyword args become error details."""
        super().__init__(ErrorCode.NOT_FOUND, message, details=kwargs)


class InvalidInputError(AppError):
    """Invalid input or validation failure."""

    def __init__(self, message: str, **kwargs: str) -> None:
        """Initialize with a message; extra keyword args become error details."""
        super().__init__(ErrorCode.INVALID_INPUT, message, details=kwargs)


class UnauthorizedError(AppError):
    """Authentication required or token invalid."""

    def __init__(self, message: str = "authentication required") -> None:
        """Initialize with an optional message describing the auth failure."""
        super().__init__(ErrorCode.UNAUTHORIZED, message)


class ForbiddenError(AppError):
    """Insufficient permissions."""

    def __init__(self, message: str = "insufficient permissions") -> None:
        """Initialize with an optional message describing the denied action."""
        super().__init__(ErrorCode.FORBIDDEN, message)


class ConflictError(AppError):
    """Resource conflict (duplicate, version mismatch, etc.)."""

    def __init__(self, message: str, **kwargs: str) -> None:
        """Initialize with a message; extra keyword args become error details."""
        super().__init__(ErrorCode.CONFLICT, message, details=kwargs)


class InternalError(AppError):
    """Internal server error."""

    def __init__(
        self, message: str = "internal error", cause: Exception | None = None
    ) -> None:
        """Initialize with an optional message and underlying cause exception."""
        super().__init__(ErrorCode.INTERNAL, message, cause=cause)


class AppTimeoutError(AppError):
    """Operation timed out."""

    def __init__(self, message: str = "operation timed out") -> None:
        """Initialize with an optional message describing the timed-out operation."""
        super().__init__(ErrorCode.TIMEOUT, message)


class UnavailableError(AppError):
    """Service unavailable."""

    def __init__(self, message: str = "service unavailable") -> None:
        """Initialize with an optional message describing the unavailable service."""
        super().__init__(ErrorCode.UNAVAILABLE, message)


class QuotaExceededError(AppError):
    """A quota or budget is spent for an organization (``RESOURCE_EXHAUSTED``).

    It is neither transient nor permanent, so the resiliency stack never retries it; the caller backs
    off until the quota window resets.
    """

    def __init__(self, message: str, *, org_id: str, reason: str) -> None:
        """Initialize with a message, the organization whose quota is spent and a machine reason."""
        super().__init__(
            ErrorCode.RESOURCE_EXHAUSTED,
            message,
            details={"org_id": org_id, "reason": reason},
        )


# Coded errors that are also a builtin exception. Production code raises these instead of a bare
# builtin, so the transport edge can map the code, while callers that catch the builtin
# (``except ValueError``) keep working.


class AppValueError(InvalidInputError, ValueError):
    """An invalid value (``INVALID_INPUT``) that is also a ``ValueError``.

    Raised for unknown ``Kind`` strings, out-of-range settings and malformed arguments.
    """


class AppTypeError(InvalidInputError, TypeError):
    """An argument of the wrong type (``INVALID_INPUT``) that is also a ``TypeError``."""


class AppRuntimeError(InternalError, RuntimeError):
    """A misuse of an object's lifecycle (``INTERNAL``) that is also a ``RuntimeError``.

    Raised, for example, when a client is used before its async context manager has opened it.
    """


class AppFileNotFoundError(NotFoundError, FileNotFoundError):
    """A required file is missing (``NOT_FOUND``); also a ``FileNotFoundError``."""


# Ingestion-specific errors


class IngestionError(AppError):
    """Error during document ingestion."""

    def __init__(
        self,
        message: str,
        document_id: str | None = None,
        stage: str | None = None,
        cause: Exception | None = None,
    ) -> None:
        """Initialize with a message and optional document id, stage and cause.

        The ``document_id`` and ``stage`` values, when provided, are recorded
        in the error details to identify which document and pipeline stage
        failed.
        """
        details: dict[str, str] = {}
        if document_id:
            details["document_id"] = document_id
        if stage:
            details["stage"] = stage
        super().__init__(ErrorCode.INGESTION_ERROR, message, details=details, cause=cause)
