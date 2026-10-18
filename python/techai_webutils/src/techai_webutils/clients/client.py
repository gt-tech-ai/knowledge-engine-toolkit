"""The client tier (mirrors Go ``clients/client``).

``tier()`` orders the tier's layers; ``ClientBase`` runs one client's calls through it, switched
and tuned per client by the decoration config's ``clients`` tier. ``decorate`` is the Python form
of a Go family's ``Decorate``: it returns the client with every async method run through the
tier, so a caller programs to the client it passed in.
"""

from __future__ import annotations

import dataclasses
import functools
import inspect
from typing import TYPE_CHECKING, Self, cast

from techai_webutils.core.interfaces.tracer import SpanKind
from techai_webutils.foundation.decorate import Deps, Layer, Operation, Scope, Tier

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Iterable
    from contextlib import AbstractAsyncContextManager
    from types import TracebackType

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


class ClientBase:
    """Runs one client's calls through the client tier.

    ``exec`` retries a transient failure (an idempotent call); ``once`` never retries (a call
    that must not run twice, or whose input is consumed).
    """

    __slots__ = ("_once", "_ops", "_subject")

    def __init__(
        self, name: str, deps: Deps, *, span_kind: SpanKind = SpanKind.CLIENT
    ) -> None:
        """Bind the client ``name``'s subjects: one retrying, one not, with spans of ``span_kind``.

        Its named calls share ``deps``.
        """
        client_tier = dataclasses.replace(tier(), span_kind=span_kind)
        self._subject = client_tier.subject(name, deps)
        self._once = client_tier.subject(name, deps.without_retry())
        self._ops = Scope(client_tier, name, deps)

    @classmethod
    def from_decorations(cls, name: str, decorations: Decorations) -> ClientBase:
        """Return the base of the client ``name``, its layers resolved from the config.

        Each named call ``op`` resolves as ``<name>.<op>``.
        """
        base = cls(name, decorations.deps(tier(), name))
        base._ops = decorations.scope(tier(), name)
        return base

    def call[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Operation[In, Out]:
        """Return the named idempotent call ``op``: ``fn`` runs through the tier with its retry."""
        return Operation(self._ops.subject(op), fn)

    def call_once[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Operation[In, Out]:
        """Return the named call ``op`` that never retries (a send, a create, a consumed input)."""
        return Operation(self._ops.subject(op, without_retry=True), fn)

    @classmethod
    def producer(cls, name: str, deps: Deps) -> ClientBase:
        """Return the base of the message publisher ``name``: PRODUCER spans (Go ``NewProducerBase``)."""
        return cls(name, deps, span_kind=SpanKind.PRODUCER)

    @classmethod
    def handler(cls, name: str, deps: Deps) -> ClientBase:
        """Return the base a message handler runs under, as ``<name>.handler``.

        A handler is observed in CONSUMER spans and never retried, broken or bulkheaded: the
        broker's redelivery is the retry (Go ``NewHandlerBase``).
        """
        handler_deps = dataclasses.replace(
            deps, retrier=None, breaker=None, bulkhead=None
        )
        return cls(f"{name}.handler", handler_deps, span_kind=SpanKind.CONSUMER)

    @property
    def name(self) -> str:
        """The client's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run the idempotent call ``fn`` as ``op``, retried when the tier retries."""
        return await self._subject.exec(op, fn)

    async def once[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run the call ``fn`` as ``op`` exactly once, never retried."""
        return await self._once.exec(op, fn)


def decorate[T](client: T, name: str, deps: Deps, *, once: Iterable[str] = ()) -> T:
    """Return ``client`` with every async method run through the client tier as ``name``.

    Each coroutine method runs as the operation named by the method, retried when the tier
    retries — except the methods named in ``once``, which never are. A method returning an async
    iterator (a stream) and a sync method pass through: opening a stream does no I/O.
    """
    return cast("T", _ClientProxy(client, ClientBase(name, deps), frozenset(once)))


def decorate_from_decorations[T](
    client: T,
    name: str,
    decorations: Decorations,
    *,
    once: Iterable[str] = (),
) -> T:
    """Return ``client`` decorated as ``name``, its layers resolved from the config."""
    return decorate(client, name, decorations.deps(tier(), name), once=once)


class _ClientProxy:
    """Forwards attribute access, running each coroutine method through a ClientBase.

    ``async with`` reaches the client too: the dunder lookup skips ``__getattr__``, so the
    proxy forwards ``__aenter__`` / ``__aexit__`` itself.
    """

    __slots__ = ("_base", "_inner", "_once")

    def __init__(self, inner: object, base: ClientBase, once: frozenset[str]) -> None:
        """Bind the client, its base and the methods never retried."""
        self._inner = inner
        self._base = base
        self._once = once

    def __getattr__(self, name: str) -> object:
        """Return the client's attribute, its coroutine methods run through the tier."""
        attr = getattr(self._inner, name)
        if not inspect.iscoroutinefunction(attr):
            return attr
        run = self._base.once if name in self._once else self._base.exec

        @functools.wraps(attr)
        async def call(*args: object, **kwargs: object) -> object:
            return await run(name, lambda: attr(*args, **kwargs))

        return call

    async def __aenter__(self) -> Self:
        """Open the client and return the proxy, so its calls stay decorated."""
        await cast("AbstractAsyncContextManager[object]", self._inner).__aenter__()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> bool | None:
        """Close the client."""
        inner = cast("AbstractAsyncContextManager[object]", self._inner)
        return await inner.__aexit__(exc_type, exc_val, exc_tb)
