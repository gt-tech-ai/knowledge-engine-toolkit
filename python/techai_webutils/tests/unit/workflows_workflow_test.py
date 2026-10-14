"""Tests for the workflow tier: ``WorkflowBase`` decorates workflows (Go ``workflows/workflow``)."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.errors.errors import (
    AppTimeoutError,
    ConflictError,
    InternalError,
)
from techai_webutils.core.interfaces.repository import TransactionManager
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations, Deps, Layer
from techai_webutils.pipelines import PipelineBase
from techai_webutils.workflows import WorkflowBase, tier

if TYPE_CHECKING:
    from conftest import Observed


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
async def test_func_runs_through_every_layer(observed: Observed) -> None:
    """A workflow built from a function runs as ``Execute`` under its name.

    Why this test is important:
      - Each workflow is one span, one metric series and one log line on every dashboard.

    What it tests:
      - The result is returned; the span is ``<name>.Execute`` with the workflow attributes;
        the series are the workflow tier's; the entry log line is ``workflow.Execute``.
    """
    base = WorkflowBase(deps=Deps(observability=observed.observability))
    wf = base.func("ingest", AsyncMock(return_value=2))

    assert await wf.execute(1) == 2
    [(name, _, attributes)] = observed.spans
    assert name == "ingest.Execute"
    assert attributes == {"workflow.name": "ingest", "workflow.operation": "Execute"}
    observed.instruments["workflow_operations_total"].inc.assert_called_once_with(
        1.0, workflow="ingest", operation="Execute"
    )
    assert observed.logger.debug.call_args.args[0] == "workflow.Execute"


@pytest.mark.asyncio
async def test_recovery_and_timeout() -> None:
    """An unexpected exception is coded INTERNAL; a slow workflow times out.

    Why this test is important:
      - A workflow must never leak an uncoded exception, and its budget bounds the work.

    What it tests:
      - A ``RuntimeError`` surfaces as ``InternalError``; a coded error passes unchanged; a
        workflow slower than the timeout raises ``AppTimeoutError``.
    """
    base = WorkflowBase(deps=Deps(timeout=0.01))

    async def slow(_: int) -> int:
        await asyncio.sleep(1)
        return 0

    with pytest.raises(InternalError):
        await base.func("w", AsyncMock(side_effect=RuntimeError("boom"))).execute(1)
    with pytest.raises(ConflictError):
        await base.func("w", AsyncMock(side_effect=ConflictError("dup"))).execute(1)
    with pytest.raises(AppTimeoutError):
        await base.func("w", slow).execute(1)


@pytest.mark.asyncio
async def test_wrap_and_read_decorate_existing_workflows_and_pipelines(
    observed: Observed,
) -> None:
    """``wrap`` decorates a workflow and ``read`` a read pipeline, each under its name.

    Why this test is important:
      - A consumer composes workflows from pipelines it already built; each must still be
        observed as a workflow.

    What it tests:
      - Both return the inner result and open ``<name>.Execute`` spans.
    """
    base = WorkflowBase(deps=Deps(observability=observed.observability))
    inner = WorkflowBase().func("inner", AsyncMock(return_value="w"))
    pipe = PipelineBase().stage("p", AsyncMock(return_value="r"))

    assert await base.wrap("outer", inner).execute(None) == "w"
    assert await base.read("query", pipe).execute(None) == "r"
    assert [name for name, _, _ in observed.spans] == ["outer.Execute", "query.Execute"]


@pytest.mark.asyncio
async def test_tx_runs_the_pipeline_inside_one_transaction() -> None:
    """``tx`` runs the pipeline inside one transaction, committed on success.

    Why this test is important:
      - A command's reads and writes must commit or roll back together.

    What it tests:
      - The order is begin → pipeline → commit, and the output is returned.
    """
    events: list[str] = []

    async def step(x: int) -> int:
        await asyncio.sleep(0)
        events.append("pipeline")
        return x + 1

    wf = WorkflowBase(_tx_manager(events)).tx("cmd", PipelineBase().stage("s", step))

    assert await wf.execute(1) == 2
    assert events == ["begin", "pipeline", "commit"]


@pytest.mark.asyncio
async def test_tx_rolls_back_and_keeps_the_error_code() -> None:
    """A failing pipeline rolls the transaction back and its coded error reaches the caller.

    Why this test is important:
      - A result whose transaction did not commit must never reach the caller.

    What it tests:
      - ``ConflictError`` propagates and the transaction rolled back.
    """
    events: list[str] = []
    fail = PipelineBase().stage("s", AsyncMock(side_effect=ConflictError("dup")))

    with pytest.raises(ConflictError):
        await WorkflowBase(_tx_manager(events)).tx("cmd", fail).execute(1)
    assert events == ["begin", "rollback"]


def test_wiring_bugs_fail_loud() -> None:
    """A missing function or transaction manager is a coded wiring error.

    Why this test is important:
      - A wiring bug must fail at the composition root, not on the first request.

    What it tests:
      - ``func`` with None and ``tx`` without a transaction manager raise ``AppValueError``.
    """
    with pytest.raises(AppValueError):
        WorkflowBase().func("w", None)  # type: ignore[arg-type]
    with pytest.raises(AppValueError, match="transaction manager"):
        WorkflowBase().tx("w", PipelineBase().stage("s", AsyncMock()))


@pytest.mark.asyncio
async def test_from_decorations_applies_the_config(observed: Observed) -> None:
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

    await (
        WorkflowBase
        .from_decorations("quiet", decorations)
        .func("quiet", AsyncMock())
        .execute(1)
    )
    await (
        WorkflowBase
        .from_decorations("loud", decorations)
        .func("loud", AsyncMock())
        .execute(1)
    )

    assert [name for name, _, _ in observed.spans] == ["loud.Execute"]
