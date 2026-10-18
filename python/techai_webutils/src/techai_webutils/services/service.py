"""The service tier (mirrors Go ``services/service``).

``tier()`` orders the tier's layers; ``ServiceBase`` runs one service's operations through it,
switched and tuned per service by the decoration config's ``services`` tier.
``ServiceBase.crud(repo, validate)`` decorates a repository's CRUD as a service,
``ServiceBase.op_service(run)`` a single-operation service, and a domain operation runs through
``ServiceBase.exec``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.crud_service import CrudService
from techai_webutils.foundation.decorate import Layer, Operation, Scope, Tier
from techai_webutils.foundation.decorate.roles import CrudOps

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.domain_types.types import Page, PageRequest
    from techai_webutils.core.interfaces.repository import Repository
    from techai_webutils.foundation.decorate import Decorations, Deps, Subject


def tier() -> Tier:
    """Return the service tier, outermost first.

    Recovery codes an unexpected exception; each operation gets its span, metrics and log line;
    auth authorizes ``<service>.<op>``; and the timeout bounds the operation.
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

    __slots__ = ("_ops", "_subject")

    def __init__(self, name: str, deps: Deps) -> None:
        """Bind the service ``name``'s subject; its named operations share ``deps``."""
        self._subject = tier().subject(name, deps)
        self._ops = Scope(tier(), name, deps)

    @classmethod
    def from_decorations(cls, name: str, decorations: Decorations) -> ServiceBase:
        """Return the base of the service ``name``, its layers resolved from the config.

        Each named operation ``op`` resolves as ``<name>.<op>``.
        """
        base = cls(name, decorations.deps(tier(), name))
        base._ops = decorations.scope(tier(), name)
        return base

    def op[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Operation[In, Out]:
        """Return the named operation ``op``, run through the tier.

        An input with a ``validate()`` method is validated first; its coded error is raised
        without running ``fn``.
        """

        async def run(value: In) -> Out:
            validate = getattr(value, "validate", None)
            if callable(validate):
                validate()
            return await fn(value)

        return Operation(self._ops.subject(op), run)

    @property
    def name(self) -> str:
        """The service's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run a domain operation ``fn`` as ``op`` through the tier."""
        return await self._subject.exec(op, fn)

    def crud[T, P, ID](
        self,
        repo: Repository[T, P, ID],
        validate: Callable[[T], None] | None = None,
    ) -> CrudService[T, P, ID]:
        """Return the service serving ``repo``'s CRUD through the tier.

        ``validate`` (optional) checks the entity inside Create and Update, raising a coded error
        (``InvalidInputError``) on a violation.
        """
        return _DecoratedService(repo, self._subject, validate)

    def op_service[A, R](self, run: Callable[[A], Awaitable[R]]) -> OpServiceImpl[A, R]:
        """Return the single-operation service whose ``run`` runs as ``Run`` through the tier."""
        return OpServiceImpl(self._subject, run)


class _DecoratedService[T, P, ID](CrudOps[T, P, ID], CrudService[T, P, ID]):
    """A repository's CRUD run through the service tier."""

    def __init__(
        self,
        repo: Repository[T, P, ID],
        op: Subject,
        validate: Callable[[T], None] | None,
    ) -> None:
        """Bind the repository, the subject and the validator."""
        super().__init__(repo, op, validate=validate)


class OpServiceImpl[A, R]:
    """A single-operation service (``core.interfaces.op_service.OpService``) run through the tier."""

    __slots__ = ("_run", "_subject")

    def __init__(self, subject: Subject, run: Callable[[A], Awaitable[R]]) -> None:
        """Bind the subject and the operation."""
        self._subject = subject
        self._run = run

    @property
    def name(self) -> str:
        """The service's name."""
        return self._subject.name

    async def run(self, args: A) -> R:
        """Run the operation as ``Run`` through the tier."""
        return await self._subject.exec("Run", lambda: self._run(args))


class DelegatingCrudService[T, P, ID](CrudService[T, P, ID]):
    """A service that serves CRUD from a decorated CrudService (``ServiceBase.crud``).

    A consumer service subclasses it to inherit decorated CRUD and adds only its domain
    operations, each run through its ``ServiceBase.exec``.
    """

    def __init__(self, crud: CrudService[T, P, ID]) -> None:
        """Bind the decorated service that serves CRUD."""
        self._crud = crud

    async def get(self, entity_id: ID) -> T:
        """Delegate to the decorated service."""
        return await self._crud.get(entity_id)

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Delegate to the decorated service."""
        return await self._crud.list(params, page)

    async def create(self, entity: T) -> T:
        """Delegate to the decorated service."""
        return await self._crud.create(entity)

    async def update(self, entity_id: ID, entity: T) -> T:
        """Delegate to the decorated service."""
        return await self._crud.update(entity_id, entity)

    async def delete(self, entity_id: ID) -> None:
        """Delegate to the decorated service."""
        await self._crud.delete(entity_id)
