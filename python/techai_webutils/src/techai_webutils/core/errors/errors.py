"""Structured error taxonomy matching the Go error package.

Error codes map to identical gRPC status codes in both Go and Python,
ensuring consistent error handling across both languages.
"""

from enum import StrEnum


class ErrorCode(StrEnum):
    """Error codes shared between Go and Python services."""

    NOT_FOUND = "NOT_FOUND"
    """Requested resource does not exist (HTTP 404 / gRPC NOT_FOUND); terminal, not retryable."""
    INVALID_INPUT = "INVALID_INPUT"
    """Request failed validation (HTTP 400 / gRPC INVALID_ARGUMENT); terminal, not retryable."""
    UNAUTHORIZED = "UNAUTHORIZED"
    """Authentication missing or token invalid (HTTP 401 / gRPC UNAUTHENTICATED); terminal."""
    FORBIDDEN = "FORBIDDEN"
    """Authenticated but lacking permission for the action (HTTP 403 / gRPC PERMISSION_DENIED)."""
    CONFLICT = "CONFLICT"
    """Resource already exists or version mismatch (HTTP 409 / gRPC ALREADY_EXISTS); terminal."""
    INTERNAL = "INTERNAL"
    """Unexpected server-side failure (HTTP 500 / gRPC INTERNAL); terminal, not retryable."""
    TIMEOUT = "TIMEOUT"
    """Operation exceeded its deadline (HTTP 504 / gRPC DEADLINE_EXCEEDED); transient, retryable."""
    UNAVAILABLE = "UNAVAILABLE"
    """Dependency temporarily unreachable (HTTP 503 / gRPC UNAVAILABLE); transient, retryable."""
    UNKNOWN = "UNKNOWN"
    """Fallback for errors that are not AppErrors or carry no code (HTTP 500 / gRPC INTERNAL; message hidden at both edges)."""
    CANCELED = "CANCELED"
    """Operation canceled by the caller (HTTP 500 / gRPC CANCELLED)."""
    INGESTION_ERROR = "INGESTION_ERROR"
    """Failure in the document ingestion pipeline (HTTP 500 / gRPC INTERNAL)."""
    QUALITY_FAILED = "QUALITY_FAILED"
    """Quality gate failed (HTTP 500 / gRPC INTERNAL)."""
    UPSTREAM = "UPSTREAM"
    """Failure originating in an upstream dependency (HTTP 502 / gRPC UNAVAILABLE)."""
    RESOURCE_EXHAUSTED = "RESOURCE_EXHAUSTED"
    """A quota or budget is spent (HTTP 429 / gRPC RESOURCE_EXHAUSTED); neither transient nor permanent.

    Retrying inside the quota window only burns another quota check, so it is not transient; the
    request succeeds once the window resets, so it is not permanent either.
    """


# gRPC status code mapping (mirrors Go's Sanitize in go/transport/rpc/errors.go)
_GRPC_STATUS_MAP: dict[ErrorCode, int] = {
    ErrorCode.NOT_FOUND: 5,  # NOT_FOUND
    ErrorCode.INVALID_INPUT: 3,  # INVALID_ARGUMENT
    ErrorCode.UNAUTHORIZED: 16,  # UNAUTHENTICATED
    ErrorCode.FORBIDDEN: 7,  # PERMISSION_DENIED
    ErrorCode.CONFLICT: 6,  # ALREADY_EXISTS
    ErrorCode.INTERNAL: 13,  # INTERNAL
    ErrorCode.TIMEOUT: 4,  # DEADLINE_EXCEEDED
    ErrorCode.UNAVAILABLE: 14,  # UNAVAILABLE
    ErrorCode.UNKNOWN: 13,  # INTERNAL (Go Sanitize maps CodeUnknown to CodeInternal via its default)
    ErrorCode.CANCELED: 1,  # CANCELLED (Go Sanitize)
    ErrorCode.INGESTION_ERROR: 13,  # INTERNAL (Go Sanitize default)
    ErrorCode.QUALITY_FAILED: 13,  # INTERNAL (Go Sanitize default)
    ErrorCode.UPSTREAM: 14,  # UNAVAILABLE (Go Sanitize)
    ErrorCode.RESOURCE_EXHAUSTED: 8,  # RESOURCE_EXHAUSTED (Go Sanitize)
}
"""Maps each ErrorCode to its gRPC status code (mirrors Go's Sanitize in go/transport/rpc/errors.go).

Every ErrorCode is listed explicitly; the ``grpc_status`` fallback below is only a defensive guard
for a future, unmapped code (it returns INTERNAL, mirroring Go's Sanitize default).
"""

# HTTP status code mapping (matches Go's ToHTTPStatus)
_HTTP_STATUS_MAP: dict[ErrorCode, int] = {
    ErrorCode.NOT_FOUND: 404,
    ErrorCode.INVALID_INPUT: 400,
    ErrorCode.UNAUTHORIZED: 401,
    ErrorCode.FORBIDDEN: 403,
    ErrorCode.CONFLICT: 409,
    ErrorCode.INTERNAL: 500,
    ErrorCode.TIMEOUT: 504,
    ErrorCode.UNAVAILABLE: 503,
    ErrorCode.UPSTREAM: 502,  # BadGateway (Go classify.go ToHTTPStatus)
    ErrorCode.RESOURCE_EXHAUSTED: 429,  # TooManyRequests (Go classify.go ToHTTPStatus)
}
"""Maps each ErrorCode to its canonical HTTP status code (matches Go's ToHTTPStatus).

UNKNOWN, CANCELED, INGESTION_ERROR and QUALITY_FAILED ride the 500 default below, matching
Go's classify.go ToHTTPStatus default (StatusInternalServerError).
"""


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
        return _GRPC_STATUS_MAP.get(
            self.code, 13
        )  # INTERNAL (Go Sanitize default; all codes are mapped)

    @property
    def http_status(self) -> int:
        """The corresponding HTTP status code."""
        return _HTTP_STATUS_MAP.get(self.code, 500)

    @property
    def is_transient(self) -> bool:
        """Whether this error may succeed on retry."""
        return self.code in {ErrorCode.TIMEOUT, ErrorCode.UNAVAILABLE}

    @property
    def is_permanent(self) -> bool:
        """Whether this error will not succeed on retry."""
        return self.code in {
            ErrorCode.NOT_FOUND,
            ErrorCode.INVALID_INPUT,
            ErrorCode.UNAUTHORIZED,
            ErrorCode.FORBIDDEN,
            ErrorCode.CONFLICT,
        }

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
