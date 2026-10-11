"""Fluent builder for composing service decorators.

Mirrors Go's ``services/service/decorators/builder.go``.
"""

from __future__ import annotations

import asyncio
import logging
import time
from typing import TYPE_CHECKING, TypeVar, cast

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.errors.errors import (
    AppError,
    AppTimeoutError,
    ForbiddenError,
    InternalError,
)
from techai_webutils.core.interfaces.crud_service import CrudService

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.logger import Logger

T = TypeVar("T")
P = TypeVar("P")
ID = TypeVar("ID")


class _TimeoutServiceDecorator[T, P, ID](CrudService[T, P, ID]):
    """Wraps service operations with an async timeout."""

    def __init__(self, inner: CrudService[T, P, ID], timeout: float) -> None:
        """Wrap ``inner`` so every operation aborts after ``timeout`` seconds."""
        self._inner = inner
        self._timeout = timeout

    async def get(self, entity_id: ID) -> T:
        """Fetch the entity, raising ``AppTimeoutError`` if the deadline elapses."""
        try:
            return await asyncio.wait_for(
                self._inner.get(entity_id), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"service.get timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """List a page of entities, raising ``AppTimeoutError`` if the deadline elapses."""
        try:
            return await asyncio.wait_for(
                self._inner.list(params, page), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"service.list timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def create(self, entity: T) -> T:
        """Create the entity, raising ``AppTimeoutError`` if the deadline elapses."""
        try:
            return await asyncio.wait_for(
                self._inner.create(entity), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"service.create timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def update(self, entity_id: ID, entity: T) -> T:
        """Update the entity, raising ``AppTimeoutError`` if the deadline elapses."""
        try:
            return await asyncio.wait_for(
                self._inner.update(entity_id, entity), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"service.update timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def delete(self, entity_id: ID) -> None:
        """Delete the entity, raising ``AppTimeoutError`` if the deadline elapses."""
        try:
            await asyncio.wait_for(self._inner.delete(entity_id), timeout=self._timeout)
        except TimeoutError as e:
            msg = f"service.delete timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e


class _LoggingServiceDecorator[T, P, ID](CrudService[T, P, ID]):
    """Logs service operations at debug level on entry, error level on failure.

    Matches Go's ``loggingDecorator`` which logs entry at Debug AND errors at Error
    with duration.
    """

    def __init__(self, inner: CrudService[T, P, ID], logger: Logger, name: str) -> None:
        """Wrap ``inner``, tagging each log line with the service ``name``."""
        self._inner = inner
        self._logger = logger
        self._name = name

    async def get(self, entity_id: ID) -> T:
        """Log the get call, then delegate; log and re-raise on failure."""
        self._logger.debug("service.get", service=self._name, id=str(entity_id))
        start = time.monotonic()
        try:
            return await self._inner.get(entity_id)
        except Exception as exc:
            self._logger.exception(
                "service.get failed",
                service=self._name,
                id=str(entity_id),
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Log the list call, then delegate; log and re-raise on failure."""
        self._logger.debug("service.list", service=self._name)
        start = time.monotonic()
        try:
            return await self._inner.list(params, page)
        except Exception as exc:
            self._logger.exception(
                "service.list failed",
                service=self._name,
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise

    async def create(self, entity: T) -> T:
        """Log the create call and its success, then delegate; log and re-raise on failure."""
        self._logger.debug("service.create", service=self._name)
        start = time.monotonic()
        try:
            result = await self._inner.create(entity)
            self._logger.debug("service.created", service=self._name)
            return result
        except Exception as exc:
            self._logger.exception(
                "service.create failed",
                service=self._name,
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise

    async def update(self, entity_id: ID, entity: T) -> T:
        """Log the update call, then delegate; log and re-raise on failure."""
        self._logger.debug("service.update", service=self._name, id=str(entity_id))
        start = time.monotonic()
        try:
            return await self._inner.update(entity_id, entity)
        except Exception as exc:
            self._logger.exception(
                "service.update failed",
                service=self._name,
                id=str(entity_id),
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise

    async def delete(self, entity_id: ID) -> None:
        """Log the delete call, then delegate; log and re-raise on failure."""
        self._logger.debug("service.delete", service=self._name, id=str(entity_id))
        start = time.monotonic()
        try:
            await self._inner.delete(entity_id)
        except Exception as exc:
            self._logger.exception(
                "service.delete failed",
                service=self._name,
                id=str(entity_id),
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise


class _AuthServiceDecorator[T, P, ID](CrudService[T, P, ID]):
    """Checks authorization before service operations.

    Receives an action string (e.g. ``"users.get"``) for per-operation authorization,
    matching Go's ``authFn(ctx, action string) error`` pattern.
    """

    def __init__(
        self,
        inner: CrudService[T, P, ID],
        auth_fn: Callable[[str], Awaitable[bool]],
        name: str,
    ) -> None:
        """Wrap ``inner``, gating each operation on ``auth_fn`` scoped to ``name``."""
        self._inner = inner
        self._auth_fn = auth_fn
        self._name = name

    async def _check_auth(self, op: str) -> None:
        """Authorize ``<name>.<op>``, raising ``ForbiddenError`` when denied."""
        action = f"{self._name}.{op}"
        if not await self._auth_fn(action):
            raise ForbiddenError()

    async def get(self, entity_id: ID) -> T:
        """Authorize the get action, then delegate to the wrapped service."""
        await self._check_auth("get")
        return await self._inner.get(entity_id)

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Authorize the list action, then delegate to the wrapped service."""
        await self._check_auth("list")
        return await self._inner.list(params, page)

    async def create(self, entity: T) -> T:
        """Authorize the create action, then delegate to the wrapped service."""
        await self._check_auth("create")
        return await self._inner.create(entity)

    async def update(self, entity_id: ID, entity: T) -> T:
        """Authorize the update action, then delegate to the wrapped service."""
        await self._check_auth("update")
        return await self._inner.update(entity_id, entity)

    async def delete(self, entity_id: ID) -> None:
        """Authorize the delete action, then delegate to the wrapped service."""
        await self._check_auth("delete")
        await self._inner.delete(entity_id)


class _RecoveryServiceDecorator[T, P, ID](CrudService[T, P, ID]):
    """Catches unexpected exceptions and wraps them as InternalError."""

    def __init__(self, inner: CrudService[T, P, ID]) -> None:
        """Wrap ``inner`` so unexpected exceptions surface as ``InternalError``."""
        self._inner = inner
        self._logger = logging.getLogger(__name__)

    async def get(self, entity_id: ID) -> T:
        """Run the get call under the recovery guard."""
        return cast(T, await self._safe(self._inner.get, entity_id))

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Run the list call under the recovery guard."""
        return cast(Page[T], await self._safe(self._inner.list, params, page))

    async def create(self, entity: T) -> T:
        """Run the create call under the recovery guard."""
        return cast(T, await self._safe(self._inner.create, entity))

    async def update(self, entity_id: ID, entity: T) -> T:
        """Run the update call under the recovery guard."""
        return cast(T, await self._safe(self._inner.update, entity_id, entity))

    async def delete(self, entity_id: ID) -> None:
        """Run the delete call under the recovery guard."""
        await self._safe(self._inner.delete, entity_id)

    async def _safe(self, fn: Callable[..., Awaitable[object]], *args: object) -> object:  # type: ignore[return]
        """Invoke ``fn``, re-raising ``AppError`` as-is and wrapping anything else as ``InternalError``."""
        try:
            return await fn(*args)
        except AppError:
            raise
        except Exception as e:
            self._logger.exception("Unhandled exception")
            raise InternalError(str(e), cause=e) from e


class ServiceBuilder[T, P, ID]:
    """Builder that composes decorators around a CrudService.

    Recovery is always applied (outermost) — no explicit opt-in required.
    This matches Go's ``ServiceBuilder.Build()`` where recovery is unconditional.

    Example::

        svc = (ServiceBuilder(base_svc, "users")
            .with_logging(logger)
            .with_authorization(auth_fn)
            .build())
    """

    def __init__(self, inner: CrudService[T, P, ID], name: str) -> None:
        """Start a builder around ``inner`` with no decorators selected yet."""
        self._inner = inner
        self._name = name
        self._logger: Logger | None = None
        self._auth_fn: Callable[[str], Awaitable[bool]] | None = None
        self._timeout: float | None = None

    def with_logging(self, logger: Logger) -> ServiceBuilder[T, P, ID]:
        """Add a logging decorator."""
        self._logger = logger
        return self

    def with_authorization(
        self, auth_fn: Callable[[str], Awaitable[bool]]
    ) -> ServiceBuilder[T, P, ID]:
        """Add an authorization decorator.

        Args:
            auth_fn: Async callable receiving an action string (e.g. ``"users.create"``)
                and returning True if the action is allowed.

        """
        self._auth_fn = auth_fn
        return self

    def with_timeout(self, seconds: float) -> ServiceBuilder[T, P, ID]:
        """Add a timeout decorator."""
        self._timeout = seconds
        return self

    def build(self) -> CrudService[T, P, ID]:
        """Build the decorated service.

        Chain: base -> timeout -> logging -> authorization -> recovery.
        Recovery is always applied (outermost) to prevent unhandled exceptions
        from escaping the service boundary, matching Go's unconditional behavior.
        """
        svc: CrudService[T, P, ID] = self._inner
        if self._timeout is not None:
            svc = _TimeoutServiceDecorator(svc, self._timeout)
        if self._logger is not None:
            svc = _LoggingServiceDecorator(svc, self._logger, self._name)
        if self._auth_fn is not None:
            svc = _AuthServiceDecorator(svc, self._auth_fn, self._name)
        # Recovery is always applied — no unhandled exception escapes the service boundary
        return _RecoveryServiceDecorator(svc)
