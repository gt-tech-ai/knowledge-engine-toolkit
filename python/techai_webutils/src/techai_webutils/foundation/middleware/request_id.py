"""Request ID middleware for propagating correlation IDs.

Mirrors Go's ``go/foundation/middleware/request_id.go``.
Generates or propagates a request ID header across the request lifecycle.
"""

from __future__ import annotations

import contextvars
import uuid
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

_REQUEST_ID_HEADER = "X-Request-ID"
"""HTTP header name carrying the correlation ID across the request lifecycle."""

_request_id_var: contextvars.ContextVar[str] = contextvars.ContextVar(
    "request_id",
    default="",
)


def get_request_id() -> str:
    """Retrieve the current request ID from context."""
    return _request_id_var.get()


def set_request_id(request_id: str) -> contextvars.Token[str]:
    """Set the request ID in the current context."""
    return _request_id_var.set(request_id)


def request_id_middleware(
    handler: Callable[..., Awaitable[Any]],
) -> Callable[..., Awaitable[Any]]:
    """Middleware that ensures a request ID is set in context.

    If an incoming request carries an ``X-Request-ID`` header (via kwargs or
    a headers dict), that value is used. Otherwise, a new UUID is generated.

    This is a generic middleware — framework-specific adapters (FastAPI, gRPC)
    should extract the header and pass it via ``request_id`` kwarg.
    """

    async def wrapper(*args: Any, **kwargs: Any) -> Any:  # noqa: ANN401
        """Bind a request ID for the duration of the wrapped call, then restore.

        Consumes a ``request_id`` kwarg if present (otherwise mints a UUID),
        sets it in the context var, and always resets the var afterwards so the
        ID never leaks into a sibling request handled on the same task.
        """
        request_id = kwargs.pop("request_id", None) or str(uuid.uuid4())
        token = set_request_id(request_id)
        try:
            return await handler(*args, **kwargs)
        finally:
            _request_id_var.reset(token)

    return wrapper
