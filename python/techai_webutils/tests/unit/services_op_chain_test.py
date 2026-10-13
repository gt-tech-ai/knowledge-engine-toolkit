"""Tests for the service custom-op chain, DelegatingCrudService, and ServiceBuilder validation/metrics/tracing."""

from __future__ import annotations

from contextlib import contextmanager
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.errors.errors import (
    ConflictError,
    ForbiddenError,
    InternalError,
    InvalidInputError,
)
from techai_webutils.core.interfaces.crud_service import CrudService
from techai_webutils.core.interfaces.repository import Repository
from techai_webutils.foundation.decorate import exec_op
from techai_webutils.services.crud_service import BaseCrudService, DelegatingCrudService
from techai_webutils.services.decorators.builder import ServiceBuilder
from techai_webutils.services.op_chain import ServiceOpDeps, service_op_chain

if TYPE_CHECKING:
    from collections.abc import Generator


def _span_recorder(calls: list[str], spans: list[str]) -> MagicMock:
    """Return a tracer whose spans append "tracing" on entry and record their names."""
    tracer = MagicMock()

    @contextmanager
    def _span(name: str, **_attrs: object) -> Generator[MagicMock]:
        calls.append("tracing")
        spans.append(name)
        yield MagicMock()

    tracer.span.side_effect = _span
    return tracer


@pytest.mark.asyncio
async def test_service_op_chain_matches_the_service_builder_order() -> None:
    """service_op_chain wraps a custom op in ServiceBuilder's order, recovery outermost.

    **Why this test is important:**
      - A domain op must be authorized, observed and recovered exactly like CRUD

    **What it tests:**
      - Entry order: tracing → metrics → authorization → logging → op; an uncoded error is coded
    """
    calls: list[str] = []
    spans: list[str] = []
    executions = MagicMock()
    executions.inc.side_effect = lambda *_, **__: calls.append("metrics")
    logger = MagicMock()
    logger.debug.side_effect = lambda msg, **_: (
        calls.append("logging") if not msg.endswith("failed") else None
    )

    authorize = AsyncMock(
        side_effect=lambda _action: (calls.append("authorization"), True)[-1]
    )

    deps = ServiceOpDeps(
        logger=logger,
        tracer=_span_recorder(calls, spans),
        histogram=MagicMock(),
        executions=executions,
        authorize=authorize,
        timeout=1.0,
    )
    mw = service_op_chain("projects", deps)

    op = AsyncMock(side_effect=lambda: (calls.append("op"), "archived")[-1])

    assert await exec_op(mw, "archive", op) == "archived"
    assert calls == ["tracing", "metrics", "authorization", "logging", "op"]
    assert spans == ["service.archive"]

    boom = AsyncMock(side_effect=RuntimeError)

    with pytest.raises(InternalError):
        await exec_op(mw, "archive", boom)


@pytest.mark.asyncio
async def test_service_op_chain_denies_unauthorized_ops() -> None:
    """A refused action raises ForbiddenError and never runs the op.

    **Why this test is important:**
      - Custom ops were previously unauthorized unless each consumer hand-rolled a check

    **What it tests:**
      - ForbiddenError; op not awaited
    """
    op = AsyncMock()
    mw = service_op_chain(
        "projects", ServiceOpDeps(authorize=AsyncMock(return_value=False))
    )
    with pytest.raises(ForbiddenError):
        await exec_op(mw, "archive", op)
    op.assert_not_awaited()


class _ProjectService(DelegatingCrudService[dict[str, str], str, str]):
    """A consumer service: engine CRUD by delegation plus one domain op."""

    def __init__(self, crud: CrudService[dict[str, str], str, str]) -> None:
        super().__init__(crud)
        self._chain = service_op_chain("projects", ServiceOpDeps())

    async def archive(self, entity_id: str) -> str:
        return await exec_op(self._chain, "archive", lambda: self._archive(entity_id))

    async def _archive(self, entity_id: str) -> str:
        if entity_id == "done":
            msg = "already archived"
            raise ConflictError(msg)
        return entity_id


@pytest.mark.asyncio
async def test_delegating_crud_service_forwards_crud_and_hosts_domain_ops() -> None:
    """A DelegatingCrudService subclass serves CRUD from the decorated service and adds its own op.

    **Why this test is important:**
      - It is Python's equivalent of Go's embedded DecoratedService: no hand-written CRUD

    **What it tests:**
      - Each CRUD call reaches the inner service; the domain op keeps its error code
    """
    crud = AsyncMock(spec=CrudService)
    crud.get.return_value = {"id": "1"}
    crud.list.return_value = Page(items=[], total=0)
    crud.create.return_value = {"id": "2"}
    crud.update.return_value = {"id": "1"}
    svc = _ProjectService(crud)
    page = PageRequest()

    assert await svc.get("1") == {"id": "1"}
    assert (await svc.list("org", page)).total == 0
    assert await svc.create({}) == {"id": "2"}
    assert await svc.update("1", {}) == {"id": "1"}
    await svc.delete("1")
    assert await svc.archive("1") == "1"
    with pytest.raises(ConflictError):
        await svc.archive("done")
    crud.delete.assert_awaited_once_with("1")


def _repo() -> AsyncMock:
    """Return a Repository mock whose writes echo the entity."""
    repo = AsyncMock(spec=Repository)
    repo.create.side_effect = lambda e: e
    repo.update.side_effect = lambda _id, e: e
    repo.get.return_value = {"name": "x"}
    return repo


def _require_name(entity: dict[str, str]) -> None:
    """Raise InvalidInputError when the entity has no name."""
    if not entity.get("name"):
        msg = "name required"
        raise InvalidInputError(msg)


@pytest.mark.asyncio
async def test_with_validation_rejects_before_the_repository() -> None:
    """with_validation runs on create/update and stops an invalid entity before persistence.

    **Why this test is important:**
      - Go's WithValidation parity: business-rule validation is a decorator, not inline code

    **What it tests:**
      - Invalid create/update raise InvalidInputError without touching the repo; valid ones pass
      - Reads and deletes pass through unvalidated
    """
    repo = _repo()
    svc = (
        ServiceBuilder(BaseCrudService(repo), "projects")
        .with_validation(_require_name)
        .build()
    )

    with pytest.raises(InvalidInputError):
        await svc.create({})
    with pytest.raises(InvalidInputError):
        await svc.update("1", {})
    repo.create.assert_not_awaited()
    assert await svc.create({"name": "a"}) == {"name": "a"}
    assert await svc.update("1", {"name": "b"}) == {"name": "b"}
    assert await svc.get("1") == {"name": "x"}
    await svc.list("org", PageRequest())
    await svc.delete("1")
    repo.delete.assert_awaited_once_with("1")


@pytest.mark.asyncio
async def test_with_metrics_and_tracing_observe_every_crud_op() -> None:
    """with_metrics and with_tracing record each CRUD op under service/op labels and spans.

    **Why this test is important:**
      - Python CRUD services previously emitted no service-tier metrics or spans

    **What it tests:**
      - One observation and one span per op, named service.<op>
    """
    hist = MagicMock()
    calls: list[str] = []
    spans: list[str] = []
    svc = (
        ServiceBuilder(BaseCrudService(_repo()), "projects")
        .with_metrics(hist)
        .with_tracing(_span_recorder(calls, spans))
        .build()
    )

    await svc.get("1")
    await svc.list("org", PageRequest())
    await svc.create({"name": "a"})
    await svc.update("1", {"name": "a"})
    await svc.delete("1")
    assert spans == [
        "service.get",
        "service.list",
        "service.create",
        "service.update",
        "service.delete",
    ]
    ops = [c.kwargs["op"] for c in hist.observe.call_args_list]
    assert ops == ["get", "list", "create", "update", "delete"]
    assert {c.kwargs["service"] for c in hist.observe.call_args_list} == {"projects"}
