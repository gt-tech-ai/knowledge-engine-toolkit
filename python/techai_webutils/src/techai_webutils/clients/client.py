"""The client tier (mirrors Go ``clients/client``).

``tier()`` orders the tier's layers; ``ClientBase``, built from the client's contract, runs its
calls through it, switched and tuned per client and per operation by the decoration config's
``clients`` tier. A client family's decorated wrapper embeds a ClientBase and runs each method
through ``exec`` or ``exec_once``.
"""

from __future__ import annotations

import dataclasses
from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.tracer import SpanKind
from techai_webutils.foundation.decorate import Deps, Layer, Tier

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.foundation.decorate import Decorations


def tier() -> Tier:
    """Return the client tier, outermost first.

    The bulkhead bounds calls before a retry multiplies them; each retry attempt passes the
    breaker and gets its own deadline, client span, metrics and log line.
    """
    return Tier(
        "client",
        (
            Layer.BULKHEAD,
            Layer.RETRY,
            Layer.CIRCUIT_BREAKER,
            Layer.TIMEOUT,
            Layer.TRACING,
            Layer.METRICS,
            Layer.LOGGING,
        ),
        SpanKind.CLIENT,
    )


@dataclass(frozen=True, slots=True)
class Option:
    """Shapes how a ClientBase decorates its client (Go ``client.Option``)."""

    span_kind: SpanKind | None = None
    """The kind of the client's spans, when it is not CLIENT."""

    adjust: Callable[[Deps], Deps] | None = None
    """Edits every operation's Deps."""


def producer() -> Option:
    """Decorate a message publisher: PRODUCER spans, since each call sends a message."""
    return Option(span_kind=SpanKind.PRODUCER)


def handler() -> Option:
    """Decorate a consumer's message handlers: CONSUMER spans, never retried, broken or bulkheaded.

    The broker's redelivery is the retry, and one poison message must not trip the others.
    """
    return Option(
        span_kind=SpanKind.CONSUMER,
        adjust=lambda d: dataclasses.replace(
            d, retrier=None, breaker=None, bulkhead=None
        ),
    )


def at_most_once() -> Option:
    """Never retry the client's calls, whatever the config says (a Redis PUBLISH)."""
    return Option(adjust=Deps.without_retry)


class ClientBase:
    """Runs one client's calls through the client tier (Go ``client.Base``).

    Each operation is one method of the client's contract, decorated as ``<name>.<method>``.
    ``exec`` retries a transient failure (an idempotent call); ``exec_once`` never retries (a
    call that must not run twice, or whose input is consumed).
    """

    __slots__ = ("_subject",)

    def __init__(
        self, contract: type | None, decorations: Decorations, name: str, *options: Option
    ) -> None:
        """Bind the client ``name``: one chain per method of ``contract`` (Go ``NewBase[C]``).

        ``contract=None`` is the wire form (``wire``).
        """
        client_tier, adjust = _shape(options)
        self._subject = (
            decorations.new_subject(client_tier, name, None, adjust)
            if contract is None
            else decorations.subject_of(client_tier, name, contract, adjust)
        )

    @classmethod
    def wire(cls, decorations: Decorations, name: str, *options: Option) -> ClientBase:
        """Return the base of a client whose operations are wire-level names (Go ``NewWireBase``).

        An RPC procedure, an HTTP route: every operation runs on the client's own chain, labeled
        by its name, so the config tunes the client as a whole.
        """
        return cls(None, decorations, name, *options)

    @property
    def name(self) -> str:
        """The client's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run the idempotent call ``fn`` as ``op``, retried when the tier retries."""
        return await self._subject.exec(op, fn)

    async def exec_once[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run the call ``fn`` as ``op`` exactly once, never retried."""
        return await self._subject.exec_once(op, fn)


def _shape(
    options: tuple[Option, ...],
) -> tuple[Tier, tuple[Callable[[Deps], Deps], ...]]:
    """Return the client tier and the Deps adjustments ``options`` select."""
    client_tier = tier()
    adjust: list[Callable[[Deps], Deps]] = []
    for o in options:
        if o.span_kind is not None:
            client_tier = dataclasses.replace(client_tier, span_kind=o.span_kind)
        if o.adjust is not None:
            adjust.append(o.adjust)
    return client_tier, tuple(adjust)
