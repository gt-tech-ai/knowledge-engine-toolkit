"""Composable ASGI middleware utilities.

Mirrors Go's ``go/foundation/middleware/`` with ``Chain()``,
``request_id()``, and other composable HTTP/ASGI middleware.

Usage::

    from techai_webutils.foundation.middleware import chain, request_id_middleware

    app = chain(request_id_middleware, my_other_middleware)(base_app)
"""

from techai_webutils.foundation.middleware.chain import chain
from techai_webutils.foundation.middleware.request_id import request_id_middleware

__all__ = [
    "chain",
    "request_id_middleware",
]
