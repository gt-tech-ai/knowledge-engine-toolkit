"""Controllers layer - thin transport adapters.

Controllers are the transport layer between external requests (HTTP, gRPC, WebSocket)
and internal business logic (workflows, services). They deserialize requests, delegate
to workflows, and serialize responses.

Controllers implements the eighth tier (ARCHITECTURE.md#layers-and-import-direction):
core -> foundation -> clients -> repos -> services -> pipelines -> workflows -> controllers
"""

from __future__ import annotations

from techai_webutils.controllers.base import (
    BaseController,
    ErrorResponse,
    map_error_to_http_status,
)
from techai_webutils.controllers.decorators import HandlerBuilder, HandlerFunc

__all__ = [
    "BaseController",
    "ErrorResponse",
    "HandlerBuilder",
    "HandlerFunc",
    "map_error_to_http_status",
]
