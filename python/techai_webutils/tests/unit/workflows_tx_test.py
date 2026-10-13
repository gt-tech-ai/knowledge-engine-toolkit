"""Tests for workflows.tx_workflow (Go ``workflow.NewTxWorkflow``)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.errors.errors import ConflictError
from techai_webutils.core.interfaces.repository import TransactionManager
from techai_webutils.pipelines.base import BaseAsyncPipeline
from techai_webutils.workflows import tx_workflow


def _tx_manager(events: list[str]) -> MagicMock:
    """Return a TransactionManager mock that records begin/commit/rollback around fn."""
    tx = MagicMock(spec=TransactionManager)

    async def _with_transaction(fn: object) -> object:
        events.append("begin")
        try:
            result = await fn()  # type: ignore[operator]
        except Exception:
            events.append("rollback")
            raise
        events.append("commit")
        return result

    tx.with_transaction.side_effect = _with_transaction
    return tx


@pytest.mark.asyncio
async def test_tx_workflow_runs_the_pipeline_inside_one_transaction() -> None:
    """tx_workflow executes the pipeline inside with_transaction and returns its output.

    **Why this test is important:**
      - A command's reads and writes must commit or roll back together

    **What it tests:**
      - Order begin → pipeline → commit; the output is returned
    """
    events: list[str] = []

    step = AsyncMock(side_effect=lambda x: (events.append("pipeline"), x + 1)[-1])

    wf = tx_workflow(_tx_manager(events), BaseAsyncPipeline(step))
    assert await wf.execute(1) == 2
    assert events == ["begin", "pipeline", "commit"]


@pytest.mark.asyncio
async def test_tx_workflow_rolls_back_and_keeps_the_error_code() -> None:
    """A failing pipeline rolls the transaction back and its coded error reaches the caller.

    **Why this test is important:**
      - A result whose transaction did not commit must never reach the caller

    **What it tests:**
      - ConflictError propagates; the transaction rolled back
    """
    events: list[str] = []

    fail = AsyncMock(side_effect=ConflictError("dup"))

    with pytest.raises(ConflictError):
        await tx_workflow(_tx_manager(events), BaseAsyncPipeline(fail)).execute(1)
    assert events == ["begin", "rollback"]
