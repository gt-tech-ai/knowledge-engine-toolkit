"""Tests for the repository custom-op chain and DelegatingRepository (Go ``repository.OpChain`` + embedding)."""

from __future__ import annotations

from contextlib import contextmanager
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.domain_types.types import Page, PageRequest
from techai_webutils.core.interfaces.repository import Repository
from techai_webutils.foundation.decorate import exec_op
from techai_webutils.repos.op_chain import RepoOpDeps, repo_op_chain
from techai_webutils.repos.repository import DelegatingRepository

if TYPE_CHECKING:
    from collections.abc import Generator


def _recording_deps(calls: list[str]) -> RepoOpDeps:
    """Build deps whose collaborators append their layer name on entry."""
    logger = MagicMock()
    logger.debug.side_effect = lambda msg, **_: (
        calls.append("logging") if not msg.endswith("failed") else None
    )
    executions = MagicMock()
    executions.inc.side_effect = lambda *_, **__: calls.append("metrics")
    tracer = MagicMock()

    @contextmanager
    def _span(_name: str, **_attrs: object) -> Generator[MagicMock]:
        calls.append("tracing")
        yield MagicMock()

    tracer.span.side_effect = _span
    retrier = MagicMock()

    async def _retry(op: object) -> object:
        calls.append("retry")
        return await op()  # type: ignore[operator]

    retrier.retry = AsyncMock(side_effect=_retry)
    breaker = MagicMock()
    breaker.__enter__.side_effect = lambda: calls.append("circuit_breaker")
    return RepoOpDeps(
        logger=logger,
        tracer=tracer,
        histogram=MagicMock(),
        executions=executions,
        retrier=retrier,
        breaker=breaker,
        timeout=1.0,
    )


@pytest.mark.asyncio
async def test_repo_op_chain_matches_the_repository_builder_order() -> None:
    """repo_op_chain wraps a custom op in RepositoryBuilder's order.

    **Why this test is important:**
      - A custom query must get the same stack, in the same order, as the CRUD ops

    **What it tests:**
      - Entry order: logging → metrics → tracing → (timeout) → retry → circuit breaker → op
    """
    calls: list[str] = []
    mw = repo_op_chain("projects", _recording_deps(calls))

    op = AsyncMock(side_effect=lambda: (calls.append("op"), "row")[-1])

    assert await exec_op(mw, "get_by_slug", op) == "row"
    assert calls == ["logging", "metrics", "tracing", "retry", "circuit_breaker", "op"]


@pytest.mark.asyncio
async def test_repo_op_chain_with_no_collaborators_is_a_pass_through() -> None:
    """Empty RepoOpDeps builds a chain that just runs the op.

    **Why this test is important:**
      - The graph must boot with every collaborator absent

    **What it tests:**
      - The op result is returned
    """

    op = AsyncMock(return_value=3)

    assert await exec_op(repo_op_chain("projects", RepoOpDeps()), "count", op) == 3


class _ProjectRepository(DelegatingRepository[dict[str, str], str, str]):
    """A consumer repository: engine CRUD by delegation plus one custom query."""

    def __init__(
        self, crud: Repository[dict[str, str], str, str], lookup: AsyncMock
    ) -> None:
        super().__init__(crud)
        self._chain = repo_op_chain("projects", RepoOpDeps())
        self._lookup = lookup

    async def get_by_slug(self, slug: str) -> dict[str, str]:
        return await exec_op(self._chain, "get_by_slug", lambda: self._lookup(slug))


@pytest.mark.asyncio
async def test_delegating_repository_forwards_crud_and_hosts_custom_ops() -> None:
    """A DelegatingRepository subclass serves CRUD from the decorated repo and adds its own op.

    **Why this test is important:**
      - It is Python's equivalent of Go's embedded DecoratedRepository: no hand-written CRUD

    **What it tests:**
      - Each CRUD call reaches the inner repository; the custom op runs its own body
    """
    crud = AsyncMock(spec=Repository)
    crud.get.return_value = {"id": "1"}
    crud.list.return_value = Page(items=[], total=0)
    crud.create.return_value = {"id": "2"}
    crud.update.return_value = {"id": "1"}
    crud.exists.return_value = True
    repo = _ProjectRepository(crud, AsyncMock(return_value={"slug": "a"}))
    page = PageRequest()

    assert await repo.get("1") == {"id": "1"}
    assert (await repo.list("org", page)).total == 0
    assert await repo.create({}) == {"id": "2"}
    assert await repo.update("1", {}) == {"id": "1"}
    await repo.delete("1")
    assert await repo.exists("1") is True
    assert await repo.get_by_slug("a") == {"slug": "a"}
    crud.list.assert_awaited_once_with("org", page)
    crud.delete.assert_awaited_once_with("1")
