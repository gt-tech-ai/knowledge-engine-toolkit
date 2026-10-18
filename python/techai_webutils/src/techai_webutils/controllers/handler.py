"""The controller tier (mirrors Go ``transport.Tier`` / ``transport.Base``).

``tier()`` orders the tier's layers; ``ControllerBase.handler`` runs a REST or RPC handler
through it, switched and tuned per controller by the decoration config's ``controllers`` tier.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.tracer import SpanKind
from techai_webutils.foundation.decorate import Layer, Tier

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.foundation.decorate import Decorations

type HandlerFunc[Req, Resp] = Callable[[Req], Awaitable[Resp]]
"""A request handler: a typed request in, a typed response out (Go ``transport.HandlerFunc``)."""


def tier() -> Tier:
    """Return the controller tier, outermost first.

    Each request gets its server span, metrics and log line around everything else, so a panic,
    a shed or a denial is still observed (as the edge, a failure is logged at Warning for a
    client fault and at Error otherwise); recovery codes an unexpected exception; the rate limit
    and the shedding bulkhead reject excess load before any work; auth authorizes
    ``<controller>.<op>``; and the timeout bounds the handler.
    """
    return Tier(
        "controller",
        (
            Layer.TRACING,
            Layer.METRICS,
            Layer.LOGGING,
            Layer.RECOVERY,
            Layer.RATE_LIMIT,
            Layer.BULKHEAD,
            Layer.AUTH,
            Layer.TIMEOUT,
        ),
        SpanKind.SERVER,
        edge=True,
    )


class ControllerBase:
    """Runs one controller's handlers through the controller tier."""

    __slots__ = ("_subject",)

    def __init__(self, contract: type, decorations: Decorations, name: str) -> None:
        """Bind the controller ``name``: one chain per method of ``contract`` (Go ``NewBase[C]``)."""
        self._subject = decorations.subject_of(tier(), name, contract)

    @property
    def name(self) -> str:
        """The controller's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run one operation ``fn`` as ``op`` through the tier."""
        return await self._subject.exec(op, fn)

    def handler[Req, Resp](
        self, op: str, fn: HandlerFunc[Req, Resp]
    ) -> HandlerFunc[Req, Resp]:
        """Return ``fn`` run through the tier as the operation ``op``."""

        async def run(req: Req) -> Resp:
            return await self._subject.exec(op, lambda: fn(req))

        return run
