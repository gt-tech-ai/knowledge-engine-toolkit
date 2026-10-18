"""The workflow tier (mirrors Go ``workflows/workflow``).

``tier()`` orders the tier's layers. ``WorkflowBase``, built from the workflow's contract, is
the core of a workflow, the only tier that opens a transaction: it runs each contract method as a
read (``exec``, no transaction), a command (``exec_tx``, one transaction) or a saga (``exec``
whose body commits each local step with ``in_tx``).
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import InternalError
from techai_webutils.core.interfaces.repository import in_transaction, marking_transaction
from techai_webutils.foundation.decorate import Layer, Tier

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.repository import TransactionManager
    from techai_webutils.foundation.decorate import Decorations


def tier() -> Tier:
    """Return the workflow tier, outermost first: recovery, logging, tracing, metrics, timeout."""
    return Tier(
        "workflow",
        (Layer.RECOVERY, Layer.LOGGING, Layer.TRACING, Layer.METRICS, Layer.TIMEOUT),
    )


class WorkflowBase:
    """Runs one workflow's operations through the workflow tier, owning their transactions."""

    __slots__ = ("_subject", "_tx")

    def __init__(
        self,
        contract: type,
        decorations: Decorations,
        name: str,
        tx: TransactionManager | None = None,
    ) -> None:
        """Bind the workflow ``name`` over ``tx`` (Go ``NewBase[W]``).

        One chain per method of ``contract``, each resolved as ``<name>.<op>``; ``tx`` is needed
        only by ``exec_tx`` and ``in_tx``.
        """
        self._subject = decorations.subject_of(tier(), name, contract)
        self._tx = tx

    @property
    def name(self) -> str:
        """The workflow's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run ``fn`` as the operation ``op`` through the tier, with no transaction (a read).

        A saga is an ``exec`` whose ``fn`` commits each local step with ``in_tx``.
        """
        return await self._subject.exec(op, fn)

    async def exec_tx[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run ``fn`` as the command ``op``: one decorated operation in one transaction.

        Every repository call ``fn`` makes joins the transaction, which commits when ``fn``
        returns and rolls back when it raises; the error reaches the caller unchanged.
        """
        return await self.exec(op, lambda: self.in_tx(fn))

    async def in_tx[R](self, fn: Callable[[], Awaitable[R]]) -> R:
        """Run ``fn`` in one transaction, undecorated: a saga's local step.

        Inside a transaction, or on a base with no transaction manager, it raises
        ``InternalError`` without running ``fn``: a transaction never opens inside another.
        """
        if in_transaction():
            msg = f"workflow {self.name}: a transaction is already open"
            raise InternalError(msg)
        tx = self._tx
        if tx is None:
            msg = f"workflow {self.name}: no transaction manager"
            raise InternalError(msg)

        async def run() -> R:
            with marking_transaction():
                return await fn()

        return await tx.with_transaction(run)
