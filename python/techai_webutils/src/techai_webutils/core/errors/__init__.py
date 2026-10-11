"""Domain error types for the Tech AI Knowledge Engine."""

from techai_webutils.core.errors.errors import (
    AppError,
    AppFileNotFoundError,
    AppRuntimeError,
    AppTimeoutError,
    AppTypeError,
    AppValueError,
    ConflictError,
    ErrorCode,
    ForbiddenError,
    IngestionError,
    InternalError,
    InvalidInputError,
    NotFoundError,
    QuotaExceededError,
    UnauthorizedError,
    UnavailableError,
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
