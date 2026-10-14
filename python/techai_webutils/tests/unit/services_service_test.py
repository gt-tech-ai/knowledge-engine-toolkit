"""Tests for the service tier: ``ServiceBase`` decorates services (Go ``services/service``)."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.errors.errors import (
    AppTimeoutError,
    ForbiddenError,
    InternalError,
    InvalidInputError,
)
from techai_webutils.core.interfaces.repository import Repository
from techai_webutils.foundation.decorate import Deps, Layer
from techai_webutils.services import DelegatingCrudService, ServiceBase, tier

if TYPE_CHECKING:
    from conftest import Observed


def _repo() -> MagicMock:
    """Return a Repository mock answering every CRUD call."""
    repo = MagicMock(spec=Repository)
    repo.get = AsyncMock(return_value="e1")
    repo.list = AsyncMock(return_value=Page(items=["e1"], total=1))
    repo.create = AsyncMock(side_effect=lambda e: e)
    repo.update = AsyncMock(side_effect=lambda _id, e: e)
    repo.delete = AsyncMock(return_value=None)
    return repo


def _reject_blank(entity: str) -> None:
    """Validate an entity: a blank one is invalid input."""
    if not entity:
        msg = "entity must not be blank"
        raise InvalidInputError(msg)


def test_tier_order_matches_go() -> None:
    """The service tier's layers, outermost first, are Go's.

    Why this test is important:
      - Both languages must decorate a service the same way.

    What it tests:
      - recovery → tracing → metrics → logging → auth → timeout.
    """
    assert tier().order == (
        Layer.RECOVERY,
        Layer.TRACING,
        Layer.METRICS,
        Layer.LOGGING,
        Layer.AUTH,
        Layer.TIMEOUT,
    )


@pytest.mark.asyncio
async def test_crud_runs_through_the_tier_and_validates(observed: Observed) -> None:
    """CRUD reaches the repository under its operation names; Create and Update validate first.

    Why this test is important:
      - An invalid entity must be rejected before the repository sees it, with a coded error.

    What it tests:
      - Each call returns the repository's answer in an ``orders.<Op>`` span; a blank entity
        raises ``InvalidInputError`` from Create and Update without reaching the repository.
    """
    repo = _repo()
    svc = ServiceBase("orders", Deps(observability=observed.observability)).crud(
        repo, _reject_blank
    )

    assert await svc.get("1") == "e1"
    assert (await svc.list(None, PageRequest())).total == 1
    assert await svc.create("e") == "e"
    assert await svc.update("1", "e") == "e"
    await svc.delete("1")
    with pytest.raises(InvalidInputError):
        await svc.create("")
    with pytest.raises(InvalidInputError):
        await svc.update("1", "")

    assert repo.create.await_count == 1
    assert repo.update.await_count == 1
    ops = ["Get", "List", "Create", "Update", "Delete", "Create", "Update"]
    assert [name for name, _, _ in observed.spans] == [f"orders.{op}" for op in ops]


@pytest.mark.asyncio
async def test_auth_authorizes_the_service_action() -> None:
    """The auth layer authorizes ``<service>.<op>`` and refuses with FORBIDDEN.

    Why this test is important:
      - Authorization is per operation; a refused action must never reach the repository.

    What it tests:
      - The authorizer sees ``orders.Delete``; a refusal raises ``ForbiddenError`` and the
        repository is not called.
    """
    auth = AsyncMock(return_value=False)
    repo = _repo()
    svc = ServiceBase("orders", Deps(auth=auth)).crud(repo)

    with pytest.raises(ForbiddenError):
        await svc.delete("1")
    auth.assert_awaited_once_with("orders.Delete")
    repo.delete.assert_not_awaited()


@pytest.mark.asyncio
async def test_domain_operations_and_op_services() -> None:
    """``exec`` and ``op_service`` run domain work through the tier.

    Why this test is important:
      - Non-CRUD operations need the same recovery and budget as CRUD.

    What it tests:
      - ``op_service`` runs as ``Run`` and reports its name; an uncoded failure in ``exec``
        becomes ``InternalError``; a slow operation times out.
    """
    base = ServiceBase("billing", Deps(timeout=0.01))
    charge = base.op_service(AsyncMock(return_value="charged"))

    async def slow() -> None:
        await asyncio.sleep(1)

    assert await charge.run({"amount": 1}) == "charged"
    assert charge.name == "billing"
    assert base.name == "billing"
    with pytest.raises(InternalError):
        await base.exec("Refund", AsyncMock(side_effect=ValueError("x")))
    with pytest.raises(AppTimeoutError):
        await base.exec("Slow", slow)


@pytest.mark.asyncio
async def test_delegating_service_serves_crud_from_the_decorated_one() -> None:
    """A consumer service inherits decorated CRUD by delegation.

    Why this test is important:
      - A consumer adds only its domain operations; CRUD must still run through the tier.

    What it tests:
      - Every CRUD call on a ``DelegatingCrudService`` reaches the inner service.
    """
    svc = DelegatingCrudService(ServiceBase("orders", Deps()).crud(_repo()))

    assert await svc.get("1") == "e1"
    assert (await svc.list(None, PageRequest())).items == ["e1"]
    assert await svc.create("e") == "e"
    assert await svc.update("1", "e") == "e"
    await svc.delete("1")
