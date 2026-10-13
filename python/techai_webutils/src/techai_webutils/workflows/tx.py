"""Transactional workflow (mirrors Go ``workflow.NewTxWorkflow``)."""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.workflows.base import BaseAsyncWorkflow

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.pipeline import AsyncPipeline
    from techai_webutils.core.interfaces.repository import TransactionManager
    from techai_webutils.core.interfaces.workflow import AsyncWorkflow


def tx_workflow[In, Out](
    tx: TransactionManager, pipe: AsyncPipeline[In, Out]
) -> AsyncWorkflow[In, Out]:
    """Return a workflow that runs ``pipe`` inside one transaction opened by ``tx``.

    Every repository call the pipeline makes joins the transaction, which commits when the
    pipeline succeeds and rolls back when it raises; the pipeline's (coded) error reaches the
    caller unchanged, so a result whose transaction did not commit never does. Decorate the
    returned workflow with ``AsyncWorkflowBuilder`` (recovery outermost).
    """

    async def run(input_data: In) -> Out:
        return await tx.with_transaction(lambda: pipe.execute(input_data))

    return BaseAsyncWorkflow(run)
