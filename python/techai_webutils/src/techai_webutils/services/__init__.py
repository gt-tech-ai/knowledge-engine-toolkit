"""Services layer - domain service abstractions.

Provides the service tier (``tier``, ``ServiceBase``: built from the service's contract,
decorated CRUD over a Repository and domain operations), ``DelegatingCrudService`` for a consumer service, and
``BaseService`` (lifecycle).
"""

from techai_webutils.core.interfaces.service import BaseService
from techai_webutils.services.service import (
    DelegatingCrudService,
    ServiceBase,
    tier,
)

__all__ = [
    "BaseService",
    "DelegatingCrudService",
    "ServiceBase",
    "tier",
]
