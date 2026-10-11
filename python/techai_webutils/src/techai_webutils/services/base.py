"""Base service patterns for FastAPI and Ray Serve.

BaseService has moved to ``techai_webutils.core.interfaces.service``.
This module re-exports it for backward compatibility.
"""

from techai_webutils.core.interfaces.service import BaseService

__all__ = ["BaseService"]
