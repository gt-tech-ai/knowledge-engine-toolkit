"""The repository tier (mirrors Go ``repos/repository``).

``tier()`` orders the tier's layers; ``RepositoryBase`` runs one repository's operations through
it, switched and tuned per repository by the decoration config's ``repositories`` tier.
``RepositoryBase.crud(store)`` decorates a store's CRUD; a custom query runs through
``RepositoryBase.exec``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.repository import Repository
from techai_webutils.foundation.decorate import Layer, Operation, Scope, Subject, Tier
from techai_webutils.foundation.decorate.roles import CrudOps, exists

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.domain_types.types import Page, PageRequest
    from techai_webutils.core.interfaces.store import Store
    from techai_webutils.foundation.decorate import Decorations, Deps


def tier() -> Tier:
    """Return the repository tier, outermost first.

    Each call gets its span, metrics and log line around everything else; the timeout bounds each
    attempt; the breaker sheds calls while the database keeps failing; and retry, innermost,
    retries a transient failure through the breaker and a fresh deadline.
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

    __slots__ = ("_create", "_ops", "_subject")

    def __init__(self, name: str, deps: Deps) -> None:
        """Bind the repository ``name``'s subject; Create gets one without retry.

        Its named operations share ``deps``.
        """
        self._subject = tier().subject(name, deps)
        self._create = tier().subject(name, deps.without_retry())
        self._ops = Scope(tier(), name, deps)

    @classmethod
    def from_decorations(cls, name: str, decorations: Decorations) -> RepositoryBase:
        """Return the base of the repository ``name``, its layers resolved from the config.

        Each named operation ``op`` resolves as ``<name>.<op>``.
        """
        base = cls(name, decorations.deps(tier(), name))
        base._ops = decorations.scope(tier(), name)
        return base

    def query[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Operation[In, Out]:
        """Return the named read ``op``, run through the tier with its retry (a read is idempotent)."""
        return Operation(self._ops.subject(op), fn)

    def command[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Operation[In, Out]:
        """Return the named write ``op``, run through the tier without retry.

        A write whose first attempt committed must not run twice.
        """
        return Operation(self._ops.subject(op, without_retry=True), fn)

    @property
    def name(self) -> str:
        """The repository's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run a custom query ``fn`` as the operation ``op`` through the tier."""
        return await self._subject.exec(op, fn)

    def crud[T, P, ID](self, store: Store[T, P, ID]) -> Repository[T, P, ID]:
        """Return the repository serving ``store``'s CRUD through the tier.

        Create is never retried: a create whose commit landed must not run twice.
        """
        return _DecoratedRepository(store, self._subject, self._create)


class _DecoratedRepository[T, P, ID](CrudOps[T, P, ID], Repository[T, P, ID]):
    """A store's CRUD run through the repository tier."""

    def __init__(self, store: Store[T, P, ID], op: Subject, create: Subject) -> None:
        """Bind the store and the subjects."""
        super().__init__(store, op, create=create)
        self._store = store

    async def exists(self, entity_id: ID) -> bool:
        """Run Exists through the tier."""
        return await exists(self._op, self._store, entity_id)


class DelegatingRepository[T, P, ID](Repository[T, P, ID]):
    """A repository that serves CRUD from a decorated Repository (``RepositoryBase.crud``).

    A consumer repository subclasses it to inherit decorated CRUD and adds only its custom
    queries, each run through its ``RepositoryBase.exec``.
    """

    def __init__(self, crud: Repository[T, P, ID]) -> None:
        """Bind the decorated repository that serves CRUD."""
        self._crud = crud

    async def get(self, entity_id: ID) -> T:
        """Delegate to the decorated repository."""
        return await self._crud.get(entity_id)

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Delegate to the decorated repository."""
        return await self._crud.list(params, page)

    async def create(self, entity: T) -> T:
        """Delegate to the decorated repository."""
        return await self._crud.create(entity)

    async def update(self, entity_id: ID, entity: T) -> T:
        """Delegate to the decorated repository."""
        return await self._crud.update(entity_id, entity)

    async def delete(self, entity_id: ID) -> None:
        """Delegate to the decorated repository."""
        await self._crud.delete(entity_id)

    async def exists(self, entity_id: ID) -> bool:
        """Delegate to the decorated repository."""
        return await self._crud.exists(entity_id)
