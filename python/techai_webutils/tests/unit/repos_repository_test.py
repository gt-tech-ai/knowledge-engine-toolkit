"""Tests for the repository tier: ``RepositoryBase`` decorates a Store's CRUD (Go ``repos/repository``)."""

from __future__ import annotations

from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.errors.errors import NotFoundError
from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.core.interfaces.store import Store
from techai_webutils.foundation.decorate import Deps, Layer
from techai_webutils.repos import DelegatingRepository, RepositoryBase, tier

if TYPE_CHECKING:
    from conftest import Observed


def _store() -> MagicMock:
    """Return a Store mock answering every CRUD call."""
    store = MagicMock(spec=Store)
    store.get = AsyncMock(return_value="e1")
    store.list = AsyncMock(return_value=Page(items=["e1"], total=1))
    store.create = AsyncMock(side_effect=lambda e: e)
    store.update = AsyncMock(side_effect=lambda _id, e: e)
    store.delete = AsyncMock(return_value=None)
    store.exists = AsyncMock(return_value=True)
    return store


def _retrier() -> MagicMock:
    """Return a Retrier mock that runs the operation once, counting its calls."""
    retrier = MagicMock(spec=Retrier)

    async def retry(op: object) -> object:
        return await op()  # type: ignore[operator]

    retrier.retry = AsyncMock(side_effect=retry)
    return retrier


def test_tier_order_matches_go() -> None:
    """The repository tier's layers, outermost first, are Go's.

    Why this test is important:
      - Each retry attempt must pass the breaker and get a fresh deadline, observed once per call.

    What it tests:
      - tracing → metrics → logging → timeout → circuit breaker → retry.
    """
    assert tier().order == (
        Layer.TRACING,
        Layer.METRICS,
        Layer.LOGGING,
        Layer.TIMEOUT,
        Layer.CIRCUIT_BREAKER,
        Layer.RETRY,
    )


@pytest.mark.asyncio
async def test_crud_runs_each_call_through_the_tier(observed: Observed) -> None:
    """Every CRUD call reaches the store and is observed under its operation name.

    Why this test is important:
      - Repository dashboards break down latency by operation (Go names: Get, List, …).

    What it tests:
      - Each call returns the store's answer, and the spans are ``users.<Op>`` in call order.
    """
    store = _store()
    repo = RepositoryBase("users", Deps(observability=observed.observability)).crud(store)
    page = PageRequest()

    assert await repo.get("1") == "e1"
    assert (await repo.list(None, page)).items == ["e1"]
    assert await repo.create("e2") == "e2"
    assert await repo.update("1", "e3") == "e3"
    await repo.delete("1")
    assert await repo.exists("1") is True

    ops = ["Get", "List", "Create", "Update", "Delete", "Exists"]
    assert [name for name, _, _ in observed.spans] == [f"users.{op}" for op in ops]


@pytest.mark.asyncio
async def test_create_is_never_retried() -> None:
    """Create bypasses the retry layer; every other operation goes through it.

    Why this test is important:
      - A create whose commit landed before a transient error must not insert twice.

    What it tests:
      - With a retrier, Get calls it and Create does not.
    """
    retrier = _retrier()
    repo = RepositoryBase("users", Deps(retrier=retrier)).crud(_store())

    await repo.get("1")
    await repo.create("e")

    assert retrier.retry.await_count == 1


@pytest.mark.asyncio
async def test_errors_pass_unchanged_and_custom_queries_are_decorated(
    observed: Observed,
) -> None:
    """A store's coded error reaches the caller unchanged; ``exec`` decorates a custom query.

    Why this test is important:
      - Services branch on NOT_FOUND; a custom query must be observed like CRUD.

    What it tests:
      - ``NotFoundError`` propagates from Get; ``exec("FindByEmail", …)`` returns the query's
        result in a ``users.FindByEmail`` span; the base reports its name.
    """
    store = _store()
    store.get.side_effect = NotFoundError("user", id="1")
    base = RepositoryBase("users", Deps(observability=observed.observability))

    with pytest.raises(NotFoundError):
        await base.crud(store).get("1")
    assert await base.exec("FindByEmail", AsyncMock(return_value="u")) == "u"
    assert observed.spans[-1][0] == "users.FindByEmail"
    assert base.name == "users"


@pytest.mark.asyncio
async def test_delegating_repository_serves_crud_from_the_decorated_one() -> None:
    """A consumer repository inherits decorated CRUD by delegation.

    Why this test is important:
      - A consumer adds only its custom queries; CRUD must still run through the tier.

    What it tests:
      - Every CRUD call on a ``DelegatingRepository`` reaches the inner repository.
    """
    inner = RepositoryBase("users", Deps()).crud(_store())
    repo = DelegatingRepository(inner)

    assert await repo.get("1") == "e1"
    assert (await repo.list(None, PageRequest())).total == 1
    assert await repo.create("e") == "e"
    assert await repo.update("1", "e") == "e"
    await repo.delete("1")
    assert await repo.exists("1") is True
