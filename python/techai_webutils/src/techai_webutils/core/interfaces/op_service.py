"""OpService — the single-operation service contract (mirrors Go ``core/interfaces.OpService[A,R]``).

Unlike ``CrudService`` (five entity methods), an ``OpService`` exposes ONE decoratable ``run``
chokepoint; cross-cutting concerns (logging, timeout, recovery) wrap ``run`` rather than each
operation. Multi-operation services model their operations as a tagged union in the args and switch
on it inside ``run``. ``A`` is the operation argument type; ``R`` is the result type.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable


@runtime_checkable
class OpService[A, R](Protocol):
    """Generic operation-service contract: a named, decoratable single-operation entrypoint."""

    @property
    def name(self) -> str:
        """The service name — the label used in logs, metrics, and decorator actions."""
        ...

    async def run(self, args: A) -> R:
        """Execute the operation described by ``args`` and return its result (the decoration chokepoint)."""
        ...
