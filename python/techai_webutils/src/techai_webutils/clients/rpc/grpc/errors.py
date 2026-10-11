"""Domain error to gRPC status code mapper.

Mirrors Go's ``go/transport/rpc/errors.go`` ``ToConnectError()``.
Maps ``AppError`` codes to gRPC status codes using client-safe messages.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import grpc

from techai_webutils.core.errors.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.logger import Logger

_CODE_MAP: dict[ErrorCode, grpc.StatusCode] = {
    ErrorCode.NOT_FOUND: grpc.StatusCode.NOT_FOUND,
    ErrorCode.INVALID_INPUT: grpc.StatusCode.INVALID_ARGUMENT,
    ErrorCode.CONFLICT: grpc.StatusCode.ALREADY_EXISTS,
    ErrorCode.UNAUTHORIZED: grpc.StatusCode.UNAUTHENTICATED,
    ErrorCode.FORBIDDEN: grpc.StatusCode.PERMISSION_DENIED,
    ErrorCode.TIMEOUT: grpc.StatusCode.DEADLINE_EXCEEDED,
    ErrorCode.UNAVAILABLE: grpc.StatusCode.UNAVAILABLE,
    ErrorCode.INTERNAL: grpc.StatusCode.INTERNAL,
    ErrorCode.CANCELED: grpc.StatusCode.CANCELLED,
    ErrorCode.UPSTREAM: grpc.StatusCode.UNAVAILABLE,
    ErrorCode.RESOURCE_EXHAUSTED: grpc.StatusCode.RESOURCE_EXHAUSTED,
}
"""Maps each domain ``ErrorCode`` to the gRPC status code returned to clients.

UNKNOWN, INGESTION_ERROR and QUALITY_FAILED are not listed; they ride the INTERNAL default in
``to_grpc_status``, matching Go's ``Sanitize`` default (``connect.CodeInternal``).
"""

_FIXED_CLIENT_MESSAGE: dict[ErrorCode, str] = {
    ErrorCode.UPSTREAM: "upstream service unavailable",
    ErrorCode.CANCELED: "request canceled",
    ErrorCode.RESOURCE_EXHAUSTED: "resource exhausted",
}
"""Codes whose client-facing message is a fixed string rather than the raw error text.

These mirror the exact fixed messages Go's ``Sanitize`` returns for ``CodeUpstream``,
``CodeCanceled`` and ``CodeResourceExhausted`` in ``go/transport/rpc/errors.go`` -- the fixed string strips any provider or
implementation detail that must never reach a client (the raw text is still recorded server-side).
"""


def to_grpc_status(
    err: Exception, logger: Logger | None = None
) -> tuple[grpc.StatusCode, str]:
    """Map an exception to a gRPC status code and client-safe message.

    Args:
        err: The exception to map.
        logger: Optional logger for recording internal errors.

    Returns:
        Tuple of (grpc.StatusCode, message).

    """
    if isinstance(err, AppError):
        code = _CODE_MAP.get(err.code, grpc.StatusCode.INTERNAL)
        # For internal errors, hide the real message from clients
        if code == grpc.StatusCode.INTERNAL:
            if logger is not None:
                logger.error("internal error", error=str(err))
            return code, "internal error"
        # Codes that strip provider text return a fixed client-safe message (Go Sanitize parity).
        fixed = _FIXED_CLIENT_MESSAGE.get(err.code)
        if fixed is not None:
            return code, fixed
        return code, err.message

    # Unknown exception — always internal
    if logger is not None:
        logger.error("unhandled error", error=str(err), type=type(err).__name__)
    return grpc.StatusCode.INTERNAL, "internal error"


def abort_with_error(
    context: grpc.ServicerContext,
    err: Exception,
    logger: Logger | None = None,
) -> None:
    """Set gRPC error status on the servicer context and abort.

    Convenience wrapper combining ``to_grpc_status`` with ``context.abort``.
    """
    code, message = to_grpc_status(err, logger)
    context.abort(code, message)
