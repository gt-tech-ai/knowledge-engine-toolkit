"""Controllers layer - thin transport adapters.

Controllers are the transport layer between external requests (HTTP, gRPC, WebSocket)
and internal business logic (workflows, services). They deserialize requests, delegate
to workflows, and serialize responses. ``ControllerBase.handler`` runs a handler through the
controller tier (``tier``).

Controllers implements the eighth tier (ARCHITECTURE.md#layers-and-import-direction):
core -> foundation -> clients -> repos -> services -> pipelines -> workflows -> controllers
"""

from __future__ import annotations

from techai_webutils.controllers.base import (
    BaseController,
    ErrorResponse,
    map_error_to_http_status,
)
from techai_webutils.controllers.handler import ControllerBase, HandlerFunc, tier

__all__ = [
    "BaseController",
    "ControllerBase",
    "ErrorResponse",
    "HandlerFunc",
    "map_error_to_http_status",
    "tier",
]
