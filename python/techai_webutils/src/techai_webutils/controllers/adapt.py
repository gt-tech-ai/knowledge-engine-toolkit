"""REST adapter bridging typed handlers to framework handlers.

Mirrors Go's ``go/transport/rest/adapt.go`` ``Adapt[Req, Resp]()`` and
``AdaptNoContent[Req, Resp]()``.

Provides a generic bridge from typed ``HandlerFunc`` to framework request
handlers (e.g., FastAPI, Starlette). The parse function extracts the typed
request from the raw HTTP request; the handler processes it; the response
is serialized back.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.controllers.base import BaseController


def adapt[Req, Resp](
    controller: BaseController,
    handler: Callable[[Req], Awaitable[Resp]],
    parse: Callable[[Any], Awaitable[Req]],
    status: int = 200,
) -> Callable[[Any], Awaitable[dict[str, Any]]]:
    """Bridge a typed handler to a framework request handler.

    Args:
        controller: BaseController for error/response serialization.
        handler: Typed handler ``(Req) -> Resp``.
        parse: Async function that extracts ``Req`` from the raw request.
        status: HTTP status code for successful responses.

    Returns:
        An async function ``(raw_request) -> dict`` suitable for framework routing.

    """

    async def framework_handler(raw_request: object) -> dict[str, Any]:
        """Parse, dispatch, and serialize one request, converting errors to responses."""
        try:
            req = await parse(raw_request)
            result = await handler(req)
            return controller.write_json(status, result)
        except Exception as exc:
            return controller.write_error(exc)

    return framework_handler


def adapt_no_content[Req, Resp](
    controller: BaseController,
    handler: Callable[[Req], Awaitable[Resp]],
    parse: Callable[[Any], Awaitable[Req]],
) -> Callable[[Any], Awaitable[dict[str, Any]]]:
    """Bridge a typed handler that returns no content (204).

    Same as ``adapt`` but always returns 204 No Content on success.
    """

    async def framework_handler(raw_request: object) -> dict[str, Any]:
        """Parse and dispatch one request, returning 204 or an error response."""
        try:
            req = await parse(raw_request)
            await handler(req)
            return controller.write_no_content()
        except Exception as exc:
            return controller.write_error(exc)

    return framework_handler
