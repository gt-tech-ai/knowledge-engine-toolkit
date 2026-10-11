"""Extended error framework with classification, gRPC/HTTP mapping, and ExceptionGroup support.

Builds on top of python/techai_webutils/src/techai_webutils/core/errors to add:
- Error classification (transient/permanent/internal/unknown)
- Standalone gRPC/HTTP status mapping functions
- IngestionErrors ExceptionGroup (supports except* syntax)
- MultiError aggregation
"""

from __future__ import annotations

from typing import Self

from techai_webutils.core.errors.errors import AppError, ErrorCode, IngestionError


def classify_error(err: BaseException) -> str:
    """Classify an error as transient, permanent, internal, or unknown.

    The transient and permanent sets mirror Go's ``IsTransient`` / ``IsPermanent`` in
    ``go/core/errors/classify.go``. Go classifies none of the remaining codes, so the
    server-side-failure codes (INTERNAL and the pipeline/dependency codes) are grouped as
    "internal" and the rest fall through to "unknown".

    Args:
        err: The exception to classify.

    Returns:
        One of: "transient", "permanent", "internal", "unknown".

    """
    if not isinstance(err, AppError):
        return "unknown"

    if err.code in {ErrorCode.TIMEOUT, ErrorCode.UNAVAILABLE}:
        return "transient"
    if err.code in {
        ErrorCode.NOT_FOUND,
        ErrorCode.INVALID_INPUT,
        ErrorCode.UNAUTHORIZED,
        ErrorCode.FORBIDDEN,
        ErrorCode.CONFLICT,
    }:
        return "permanent"
    # Server-side failures: the base internal error, the two pipeline-failure codes, and an
    # upstream-dependency failure. UPSTREAM is intentionally NOT transient -- Go's classify.go
    # IsTransient omits it, so it is surfaced for investigation rather than retried, even though
    # it maps to gRPC UNAVAILABLE on the wire.
    if err.code in {
        ErrorCode.INTERNAL,
        ErrorCode.INGESTION_ERROR,
        ErrorCode.QUALITY_FAILED,
        ErrorCode.UPSTREAM,
    }:
        return "internal"
    # UNKNOWN, CANCELED and RESOURCE_EXHAUSTED are classified neither transient nor permanent in
    # Go, so they fall through to the catch-all category (a spent quota is not retried, and it is
    # not a server-side failure either).
    return "unknown"


def to_grpc_status(err: BaseException) -> int:
    """Map an error to its gRPC status code.

    Args:
        err: The exception to map.

    Returns:
        gRPC status code integer. Returns 13 (INTERNAL) for a non-AppError.

    """
    if isinstance(err, AppError):
        return err.grpc_status
    # A plain error has no code; Go's Sanitize maps Code(err)=CodeUnknown through its default to
    # connect.CodeInternal, so a non-AppError sanitizes to INTERNAL (not UNKNOWN) at the edge.
    return 13  # INTERNAL


def to_http_status(err: BaseException) -> int:
    """Map an error to its HTTP status code.

    Args:
        err: The exception to map.

    Returns:
        HTTP status code integer. Returns 500 for non-AppError.

    """
    if isinstance(err, AppError):
        return err.http_status
    return 500


class IngestionErrors(ExceptionGroup[IngestionError]):
    """ExceptionGroup for batch ingestion failures.

    Collects per-document errors during ingestion pipeline processing.
    Compatible with Python 3.11+ except* syntax.
    """

    def __new__(cls, message: str, errors: list[IngestionError]) -> Self:
        """Create a new ``IngestionErrors`` exception group.

        Args:
            message: Human-readable summary of the batch failure.
            errors: List of per-document ``IngestionError`` exceptions.

        """
        return super().__new__(cls, message, errors)

    def __init__(self, message: str, errors: list[IngestionError]) -> None:
        """Initialize the group with the same arguments ``__new__`` received."""
        super().__init__(message, errors)

    def derive(self, excs: list[BaseException]) -> IngestionErrors:  # type: ignore[override]
        """Derive a new ``IngestionErrors`` group from a subset of exceptions.

        Required by ``ExceptionGroup`` to support ``except*`` filtering,
        which creates subgroups that preserve the original group type.
        """
        return IngestionErrors(self.message, excs)  # type: ignore[arg-type]


class MultiError:
    """Aggregates multiple errors into one."""

    def __init__(self, errors: list[BaseException]) -> None:
        """Initialize with a list of exceptions to aggregate.

        Args:
            errors: The individual exceptions collected during an operation.

        """
        self.errors = errors

    def __str__(self) -> str:
        """Return a summary string showing the count and messages of all contained errors."""
        return f"{len(self.errors)} errors: {[str(e) for e in self.errors]}"

    def __repr__(self) -> str:
        """Return a compact debug representation showing only the error count."""
        return f"MultiError({len(self.errors)} errors)"
