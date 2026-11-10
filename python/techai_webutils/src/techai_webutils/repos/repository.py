"""The repository tier (mirrors Go ``repos/repository``).

``tier()`` orders the tier's layers; ``RepositoryBase``, built from the repository's
contract, runs its operations through it, switched and tuned per repository and per
operation by the decoration config's ``repositories`` tier. ``RepositoryBase.crud(store)``
decorates a store's CRUD; a custom query runs through ``RepositoryBase.exec``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, final, override

from techai_webutils.core.interfaces.repository import Repository
from techai_webutils.foundation.decorate import Layer, Subject, Tier
from techai_webutils.foundation.decorate.roles import CrudOps, exists

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable
    from typing import ClassVar, Literal

    from techai_webutils.core.domain_types.types import Page, PageRequest
    from techai_webutils.core.interfaces.store import Store
    from techai_webutils.foundation.decorate import Decorations


def tier() -> Tier:
    """Return the repository tier, outermost first.

    Each call gets its span, metrics and log line around everything else; the timeout
    bounds each attempt; the breaker sheds calls while the database keeps failing; and
    retry, innermost, retries a transient failure through the breaker and a fresh
    deadline.

    Returns:
        The repository tier, outermost first.

    """
    return Tier(
        "repository",
        (
            Layer.TRACING,
            Layer.METRICS,
            Layer.LOGGING,
            Layer.TIMEOUT,
            Layer.CIRCUIT_BREAKER,
            Layer.RETRY,
        ),
    )


class RepositoryBase:
    """Runs one repository's operations through the repository tier."""

    __slots__: ClassVar[tuple[Literal["_subject"]]] = ("_subject",)

    def __init__(self, contract: type, decorations: Decorations, name: str) -> None:
        """Bind the repository ``name``.

        One chain per method of ``contract`` (Go ``NewBase[R]``).

        Args:
            contract: The interface whose methods are the operations.
            decorations: Resolves the tier's layers.
            name: The component's name in the decoration config.

        """
        self._subject: Subject = decorations.subject_of(tier(), name, contract)

    @property
    def name(self) -> str:
        """The repository's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run a custom query ``fn`` as the operation ``op`` through the tier.

        Args:
            op: The operation.
            fn: The call.

        Returns:
            What ``fn`` returns.

        """
        return await self._subject.exec(op, fn)

    async def exec_once[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run a non-idempotent write ``fn`` as ``op`` through the tier, without retry.

        Args:
            op: The operation.
            fn: The call.

        Returns:
            What ``fn`` returns.

        """
        return await self._subject.exec_once(op, fn)

    def crud[T, P, ID](self, store: Store[T, P, ID]) -> Repository[T, P, ID]:
        """Return the repository serving ``store``'s CRUD through the tier.

        Create is never retried: a create whose commit landed must not run twice.

        Args:
            store: The store the repository wraps.

        Returns:
            The repository serving ``store``'s CRUD through the tier.

        """
        return _DecoratedRepository(store, self._subject, self._subject.once())


@final
class _DecoratedRepository[T, P, ID](CrudOps[T, P, ID], Repository[T, P, ID]):
    """A store's CRUD run through the repository tier."""

    def __init__(self, store: Store[T, P, ID], op: Subject, create: Subject) -> None:
        """Bind the store and the subjects.

        Args:
            store: The wrapped store.
            op: The subject every call runs through.
            create: Create's own subject (never retried).

        """
        super().__init__(store, op, create=create)
        self._store = store

    @override
    async def exists(self, entity_id: ID) -> bool:
        """Run Exists through the tier.

        Args:
            entity_id: The entity's identifier.

        Returns:
            Whether the entity exists.

        """
        return await exists(self._op, self._store, entity_id)


@final
class DelegatingRepository[T, P, ID](Repository[T, P, ID]):
    """A repository that serves CRUD from a decorated Repository.

    (``RepositoryBase.crud``).

    A consumer repository subclasses it to inherit decorated CRUD and adds only its custom
    queries, each run through its ``RepositoryBase.exec``.
    """

    def __init__(self, crud: Repository[T, P, ID]) -> None:
        """Bind the decorated repository that serves CRUD.

        Args:
            crud: The decorated repository.

        """
        self._crud = crud

    @override
    async def get(self, entity_id: ID) -> T:
        """Delegate to the decorated repository.

        Args:
            entity_id: The entity's identifier.

        Returns:
            The entity.

        """
        return await self._crud.get(entity_id)

    @override
    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Delegate to the decorated repository.

        Args:
            params: The filter the entities must match.
            page: Which page of results to return.

        Returns:
            The page.

        """
        return await self._crud.list(params, page)

    @override
    async def create(self, entity: T) -> T:
        """Delegate to the decorated repository.

        Args:
            entity: The entity.

        Returns:
            The entity.

        """
        return await self._crud.create(entity)

    @override
    async def update(self, entity_id: ID, entity: T) -> T:
        """Delegate to the decorated repository.

        Args:
            entity_id: The entity's identifier.
            entity: The entity's new state.

        Returns:
            The entity.

        """
        return await self._crud.update(entity_id, entity)

    @override
    async def delete(self, entity_id: ID) -> None:
        """Delegate to the decorated repository.

        Args:
            entity_id: The entity's identifier.

        """
        await self._crud.delete(entity_id)

    @override
    async def exists(self, entity_id: ID) -> bool:
        """Delegate to the decorated repository.

        Args:
            entity_id: The entity's identifier.

        Returns:
            Whether the entity exists.

        """
        return await self._crud.exists(entity_id)
