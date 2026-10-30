"""Domain error to gRPC status code mapper.

Maps ``AppError`` codes to gRPC status codes and client-safe messages from the error-code
taxonomy (``core/errors/codes.py``), the same source as Go's ``clients/rpc/wire``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import grpc

from techai_webutils.core.errors.codes import (
    FIXED_CLIENT_MESSAGE,
    GRPC_STATUS,
    SAFE_MESSAGE,
)
from techai_webutils.core.errors.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.logger import Logger

_STATUS_BY_NUMBER: dict[int, grpc.StatusCode] = {s.value[0]: s for s in grpc.StatusCode}
"""Every gRPC status code by its number."""

_CODE_MAP: dict[ErrorCode, grpc.StatusCode] = {
    code: _STATUS_BY_NUMBER[number] for code, number in GRPC_STATUS.items()
}
"""Maps each domain ``ErrorCode`` to the gRPC status code returned to clients.

The taxonomy's ``GRPC_STATUS`` (``core/errors/codes.py``), the same source as Go's
``wire.Code``; a code it does not declare rides the INTERNAL default in ``to_grpc_status``.
"""

_FIXED_CLIENT_MESSAGE: dict[ErrorCode, str] = {
    code: SAFE_MESSAGE[code] for code in FIXED_CLIENT_MESSAGE
}
"""Codes whose client-facing message is a fixed string rather than the raw error text.

The taxonomy's ``fixed_client_message`` codes, each answered with its ``SAFE_MESSAGE``: the
fixed string strips any provider or implementation detail that must never reach a client
(the raw text is still recorded server-side).
"""


def safe_status(err: AppError) -> tuple[grpc.StatusCode, str]:
    """Return the status of ``err``'s code carrying only that code's client-safe message.

    A rejection the controller tier made (a denial, a rate limit, a timeout) answers with this, so
    its own text never reaches the caller (Go ``wire.SanitizeGRPC``).
    """
    code = _CODE_MAP.get(err.code, grpc.StatusCode.INTERNAL)
    return code, SAFE_MESSAGE.get(err.code, "internal error")


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
