"""Services layer - domain service abstractions.

Provides BaseService (lifecycle), BaseCrudService (CRUD delegation),
and ServiceBuilder for decorator composition.
"""

from techai_webutils.core.interfaces.service import BaseService
from techai_webutils.services.crud_service import BaseCrudService
from techai_webutils.services.decorators.builder import ServiceBuilder

__all__ = ["BaseCrudService", "BaseService", "ServiceBuilder"]
