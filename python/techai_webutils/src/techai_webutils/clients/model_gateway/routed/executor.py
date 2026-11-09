"""The routed gateway's health-aware fallback executor.

``ChainExecutor`` walks the planned endpoints in order behind one ``AsyncCircuitBreaker``
per endpoint:

- an endpoint whose breaker is open is **skipped without a call** (``breaker_open``) and
  does not count toward ``max_attempts``;
- a transient error (``is_transient``) is recorded ``failed`` and moves to the next
  endpoint;
- a permanent error surfaces at once — it fails on every model, so failing over only
  multiplies the cost (the breaker counts it as a response, not an outage);
- when the bound or the chain runs out, a coded ``UNAVAILABLE`` is raised, the same
  terminal the LLM tier's ``FallbackLlmProvider`` raises.

A stream fails over only **before its first item**: two models' tokens can't be spliced
    into one
answer, so a failure after the first token surfaces to the caller. Endpoints are built
bare (the catalog clears ``fallback_model``), so this chain never stacks on the LLM tier's
own.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, cast, final

from techai_webutils.core.errors import AppError, ErrorCode, UnavailableError
from techai_webutils.core.interfaces.model_gateway import RouteAttempt
from techai_webutils.foundation.logger import get_logger
from techai_webutils.foundation.resilience.async_circuit_breaker import (
    AsyncCircuitBreaker,
)
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError
from techai_webutils.foundation.resilience.classify import is_transient

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable, Sequence

    from techai_webutils.core.interfaces.llm import LLMProvider, LLMResponse, StreamUsage
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.types.routing import CatalogEndpointRef

    type StreamItems = AsyncIterator[str | StreamUsage]
    """A provider's stream of text chunks and its trailing usage."""

logger = get_logger(__name__)

_END = object()
"""The sentinel ``anext`` returns for a stream that ends before its first item."""


def _code(exc: BaseException) -> str:
    """Return ``exc``'s error code (``UNKNOWN`` for an uncoded error).

    Returns:
        ``exc``'s error code (``UNKNOWN`` for an uncoded error).

    """
    return str(exc.code) if isinstance(exc, AppError) else str(ErrorCode.UNKNOWN)


@final
class _PermanentError(Exception):
    """Carries a permanent error out of the breaker without counting it as an outage."""

    def __init__(self, error: BaseException) -> None:
        """Wrap the permanent ``error``."""
        super().__init__(str(error))
        self.error = error


@final
class ChainExecutor:
    """Runs one call down an ordered endpoint chain, bounded and breaker-aware."""

    def __init__(
        self,
        max_attempts: int = 2,
        *,
        failure_threshold: int = 5,
        recovery_timeout: float = 30.0,
        metrics: MetricsProvider | None = None,
    ) -> None:
        """Bind the attempt bound, the per-endpoint breaker settings and the metrics."""
        self._max_attempts = max_attempts
        self._failure_threshold = failure_threshold
        self._recovery_timeout = recovery_timeout
        self._breakers: dict[str, AsyncCircuitBreaker] = {}
        self._attempts: MetricCounter | None = None
        if metrics is not None:
            self._attempts = metrics.counter(
                "routed_attempts_total",
                "Routed gateway endpoint attempts by outcome.",
                ["endpoint", "outcome"],
            )

    def _breaker(self, ref: CatalogEndpointRef) -> AsyncCircuitBreaker:
        """Return ``ref``'s breaker, created on first use.

        Returns:
            ``ref``'s breaker, created on first use.

        """
        breaker = self._breakers.get(ref.key)
        if breaker is None:
            breaker = AsyncCircuitBreaker(self._failure_threshold, self._recovery_timeout)
            self._breakers[ref.key] = breaker
        return breaker

    def _record(self, attempts: list[RouteAttempt], attempt: RouteAttempt) -> None:
        """Append ``attempt`` and count it."""
        attempts.append(attempt)
        if self._attempts is not None:
            self._attempts.inc(endpoint=attempt.endpoint, outcome=attempt.outcome)

    async def _guarded[T](
        self,
        ref: CatalogEndpointRef,
        provider_for: Callable[[CatalogEndpointRef], LLMProvider],
        call: Callable[[LLMProvider], Awaitable[T]],
    ) -> T:
        """Call ``ref`` behind its breaker.

        A permanent error leaves wrapped in ``_PermanentError``.

        A callee raises ``CircuitOpenError``: ``ref``'s breaker is open (no call was
        made).

        Returns:
            What ``call`` returns.

        Raises:
            _PermanentError: A non-retriable failure, re-raised to stop the chain.
            AppError: Circuit breaker suppressed a call's outcome.

        """
        permanent: Exception | None = None
        async with self._breaker(ref):
            try:
                return await call(provider_for(ref))
            except Exception as exc:
                if is_transient(exc):
                    raise
                # A permanent error is the request's fault, not the endpoint's: it leaves
                # the breaker as a success so it never opens a healthy endpoint.
                permanent = exc
        if permanent is not None:
            raise _PermanentError(permanent) from permanent
        # The breaker never suppresses an exception, so this is unreachable.
        raise AppError(ErrorCode.INTERNAL, "circuit breaker suppressed a call's outcome")

    async def _run[T](
        self,
        ordered: Sequence[CatalogEndpointRef],
        provider_for: Callable[[CatalogEndpointRef], LLMProvider],
        call: Callable[[LLMProvider], Awaitable[T]],
    ) -> tuple[T, tuple[RouteAttempt, ...], CatalogEndpointRef]:
        """Return the first endpoint's result the bound reaches.

        Else raise ``UNAVAILABLE``.

        Returns:
            The first endpoint's result the bound reaches.

        Raises:
            UnavailableError: Every routed endpoint failed (``tried`` attempted).

        """
        attempts: list[RouteAttempt] = []
        tried = 0
        last: BaseException | None = None
        for ref in ordered:
            if tried >= self._max_attempts:
                break
            try:
                result = await self._guarded(ref, provider_for, call)
            except CircuitOpenError:
                self._record(attempts, RouteAttempt(ref.key, "breaker_open"))
                continue
            except _PermanentError as carrier:
                self._record(
                    attempts, RouteAttempt(ref.key, "failed", _code(carrier.error))
                )
                raise carrier.error from None
            except Exception as exc:
                logger.exception("run failed")
                tried += 1
                self._record(attempts, RouteAttempt(ref.key, "failed", _code(exc)))
                last = exc
                continue
            self._record(attempts, RouteAttempt(ref.key, "ok"))
            return result, tuple(attempts), ref
        msg = f"every routed endpoint failed ({tried} attempted)"
        raise UnavailableError(msg) from last

    async def complete(
        self,
        ordered: Sequence[CatalogEndpointRef],
        provider_for: Callable[[CatalogEndpointRef], LLMProvider],
        call: Callable[[LLMProvider], Awaitable[LLMResponse]],
    ) -> tuple[LLMResponse, tuple[RouteAttempt, ...], CatalogEndpointRef]:
        """Complete down the chain; return the response.

        The attempts and the served endpoint.

        A callee raises ``AppError``: the first permanent error, or ``UNAVAILABLE`` when
        the chain is exhausted.

        Returns:
            The response, the attempts made and the endpoint that served it.

        """
        return await self._run(ordered, provider_for, call)

    async def stream(
        self,
        ordered: Sequence[CatalogEndpointRef],
        provider_for: Callable[[CatalogEndpointRef], LLMProvider],
        open_stream: Callable[[LLMProvider], Awaitable[StreamItems]],
    ) -> tuple[StreamItems, tuple[RouteAttempt, ...], CatalogEndpointRef]:
        """Open a stream down the chain, failing over only until its first item arrives.

        Returns:
            A stream down the chain, failing over only until its first item arrives.

        A callee raises ``AppError``: the first permanent error, or ``UNAVAILABLE`` when
        the chain is exhausted.

        """

        async def started(provider: LLMProvider) -> tuple[object, StreamItems]:
            items = await open_stream(provider)
            return await anext(items, _END), items

        (first, items), attempts, ref = await self._run(ordered, provider_for, started)

        async def replay() -> StreamItems:
            if first is _END:
                return
            yield cast("str | StreamUsage", first)
            async for item in items:
                yield item

        return replay(), attempts, ref
