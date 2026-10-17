"""The workflow tier (mirrors Go ``workflows/workflow``).

``tier()`` orders the tier's layers; ``WorkflowBase`` builds decorated workflows — one span,
metric series and log line per workflow — from a function (``func``), a workflow (``wrap``), a
read pipeline (``read``) or a pipeline run inside one transaction (``tx``); ``run_tx`` runs a
saga's local step in one transaction, undecorated.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.workflow import AsyncWorkflow
from techai_webutils.foundation.decorate import Deps, Layer, Tier

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.pipeline import AsyncPipeline
    from techai_webutils.core.interfaces.repository import TransactionManager
    from techai_webutils.foundation.decorate import Decorations


def tier() -> Tier:
    """Return the workflow tier, outermost first: recovery, logging, tracing, metrics, timeout."""
    return Tier(
        "workflow",
        (Layer.RECOVERY, Layer.LOGGING, Layer.TRACING, Layer.METRICS, Layer.TIMEOUT),
    )


class _Flow[In, Out](AsyncWorkflow[In, Out]):
    """A workflow built from a function."""

    __slots__ = ("_fn",)

    def __init__(self, fn: Callable[[In], Awaitable[Out]]) -> None:
        """Bind the workflow's function."""
        self._fn = fn

    @override
    async def execute(self, input_data: In) -> Out:
        """Run the workflow."""
        return await self._fn(input_data)


class WorkflowBase:
    """Builds decorated workflows: each runs as ``Execute`` through the tier under its name."""

    __slots__ = ("_deps", "_tx")

    def __init__(
        self, tx: TransactionManager | None = None, deps: Deps | None = None
    ) -> None:
        """Bind the transaction manager (for ``tx``) and the workflows' collaborators."""
        self._tx = tx
        self._deps = deps or Deps()

    @classmethod
    def from_decorations(
        cls,
        name: str,
        decorations: Decorations,
        tx: TransactionManager | None = None,
    ) -> WorkflowBase:
        """Return the base whose workflows get the layers the config resolves for ``name``."""
        return cls(tx, decorations.deps(tier(), name))

    def func[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> AsyncWorkflow[In, Out]:
        """Return ``fn`` as the workflow ``op``, run through the tier.

        A None function is a wiring bug and raises ``AppValueError``.
        """
        if fn is None:  # pyright: ignore[reportUnnecessaryComparison]
            msg = "workflow: fn must not be None"
            raise AppValueError(msg)
        subject = tier().subject(op, self._deps)

        async def run(input_data: In) -> Out:
            return await subject.exec("Execute", lambda: fn(input_data))

        return _Flow(run)

    def wrap[In, Out](
        self, op: str, wf: AsyncWorkflow[In, Out]
    ) -> AsyncWorkflow[In, Out]:
        """Return the workflow ``wf`` run through the tier as ``op``."""
        return self.func(op, wf.execute)

    def read[In, Out](
        self, op: str, pipe: AsyncPipeline[In, Out]
    ) -> AsyncWorkflow[In, Out]:
        """Return the read pipeline ``pipe`` as the workflow ``op`` (no transaction)."""
        return self.func(op, pipe.execute)

    def tx[In, Out](
        self, op: str, pipe: AsyncPipeline[In, Out]
    ) -> AsyncWorkflow[In, Out]:
        """Return the workflow ``op`` that runs ``pipe`` inside one transaction.

        Every repository call the pipeline makes joins the transaction, which commits when the
        pipeline succeeds and rolls back when it raises; the pipeline's error reaches the caller
        unchanged. A base without a transaction manager raises ``AppValueError``.
        """
        tx = self._manager()

        async def run(input_data: In) -> Out:
            return await tx.with_transaction(lambda: pipe.execute(input_data))

        return self.func(op, run)

    async def run_tx[In, Out](self, pipe: AsyncPipeline[In, Out], input_data: In) -> Out:
        """Run ``pipe`` on ``input_data`` inside one transaction, as ``tx`` does, undecorated.

        It is a local step of a saga already decorated by ``func`` or ``wrap`` that commits a
        write between remote calls (or compensates one), so the request stays one decorated
        workflow. A base without a transaction manager raises ``AppValueError``.
        """
        tx = self._manager()
        return await tx.with_transaction(lambda: pipe.execute(input_data))

    def _manager(self) -> TransactionManager:
        """Return the transaction manager, raising ``AppValueError`` when the base has none."""
        if self._tx is None:
            msg = "workflow: tx needs a transaction manager"
            raise AppValueError(msg)
        return self._tx
