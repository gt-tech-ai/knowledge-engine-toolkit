"""Services layer - domain service abstractions.

Provides the service tier (``tier``, ``ServiceBase``: decorated CRUD over a Repository, single-
operation services and domain operations), ``DelegatingCrudService`` for a consumer service, and
``BaseService`` (lifecycle).
"""

from techai_webutils.core.interfaces.service import BaseService
from techai_webutils.services.service import (
    DelegatingCrudService,
    OpServiceImpl,
    ServiceBase,
    tier,
)

__all__ = [
    "BaseService",
    "DelegatingCrudService",
    "OpServiceImpl",
    "ServiceBase",
    "tier",
]
