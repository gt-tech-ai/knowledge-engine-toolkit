"""CRUD role shims over a decorated Subject (mirrors Go ``decorate/roles.go``).

``CrudOps`` runs each CRUD call of an inner store, repository or service as an operation
of a ``Subject`` — ``Get``, ``List``, ``Create``, ``Update``, ``Delete``, ``Exists`` — so
the repository and service tiers decorate CRUD without a decorator class per layer. A tier
subclasses it with its own interface (``Repository``, ``CrudService``).
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.core.domain_types.types import Page, PageRequest
    from techai_webutils.foundation.decorate.tier import Subject


class CrudBackend[T, P, ID](Protocol):
    """The CRUD surface ``CrudOps`` decorates: a store, a repository or a service."""

    async def get(self, entity_id: ID) -> T:
        """Return the entity ``entity_id``.

        Args:
            entity_id: The entity's identifier.

        Returns:
            The entity.

        """
        ...

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Return one page of the entities ``params`` selects.

        Args:
            params: The filter the entities must match.
            page: Which page of results to return.

        Returns:
            The page of entities.

        """
        ...

    async def create(self, entity: T) -> T:
        """Create ``entity`` and return it as stored.

        Args:
            entity: The entity.

        Returns:
            The entity as stored.

        """
        ...

    async def update(self, entity_id: ID, entity: T) -> T:
        """Replace the entity ``entity_id`` and return it as stored.

        Args:
            entity_id: The entity's identifier.
            entity: The entity's new state.

        Returns:
            The entity as stored.

        """
        ...

    async def delete(self, entity_id: ID) -> None:
        """Delete the entity ``entity_id``.

        Args:
            entity_id: The entity's identifier.

        """
        ...


class ExistsBackend[ID](Protocol):
    """A backend that also answers existence (a store or a repository)."""

    async def exists(self, entity_id: ID) -> bool:
        """Report whether the entity ``entity_id`` exists.

        Args:
            entity_id: The entity's identifier.

        Returns:
            Whether it exists.

        """
        ...


class CrudOps[T, P, ID]:
    """Runs a backend's CRUD calls through a Subject.

    ``create`` decorates Create (a repository passes one without retry, since a create
    whose commit landed must not run twice); ``validate`` (optional) checks the entity
    inside Create and Update before the backend sees it, raising a coded error on a
    violation.
    """

    def __init__(
        self,
        inner: CrudBackend[T, P, ID],
        op: Subject,
        *,
        create: Subject | None = None,
        validate: Callable[[T], None] | None = None,
    ) -> None:
        """Bind the backend, the subject for every call and the validator.

        Create has its own subject.

        Args:
            inner: The backend.
            op: The subject every call runs through.
            create: Create's own subject (never retried); ``None`` uses ``op``.
            validate: Checks an entity before Create and Update; ``None`` checks nothing.

        """
        self._inner: CrudBackend[T, P, ID] = inner
        self._op: Subject = op
        self._create: Subject = create or op
        self._validate: Callable[[T], None] | None = validate

    async def get(self, entity_id: ID) -> T:
        """Run Get through the subject.

        Args:
            entity_id: The entity's identifier.

        Returns:
            The entity.

        """
        return await self._op.exec("get", lambda: self._inner.get(entity_id))

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Run List through the subject.

        Args:
            params: The filter the entities must match.
            page: Which page of results to return.

        Returns:
            The page.

        """
        return await self._op.exec("list", lambda: self._inner.list(params, page))

    async def create(self, entity: T) -> T:
        """Run Create (validated) through Create's subject.

        Args:
            entity: The entity.

        Returns:
            The entity.

        """

        async def run() -> T:
            """Check the entity, then create it.

            Returns:
                The created entity.

            """
            self._check(entity)
            return await self._inner.create(entity)

        return await self._create.exec("create", run)

    async def update(self, entity_id: ID, entity: T) -> T:
        """Run Update (validated) through the subject.

        Args:
            entity_id: The entity's identifier.
            entity: The entity's new state.

        Returns:
            The entity.

        """

        async def run() -> T:
            """Check the entity, then update it.

            Returns:
                The updated entity.

            """
            self._check(entity)
            return await self._inner.update(entity_id, entity)

        return await self._op.exec("update", run)

    async def delete(self, entity_id: ID) -> None:
        """Run Delete through the subject.

        Args:
            entity_id: The entity's identifier.

        """
        await self._op.exec("delete", lambda: self._inner.delete(entity_id))

    def _check(self, entity: T) -> None:
        """Validate ``entity`` when a validator is set.

        Args:
            entity: The entity.

        """
        if self._validate is not None:
            self._validate(entity)


async def exists[ID](op: Subject, inner: ExistsBackend[ID], entity_id: ID) -> bool:
    """Run Exists through ``op``.

    Args:
        op: The subject the call runs through.
        inner: The backend.
        entity_id: The entity's id.

    Returns:
        Whether the entity exists.

    """
    return await op.exec("exists", lambda: inner.exists(entity_id))
