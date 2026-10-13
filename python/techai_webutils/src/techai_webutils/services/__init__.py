"""Services layer - domain service abstractions.

Provides BaseService (lifecycle), BaseCrudService (CRUD delegation), DelegatingCrudService
(CRUD from a decorated service), ServiceBuilder for decorator composition, and
service_op_chain for domain ops.
"""

from techai_webutils.core.interfaces.service import BaseService
from techai_webutils.services.crud_service import BaseCrudService, DelegatingCrudService
from techai_webutils.services.decorators.builder import ServiceBuilder
from techai_webutils.services.op_chain import ServiceOpDeps, service_op_chain

__all__ = [
    "BaseCrudService",
    "BaseService",
    "DelegatingCrudService",
    "ServiceBuilder",
    "ServiceOpDeps",
    "service_op_chain",
]
