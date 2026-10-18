"""Decoration tiers, their collaborators and decorated subjects (mirrors Go ``decorate/tier.go``).

Each tier package declares its ``Tier`` as data — a name and its layers, outermost first — and
builds a ``Subject`` per component from the component's ``Deps``; ``Subject.exec`` runs one
operation through the tier's chain.
"""

from __future__ import annotations

import dataclasses
from dataclasses import dataclass
from enum import IntEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppRuntimeError
from techai_webutils.core.interfaces.tracer import SpanKind
from techai_webutils.foundation.decorate.middleware import (
    chain,
    exec_op,
    new_auth,
    new_bulkhead,
    new_circuit_breaker,
    new_logging,
    new_metrics,
    new_rate_limit,
    new_recovery,
    new_retry,
    new_timeout,
    new_tracing,
)

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.bulkhead import Bulkhead
    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter
    from techai_webutils.core.interfaces.retrier import Retrier
    from techai_webutils.core.interfaces.tracer import TracerProvider
    from techai_webutils.foundation.decorate.middleware import Authorizer, OpMiddleware


class Layer(IntEnum):
    """One decoration layer of a tier."""

    RECOVERY = 1
    """Codes an unexpected exception as INTERNAL; it needs no collaborator."""

    TRACING = 2
    """Opens a span per operation (``Deps.tracer``)."""

    METRICS = 3
    """Records the tier's RED series (``Deps.metrics``)."""

    LOGGING = 4
    """Logs each operation (``Deps.logger``)."""

    AUTH = 5
    """Authorizes ``<name>.<op>`` (``Deps.auth``)."""

    TIMEOUT = 6
    """Bounds each operation (``Deps.timeout``)."""

    CIRCUIT_BREAKER = 7
    """Sheds calls while the breaker is open (``Deps.breaker``)."""

    RETRY = 8
    """Retries transient failures (``Deps.retrier``)."""

    BULKHEAD = 9
    """Bounds the operations in flight (``Deps.bulkhead``): waits, or sheds with ``shed_load``."""

    RATE_LIMIT = 10
    """Rejects operations over the limiter's rate (``Deps.rate_limiter``)."""


@dataclass(frozen=True, slots=True)
class Observability:
    """The collaborators of the logging, metrics and tracing layers."""

    logger: Logger | None = None
    """Backs the logging layer (and the recovery layer's log line)."""

    metrics: MetricsProvider | None = None
    """Backs the metrics layer."""

    tracer: TracerProvider | None = None
    """Backs the tracing layer."""


@dataclass(frozen=True, slots=True)
class Deps:
    """The collaborators a tier's layers use.

    A None collaborator, or a zero timeout, leaves its layer out; recovery needs none and is
    applied wherever the tier orders it unless ``skip_recovery`` is set.
    ``Decorations.deps`` builds a Deps from config.
    """

    observability: Observability = dataclasses.field(default_factory=Observability)
    """Backs the logging, metrics and tracing layers."""

    auth: Authorizer | None = None
    """Authorizes an operation by its ``<name>.<op>`` action."""

    retrier: Retrier | None = None
    """Backs the retry layer."""

    breaker: CircuitBreakerInterface | None = None
    """Backs the circuit-breaker layer."""

    bulkhead: Bulkhead | None = None
    """Backs the bulkhead layer."""

    rate_limiter: RateLimiter | None = None
    """Backs the rate-limit layer."""

    timeout: float = 0.0
    """Bounds each operation, in seconds; zero leaves the layer out."""

    skip_recovery: bool = False
    """Leaves the recovery layer out (the config turned it off)."""

    panic_logger: Logger | None = None
    """Logs a recovered exception; None falls back to the observability logger. ``build`` sets
    it whenever recovery is on, so a crash is logged even in a tier whose logging layer is off."""

    shed_load: bool = False
    """Makes the bulkhead reject an operation when full instead of waiting (a server sheds)."""

    def without_retry(self) -> Deps:
        """Return these deps with no retrier, for an operation that is not idempotent."""
        return dataclasses.replace(self, retrier=None)

    def without_timeout(self) -> Deps:
        """Return these deps with no timeout, for a stream that outlives any call budget."""
        return dataclasses.replace(self, timeout=0.0)


@dataclass(frozen=True, slots=True)
class Tier:
    """One request tier's decoration: its name and layers, outermost first.

    The name is the metric, log and span-attribute prefix; ``span_kind`` is the kind of the
    tracing layer's spans (internal by default; client for an outbound call, server for a
    request handler).
    """

    name: str
    """The tier label ("controller", "client", "workflow", "pipeline", "service", …)."""

    order: tuple[Layer, ...]
    """The tier's layers, outermost first."""

    span_kind: SpanKind = SpanKind.INTERNAL
    """The kind of the tracing layer's spans."""

    edge: bool = False
    """Whether the tier is a request's outermost seam (the controllers tier).

    Its logging layer logs a failure once, at Warning for a client fault and at Error
    otherwise, where an inner tier logs it at Debug.
    """

    def middleware(self, layer: Layer, name: str, deps: Deps) -> OpMiddleware | None:
        """Return the middleware for one layer over the subject ``name``, or None when absent.

        A protocol adapter (a server's interceptors) walks ``order`` with it, standing in its own
        interceptor for a layer it implements natively.
        """
        obs = deps.observability
        match layer:
            case Layer.RECOVERY:
                return (
                    None
                    if deps.skip_recovery
                    else new_recovery(deps.panic_logger or obs.logger, self.name, name)
                )
            case Layer.TRACING:
                return _when(
                    obs.tracer, lambda t: new_tracing(t, self.name, name, self.span_kind)
                )
            case Layer.METRICS:
                return _when(obs.metrics, lambda m: new_metrics(m, self.name, name))
            case Layer.LOGGING:
                return _when(
                    obs.logger,
                    lambda lg: new_logging(lg, self.name, name, edge=self.edge),
                )
            case Layer.AUTH:
                return _when(deps.auth, lambda a: new_auth(name, a))
            case Layer.TIMEOUT:
                return new_timeout(deps.timeout) if deps.timeout > 0 else None
            case Layer.CIRCUIT_BREAKER:
                return _when(deps.breaker, lambda cb: new_circuit_breaker(cb, name))
            case Layer.RETRY:
                return _when(deps.retrier, new_retry)
            case Layer.BULKHEAD:
                return _when(
                    deps.bulkhead, lambda b: new_bulkhead(b, shed=deps.shed_load)
                )
            case Layer.RATE_LIMIT:
                return _when(deps.rate_limiter, new_rate_limit)

    def chain(self, name: str, deps: Deps) -> OpMiddleware:
        """Build the tier's middleware chain for the subject ``name`` from ``deps``."""
        mws = (self.middleware(layer, name, deps) for layer in self.order)
        return chain(*(mw for mw in mws if mw is not None))

    def chains(self, name: str, deps: Deps) -> Chains:
        """Build the chains one operation of the subject ``name`` can run on from ``deps``."""
        return Chains(
            full=self.chain(name, deps),
            once=self.chain(name, deps.without_retry()),
            observed=self.chain(name, Deps(observability=deps.observability)),
            streaming=self.chain(name, deps.without_retry().without_timeout()),
            timeout=deps.timeout,
        )

    def subject(self, name: str, deps: Deps) -> Subject:
        """Build the Subject that runs any operation of ``name`` through the tier's chains."""
        return Subject(name, self.chains(name, deps))


@dataclass(frozen=True, slots=True)
class Chains:
    """One operation's middleware chains (mirrors Go ``decorate.chains``)."""

    full: OpMiddleware
    """The chain as configured."""

    once: OpMiddleware
    """``full`` without the retry layer, for a write that must not replay."""

    observed: OpMiddleware
    """The tracing, metrics and logging layers only, for a statement inside a caller's transaction."""

    streaming: OpMiddleware
    """``full`` without the retry and timeout layers, for opening a stream whose deadline the
    caller owns."""

    timeout: float
    """The operation's configured deadline in seconds (zero: none)."""


class View(IntEnum):
    """Which of an operation's chains a Subject runs it on."""

    FULL = 1
    """The chain as configured."""

    ONCE = 2
    """Without the retry layer."""

    OBSERVED = 3
    """The observability layers only."""

    STREAMING = 4
    """Without the retry and timeout layers."""


def _when[C](
    collaborator: C | None, build: Callable[[C], OpMiddleware]
) -> OpMiddleware | None:
    """Build the middleware only when its collaborator is present."""
    return None if collaborator is None else build(collaborator)


class Subject:
    """Runs one component's operations through its tier's chains (mirrors Go ``decorate.Subject``).

    Built by ``Decorations.subject_of`` from the component's contract, it holds one set of chains
    per operation, each resolved from that operation's own config key (``<name>.<op>``), and an
    operation the contract does not have raises. Built by ``Tier.subject``, one set of chains
    serves every operation. ``Subject.plain(name)`` runs operations undecorated.
    """

    __slots__ = ("_all", "_name", "_ops", "_view")

    def __init__(
        self,
        name: str,
        chains: Chains | None,
        ops: dict[str, Chains] | None = None,
        view: View = View.FULL,
    ) -> None:
        """Bind the name, the component's chains (None: undecorated) and each op's chains."""
        self._name = name
        self._all = chains
        self._ops = ops
        self._view = view

    @classmethod
    def plain(cls, name: str) -> Subject:
        """Return the undecorated Subject named ``name``."""
        return cls(name, None)

    @property
    def name(self) -> str:
        """The component's name."""
        return self._name

    def _with(self, view: View, *, ops: bool = True) -> Subject:
        """Return this Subject on ``view``; ``ops=False`` drops the declared operations."""
        return Subject(self._name, self._all, self._ops if ops else None, view)

    def once(self) -> Subject:
        """Return this Subject running every operation without its retry layer."""
        return self._with(View.ONCE)

    def observed(self) -> Subject:
        """Return this Subject running every operation with its observability layers only."""
        return self._with(View.OBSERVED)

    def streaming(self) -> Subject:
        """Return this Subject running every operation without its retry and timeout layers."""
        return self._with(View.STREAMING)

    def component(self) -> Subject:
        """Return this Subject running any operation on the component's own chains.

        For an operation named outside the contract (a loop's pass, named by its loop), which the
        config tunes with the component.
        """
        return self._with(self._view, ops=False)

    def timeout(self, op: str) -> float:
        """Return the deadline configured for ``op`` in seconds (zero when none or undeclared)."""
        if self._ops is None:
            return self._all.timeout if self._all is not None else 0.0
        chains = self._ops.get(op)
        return chains.timeout if chains is not None else 0.0

    def _chain(self, op: str) -> OpMiddleware | None:
        """Return the chain ``op`` runs on; an op the contract lacks raises ``AppRuntimeError``."""
        chains = self._all
        if self._ops is not None:
            chains = self._ops.get(op)
            if chains is None:
                msg = f"decorate: {self._name} has no operation {op}"
                raise AppRuntimeError(msg)
        if chains is None:
            return None
        match self._view:
            case View.ONCE:
                return chains.once
            case View.OBSERVED:
                return chains.observed
            case View.STREAMING:
                return chains.streaming
            case View.FULL:
                return chains.full

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run ``fn`` as the operation ``op`` through its chain and return its result unchanged."""
        mw = self._chain(op)
        if mw is None:
            return await fn()
        return await exec_op(mw, op, fn)

    async def exec_once[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run ``fn`` as the operation ``op`` without its retry layer."""
        return await self.once().exec(op, fn)
