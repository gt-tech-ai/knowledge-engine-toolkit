"""The workflow tier (mirrors Go ``workflows/workflow``).

``tier()`` orders the tier's layers. ``WorkflowBase`` is the core of a workflow, the only tier
that opens a transaction: it runs each operation as a read (``exec``, no transaction), a command
(``exec_tx``, one transaction) or a saga (``exec`` whose body commits each local step with
``in_tx``).
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import InternalError
from techai_webutils.core.interfaces.repository import in_transaction, marking_transaction
from techai_webutils.foundation.decorate import Deps, Layer, Operation, Scope, Tier

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

    __slots__ = ("_ops", "_subject", "_tx")

    def __init__(
        self, name: str, tx: TransactionManager | None = None, deps: Deps | None = None
    ) -> None:
        """Bind the workflow ``name``'s subject and the manager its transactions open on.

        Its named operations share ``deps``.
        """
        self._subject = tier().subject(name, deps or Deps())
        self._tx = tx
        self._ops = Scope(tier(), name, deps or Deps())

    @classmethod
    def from_decorations(
        cls,
        name: str,
        decorations: Decorations,
        tx: TransactionManager | None = None,
    ) -> WorkflowBase:
        """Return the base of the workflow ``name``, its layers resolved from the config.

        Each named operation ``op`` resolves as ``<name>.<op>``.
        """
        base = cls(name, tx, decorations.deps(tier(), name))
        base._ops = decorations.scope(tier(), name)
        return base

    def read[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Operation[In, Out]:
        """Return the named read ``op``: ``fn`` runs through the tier with no transaction open."""
        return Operation(self._ops.subject(op), fn)

    def command[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Operation[In, Out]:
        """Return the named command ``op``: ``fn`` runs through the tier in one transaction."""

        async def run(value: In) -> Out:
            return await self.in_tx(lambda: fn(value))

        return Operation(self._ops.subject(op), run)

    def saga[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Operation[In, Out]:
        """Return the named saga ``op``: ``fn`` runs with no transaction and commits each step with ``in_tx``."""
        return Operation(self._ops.subject(op), fn)

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
