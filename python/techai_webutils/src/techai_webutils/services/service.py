"""The service tier (mirrors Go ``services/service``).

``tier()`` orders the tier's layers; ``ServiceBase``, built from the service's contract,
runs its operations through it, switched and tuned per service and per operation by the
decoration config's ``services`` tier. ``ServiceBase.crud(repo, validate)`` decorates a
repository's CRUD as a service, and a domain operation runs through ``ServiceBase.exec``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, final, override

from techai_webutils.core.interfaces.crud_service import CrudService
from techai_webutils.foundation.decorate import Layer, Tier
from techai_webutils.foundation.decorate.roles import CrudOps

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable
    from typing import ClassVar, Literal

    from techai_webutils.core.domain_types.types import Page, PageRequest
    from techai_webutils.core.interfaces.repository import Repository
    from techai_webutils.foundation.decorate import Decorations, Subject


def tier() -> Tier:
    """Return the service tier, outermost first.

    Recovery codes an unexpected exception; each operation gets its span, metrics and log
    line; auth authorizes ``<service>.<op>``; and the timeout bounds the operation.

    Returns:
        The service tier, outermost first.

    """
    return Tier(
        "service",
        (
            Layer.RECOVERY,
            Layer.TRACING,
            Layer.METRICS,
            Layer.LOGGING,
            Layer.AUTH,
            Layer.TIMEOUT,
        ),
    )


class ServiceBase:
    """Runs one service's operations through the service tier."""

    __slots__: ClassVar[tuple[Literal["_subject"]]] = ("_subject",)

    def __init__(self, contract: type, decorations: Decorations, name: str) -> None:
        """Bind the service ``name``: one chain per method of ``contract``.

        Go's ``NewBase[S]``.

        Args:
            contract: The interface whose methods are the operations.
            decorations: Resolves the tier's layers.
            name: The component's name in the decoration config.

        """
        self._subject: Subject = decorations.subject_of(tier(), name, contract)

    @property
    def name(self) -> str:
        """The service's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run a domain operation ``fn`` as ``op`` through the tier.

        Args:
            op: The operation.
            fn: The call.

        Returns:
            What ``fn`` returns.

        """
        return await self._subject.exec(op, fn)

    def crud[T, P, ID](
        self,
        repo: Repository[T, P, ID],
        validate: Callable[[T], None] | None = None,
    ) -> CrudService[T, P, ID]:
        """Return the service serving ``repo``'s CRUD through the tier.

        ``validate`` (optional) checks the entity inside Create and Update, raising a
        coded error (``InvalidInputError``) on a violation.

        Args:
            repo: The repository the service wraps.
            validate: Checks an entity before Create and Update; ``None`` checks nothing.

        Returns:
            The service serving ``repo``'s CRUD through the tier.

        """
        return _DecoratedService(repo, self._subject, validate)


class _DecoratedService[T, P, ID](CrudOps[T, P, ID], CrudService[T, P, ID]):
    """A repository's CRUD run through the service tier."""

    def __init__(
        self,
        repo: Repository[T, P, ID],
        op: Subject,
        validate: Callable[[T], None] | None,
    ) -> None:
        """Bind the repository, the subject and the validator.

        Args:
            repo: The wrapped repository.
            op: The subject every call runs through.
            validate: Checks an entity before Create and Update.

        """
        super().__init__(repo, op, validate=validate)


@final
class DelegatingCrudService[T, P, ID](CrudService[T, P, ID]):
    """A service that serves CRUD from a decorated CrudService (``ServiceBase.crud``).

    A consumer service subclasses it to inherit decorated CRUD and adds only its domain
    operations, each run through its ``ServiceBase.exec``.
    """

    def __init__(self, crud: CrudService[T, P, ID]) -> None:
        """Bind the decorated service that serves CRUD.

        Args:
            crud: The decorated service.

        """
        self._crud = crud

    @override
    async def get(self, entity_id: ID) -> T:
        """Delegate to the decorated service.

        Args:
            entity_id: The entity's identifier.

        Returns:
            The entity.

        """
        return await self._crud.get(entity_id)

    @override
    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Delegate to the decorated service.

        Args:
            params: The filter the entities must match.
            page: Which page of results to return.

        Returns:
            The page.

        """
        return await self._crud.list(params, page)

    @override
    async def create(self, entity: T) -> T:
        """Delegate to the decorated service.

        Args:
            entity: The entity.

        Returns:
            The entity.

        """
        return await self._crud.create(entity)

    @override
    async def update(self, entity_id: ID, entity: T) -> T:
        """Delegate to the decorated service.

        Args:
            entity_id: The entity's identifier.
            entity: The entity's new state.

        Returns:
            The entity.

        """
        return await self._crud.update(entity_id, entity)

    @override
    async def delete(self, entity_id: ID) -> None:
        """Delegate to the decorated service.

        Args:
            entity_id: The entity's identifier.

        """
        await self._crud.delete(entity_id)
