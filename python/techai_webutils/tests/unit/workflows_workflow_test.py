"""Tests for the workflow tier: ``WorkflowBase`` runs a workflow's operations (Go ``workflows/workflow``)."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Protocol
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.domain_types.types import PageRequest
from techai_webutils.core.errors.errors import (
    AppTimeoutError,
    ConflictError,
    InternalError,
)
from techai_webutils.core.interfaces.crud_service import CrudService
from techai_webutils.core.interfaces.repository import TransactionManager, in_transaction
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations, Deps, Layer
from techai_webutils.workflows import WorkflowBase, tier

if TYPE_CHECKING:
    from conftest import Observed


class _Flows(Protocol):
    """The test workflows' contract: one operation per shape tested."""

    async def run(self) -> None:
        """Run."""
        ...

    async def op(self) -> None:
        """Run an operation."""
        ...

    async def read(self) -> None:
        """Read."""
        ...

    async def command(self) -> None:
        """Run a command."""
        ...

    async def saga(self) -> None:
        """Run a saga."""
        ...

    async def inner(self) -> None:
        """Run a nested command."""
        ...


def _tx_manager(events: list[str]) -> MagicMock:
    """Return a TransactionManager mock recording begin/commit/rollback around ``fn``."""
    tx = MagicMock(spec=TransactionManager)

    async def with_transaction(fn: object) -> object:
        events.append("begin")
        try:
            result = await fn()  # type: ignore[operator]
        except Exception:
            events.append("rollback")
            raise
        events.append("commit")
        return result

    tx.with_transaction.side_effect = with_transaction
    return tx


def test_tier_order_matches_go() -> None:
    """The workflow tier's layers, outermost first, are Go's.

    Why this test is important:
      - Both languages must decorate a workflow the same way, so a span, a log line and a
        recovered panic mean the same thing in either.

    What it tests:
      - recovery → logging → tracing → metrics → timeout.
    """
    assert tier().order == (
        Layer.RECOVERY,
        Layer.LOGGING,
        Layer.TRACING,
        Layer.METRICS,
        Layer.TIMEOUT,
    )


@pytest.mark.asyncio
async def test_exec_runs_an_operation_through_every_layer(observed: Observed) -> None:
    """An operation runs under the workflow's name and the operation's own.

    Why this test is important:
      - Each workflow operation is one span, one metric series and one log line on every
        dashboard, labelled like every other tier's.

    What it tests:
      - The result is returned; the span is ``<name>.<op>`` with the workflow attributes;
        the series are labelled ``workflow=<name>, operation=<op>``; the entry log line is
        ``workflow.<op>``.
    """
    base = WorkflowBase(
        _Flows, Decorations.fixed(Deps(observability=observed.observability)), "ingest"
    )

    assert await base.exec("run", AsyncMock(return_value=2)) == 2
    [(name, _, attributes)] = observed.spans
    assert name == "ingest.run"
    assert attributes == {"workflow.name": "ingest", "workflow.operation": "run"}
    observed.instruments["workflow_operations_total"].inc.assert_called_once_with(
        1.0, workflow="ingest", operation="run"
    )
    assert observed.logger.debug.call_args.args[0] == "workflow.run"
    assert base.name == "ingest"


@pytest.mark.asyncio
async def test_recovery_and_timeout() -> None:
    """An unexpected exception is coded INTERNAL; a slow operation times out.

    Why this test is important:
      - A workflow must never leak an uncoded exception, and its budget bounds the work.

    What it tests:
      - A ``RuntimeError`` surfaces as ``InternalError``; a coded error passes unchanged; an
        operation slower than the timeout raises ``AppTimeoutError``.
    """
    base = WorkflowBase(_Flows, Decorations.fixed(Deps(timeout=0.01)), "w")

    async def slow() -> int:
        await asyncio.sleep(1)
        return 0

    with pytest.raises(InternalError):
        await base.exec("op", AsyncMock(side_effect=RuntimeError("boom")))
    with pytest.raises(ConflictError):
        await base.exec("op", AsyncMock(side_effect=ConflictError("dup")))
    with pytest.raises(AppTimeoutError):
        await base.exec("op", slow)


@pytest.mark.asyncio
async def test_read_command_and_saga_shapes() -> None:
    """``exec`` opens no transaction; ``exec_tx`` and ``in_tx`` each open one.

    Why this test is important:
      - A command's reads and writes must commit or roll back together, and a read must not
        hold a transaction it does not need.

    What it tests:
      - ``exec`` runs outside a transaction; ``exec_tx`` (a command) and ``in_tx`` (a saga
        step) each run begin → body → commit with ``in_transaction()`` true in the body, and
        return the body's result.
    """
    events: list[str] = []
    base = WorkflowBase(_Flows, Decorations(), "w", _tx_manager(events))

    async def body() -> bool:
        await asyncio.sleep(0)
        events.append("body")
        return in_transaction()

    assert await base.exec("read", body) is False
    assert await base.exec_tx("command", body) is True

    async def saga() -> bool:
        return await base.in_tx(body)

    assert await base.exec("saga", saga) is True
    assert events == ["body", "begin", "body", "commit", "begin", "body", "commit"]
    assert in_transaction() is False


@pytest.mark.asyncio
async def test_a_failing_body_rolls_back_and_keeps_the_error_code() -> None:
    """A failing body rolls its transaction back and its coded error reaches the caller.

    Why this test is important:
      - A result whose transaction did not commit must never reach the caller.

    What it tests:
      - ``ConflictError`` propagates from ``exec_tx`` and ``in_tx`` and each transaction
        rolled back.
    """
    events: list[str] = []
    fail = AsyncMock(side_effect=ConflictError("dup"))
    base = WorkflowBase(_Flows, Decorations(), "w", _tx_manager(events))

    with pytest.raises(ConflictError):
        await base.exec_tx("command", fail)
    with pytest.raises(ConflictError):
        await base.in_tx(fail)
    assert events == ["begin", "rollback"] * 2


@pytest.mark.asyncio
async def test_a_nested_or_unmanaged_transaction_fails_loud() -> None:
    """A transaction never opens inside another, nor without a manager.

    Why this test is important:
      - A nested transaction runs on a second connection and commits on its own, so the
        outer one's failure leaves the inner write behind.

    What it tests:
      - ``in_tx`` or ``exec_tx`` inside a transaction raises ``InternalError`` without
        opening a second one; ``exec_tx`` on a base with no manager raises
        ``InternalError``.
    """
    events: list[str] = []
    base = WorkflowBase(_Flows, Decorations(), "w", _tx_manager(events))
    never = AsyncMock()

    async def nested_step() -> None:
        await base.in_tx(never)

    async def nested_command() -> None:
        await base.exec_tx("inner", never)

    with pytest.raises(InternalError, match="already open"):
        await base.in_tx(nested_step)
    with pytest.raises(InternalError, match="already open"):
        await base.in_tx(nested_command)
    with pytest.raises(InternalError, match="transaction manager"):
        await WorkflowBase(_Flows, Decorations(), "w").exec_tx("command", never)
    never.assert_not_called()
    assert events == ["begin", "rollback"] * 2


@pytest.mark.asyncio
async def test_workflow_base_applies_the_config(observed: Observed) -> None:
    """The decoration config's ``workflows`` tier switches a workflow's layers by name.

    Why this test is important:
      - Decorators are configured, not hard-wired: a per-workflow override must take effect.

    What it tests:
      - With tracing off for ``quiet``, ``quiet`` opens no span while ``loud`` does.
    """
    config = decoration.from_mapping({
        "workflows": {"overrides": {"quiet": {"tracing": False}}}
    })
    decorations = Decorations(config, Collaborators(observability=observed.observability))

    await WorkflowBase(_Flows, decorations, "quiet").exec("run", AsyncMock())
    await WorkflowBase(_Flows, decorations, "loud").exec("run", AsyncMock())

    assert [name for name, _, _ in observed.spans] == ["loud.run"]


@pytest.mark.asyncio
async def test_crud_reads_without_a_transaction_and_commits_each_write() -> None:
    """A CRUD workflow reads with no transaction and commits each write (Go ``Base.CRUD``).

    Why this test is important:
      - The workflow tier is the only one that opens a transaction: a write run outside one
        would be refused by every store, and a read that opened one would hold a connection
        for nothing.

    What it tests:
      - Get and List run with no transaction open; Create, Update and Delete each run in one
        transaction that commits (three in all).
    """
    seen: list[tuple[str, bool]] = []

    def answer(role: str, result: object) -> AsyncMock:
        def run(*_: object) -> object:
            seen.append((role, in_transaction()))
            return result

        return AsyncMock(side_effect=run)

    pipes = MagicMock(spec=CrudService)
    pipes.get = answer("get", "e1")
    pipes.list = answer("list", "page")
    pipes.create = answer("create", "e")
    pipes.update = answer("update", "e")
    pipes.delete = answer("delete", None)
    events: list[str] = []
    flows = WorkflowBase(
        CrudService, Decorations.fixed(Deps()), "widgets", _tx_manager(events)
    ).crud(pipes)

    assert await flows.get("1") == "e1"
    assert await flows.list(None, PageRequest()) == "page"
    assert await flows.create("e") == "e"
    assert await flows.update("1", "e") == "e"
    await flows.delete("1")
    assert seen == [
        ("get", False),
        ("list", False),
        ("create", True),
        ("update", True),
        ("delete", True),
    ]
    assert events == ["begin", "commit"] * 3
