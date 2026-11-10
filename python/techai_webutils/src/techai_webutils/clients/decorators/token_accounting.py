"""``TokenAccountingProxy``: budget before a model call, usage after it.

It is the outermost AI band.

A transparent ``__getattr__`` proxy over an ``LLMProvider``, a ``ModelGateway`` or an
``EmbeddingProvider``. On ``complete`` / ``stream`` / ``stream_with_usage`` / ``embed`` /
``embed_batch`` it:

1. reads the request's ``UsageScope`` from the injected ``scope`` callable — ``None`` (a
   call outside any request) passes straight through, unrecorded;
2. asks ``ledger.check_budget`` and applies ``policy.decide``: a rejection (an exhausted
   budget under ``soft`` / ``hard``, a ledger error under ``hard``) raises
   ``QuotaExceededError`` **before** the inner call; ``shadow`` serves and counts
   ``ai_budget_would_exceed_total``;
3. awaits the inner call, then records one ``UsageRecord`` priced from the injected
   ``PriceTable`` — a stream once its terminal ``StreamUsage`` arrives. An unpriced model
   records ``cost_usd=0`` with ``pricing_version="missing"`` and is counted; a call the
   semantic cache answered (``served_from_cache()``) records one zero-token
   ``<step>.cache_hit`` usage instead.

Recording never raises into the caller. Metrics:
``ai_tokens_total{org,workspace,model,type}``, ``ai_cost_usd_total{org,workspace,model}``.
Every other attribute passes through untouched.
"""

from __future__ import annotations

import functools
from dataclasses import dataclass
from datetime import UTC, datetime
from decimal import Decimal
from typing import TYPE_CHECKING, cast, final

from opentelemetry import trace

from techai_webutils.core.errors import QuotaExceededError
from techai_webutils.core.interfaces.embedding import EmbeddingResult
from techai_webutils.core.interfaces.llm import LLMResponse, StreamUsage
from techai_webutils.core.interfaces.semantic_cache import (
    served_from_cache,
    set_served_from_cache,
)
from techai_webutils.core.interfaces.token_ledger import (
    LEDGER_UNAVAILABLE_REASON,
    UsageRecord,
)
from techai_webutils.foundation.logger.logger import get_logger
from techai_webutils.foundation.tokens.cost import estimate_cost
from techai_webutils.foundation.tokens.policy import BudgetPolicy, decide

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable

    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.interfaces.token_ledger import (
        BudgetDecision,
        TokenLedger,
        UsageScope,
        UsageScopeProvider,
    )
    from techai_webutils.foundation.tokens.cost import PriceTable

MISSING_PRICING = "missing"
"""The ``pricing_version`` recorded for a model the price table doesn't price."""


def _utcnow() -> datetime:
    """Return the current UTC time.

    Returns:
        The current UTC time.

    """
    return datetime.now(UTC)


def _trace_id() -> str:
    """Return the current span's trace id (hex), or ``""`` outside a recorded span.

    Returns:
        The current span's trace id (hex), or ``""`` outside a recorded span.

    """
    ctx = trace.get_current_span().get_span_context()
    return f"{ctx.trace_id:032x}" if ctx.is_valid else ""


@dataclass(frozen=True, kw_only=True)
class UsageStamp:
    """What ``usage_from_llm`` takes, grouped into one value."""

    trace_id: str
    """The ``trace_id`` value of ``usage_from_llm``."""

    ts: datetime
    """The ``ts`` value of ``usage_from_llm``."""


def usage_from_llm(
    scope: UsageScope,
    step: str,
    result: LLMResponse | StreamUsage | list[EmbeddingResult],
    prices: PriceTable,
    options: UsageStamp,
) -> UsageRecord:
    """Return the ``UsageRecord`` for one model call's result.

    A completion or stream reports input and output tokens; a list of embeddings reports
    their summed ``token_count`` as ``embed_tokens``. An unpriced model costs ``0`` under
    ``pricing_version="missing"``.

    Args:
        scope: Who the usage is billed to.
        step: The pipeline step that made the call.
        result: The call's response, stream usage or embeddings.
        prices: Prices each model's tokens.
        options: The record's model, call kind and timing.

    Returns:
        The ``UsageRecord`` for one model call's result.

    """
    if isinstance(result, list):
        model = result[0].model if result else ""
        counts = (0, 0, sum(r.token_count for r in result))
    else:
        model = result.model
        counts = (result.input_tokens, result.output_tokens, 0)
    cost = estimate_cost(
        model,
        prices,
        input_tokens=counts[0],
        output_tokens=counts[1],
        embed_tokens=counts[2],
    )
    return UsageRecord(
        scope=scope,
        model=model,
        operation=step,
        input_tokens=counts[0],
        output_tokens=counts[1],
        embed_tokens=counts[2],
        ts=options.ts,
        cost_usd=cost if cost is not None else Decimal(0),
        pricing_version=prices.version if cost is not None else MISSING_PRICING,
        trace_id=options.trace_id,
    )


@dataclass(frozen=True, kw_only=True)
class TokenAccountingOptions:
    """What ``TokenAccountingProxy`` takes, grouped into one value."""

    ledger: TokenLedger
    """Answers budget checks and records usage."""

    scope: UsageScopeProvider
    """Returns the current request's ``UsageScope`` (``None`` outside a request)."""

    prices: PriceTable
    """Prices each recorded usage."""

    policy: BudgetPolicy
    """How an exhausted budget is enforced."""

    step: str
    """The pipeline step, recorded as the usage's ``operation``."""

    metrics: MetricsProvider | None = None
    """Emits token, cost and budget counters; ``None`` emits nothing."""

    clock: Callable[[], datetime] = _utcnow
    """Returns the usage timestamp (injected for tests)."""


@final
class TokenAccountingProxy:
    """Gates each model call on the tenant's budget and records what it used."""

    def __init__(self, inner: object, options: TokenAccountingOptions) -> None:
        """Wrap ``inner`` for the pipeline step ``step``.

        Args:
            inner: The provider, gateway or embedder to account.
            options: The settings and collaborators, grouped.

        """
        self._inner = inner
        self._ledger = options.ledger
        self._scope = options.scope
        self._prices = options.prices
        self._policy = options.policy
        self._step = options.step
        self._clock = options.clock
        self._logger = get_logger("token_accounting")
        self._counters: dict[str, MetricCounter] = {}
        if options.metrics is not None:
            for name, help_text, labels in (
                (
                    "ai_tokens_total",
                    "AI tokens by tenant, model and type.",
                    ["org", "workspace", "model", "type"],
                ),
                (
                    "ai_cost_usd_total",
                    "AI cost in US dollars by tenant and model.",
                    ["org", "workspace", "model"],
                ),
                (
                    "ai_budget_would_exceed_total",
                    "Calls served over budget in shadow mode.",
                    ["org"],
                ),
                ("ai_budget_check_failed_total", "Budget checks that failed open.", []),
                (
                    "ai_usage_unpriced_total",
                    "Usages recorded for an unpriced model.",
                    ["model"],
                ),
            ):
                self._counters[name] = options.metrics.counter(name, help_text, labels)

    def __getattr__(self, name: str) -> object:
        """Return the inner attribute, accounted when it is a model call.

        Args:
            name: The attribute.

        Returns:
            The inner attribute, accounted when it is a model call.

        """
        attr = cast("object", getattr(self._inner, name))
        if not callable(attr):
            return attr
        if name in {"complete", "embed", "embed_batch"}:
            return self._wrap_call(attr)
        if name == "stream":
            source = getattr(self._inner, "stream_with_usage", attr)
            return self._wrap_stream(attr, source, keep_usage=False)
        if name == "stream_with_usage":
            return self._wrap_stream(attr, attr, keep_usage=True)
        return attr

    def _count(self, name: str, value: float = 1.0, **labels: str) -> None:
        """Increment the counter ``name`` (no-op without metrics).

        Args:
            name: The counter.
            value: The increment.
            **labels: The series' labels.

        """
        counter = self._counters.get(name)
        if counter is not None:
            counter.inc(value, **labels)

    async def _gate(self) -> UsageScope | None:
        """Return the scope to charge, rejecting what the policy rejects.

        ``None`` leaves the call unaccounted.

        Returns:
            The scope to charge (``None``: unaccounted).

        Raises:
            QuotaExceededError: the policy rejects the call (an exhausted budget under
                ``soft`` / ``hard``, or a ledger error under ``hard``).

        """
        scope = self._scope()
        if scope is None:
            return None
        set_served_from_cache(value=False)
        decision: BudgetDecision | None = None
        error: Exception | None = None
        try:
            decision = await self._ledger.check_budget(scope)
        except Exception as exc:
            self._logger.warning(
                "budget check failed", org_id=scope.org_id, exc_info=True
            )
            error = exc
        if error is not None or (
            decision is not None and decision.reason == LEDGER_UNAVAILABLE_REASON
        ):
            self._count("ai_budget_check_failed_total")
        verdict = decide(self._policy, decision, error)
        if verdict.serve:
            if verdict.would_exceed:
                self._count("ai_budget_would_exceed_total", org=scope.org_id)
            return scope
        msg = f"AI budget exhausted for organization {scope.org_id}"
        raise QuotaExceededError(msg, org_id=scope.org_id, reason=verdict.reason)

    def _usage(
        self, scope: UsageScope, result: LLMResponse | StreamUsage | list[EmbeddingResult]
    ) -> UsageRecord:
        """Return the call's usage.

        Priced tokens, or one zero-token record for a cache hit.

        Args:
            scope: Who the usage is billed to.
            result: The call's response, stream usage or embeddings.

        Returns:
            The call's usage: priced tokens.

        """
        if not served_from_cache():
            return usage_from_llm(
                scope,
                self._step,
                result,
                self._prices,
                options=UsageStamp(trace_id=_trace_id(), ts=self._clock()),
            )
        if isinstance(result, list):
            model = result[0].model if result else ""
        else:
            model = result.model
        return UsageRecord(
            scope=scope,
            model=model,
            operation=f"{self._step}.cache_hit",
            input_tokens=0,
            output_tokens=0,
            ts=self._clock(),
            pricing_version=self._prices.version,
            trace_id=_trace_id(),
        )

    async def _record(
        self, scope: UsageScope, result: LLMResponse | StreamUsage | list[EmbeddingResult]
    ) -> None:
        """Count and record one call's usage; log a failure, never raise it.

        Args:
            scope: Who the usage is billed to.
            result: The call's response, stream usage or embeddings.

        """
        try:
            record = self._usage(scope, result)
            self._emit(record)
            await self._ledger.record(record)
        except Exception:
            self._logger.warning(
                "token usage record failed", org_id=scope.org_id, exc_info=True
            )

    def _emit(self, record: UsageRecord) -> None:
        """Count ``record``'s tokens and cost.

        Args:
            record: The call's usage.

        """
        labels = {
            "org": record.scope.org_id,
            "workspace": record.scope.workspace_id or "",
            "model": record.model,
        }
        for kind, tokens in (
            ("input", record.input_tokens),
            ("output", record.output_tokens),
            ("embed", record.embed_tokens),
        ):
            if tokens:
                self._count("ai_tokens_total", tokens, type=kind, **labels)
        self._count("ai_cost_usd_total", float(record.cost_usd), **labels)
        if record.pricing_version == MISSING_PRICING:
            self._count("ai_usage_unpriced_total", model=record.model)

    def _wrap_call(self, attr: Callable[..., object]) -> object:
        """Wrap a unary call (``complete``, ``embed``, ``embed_batch``).

        It gates, calls, then records.

        Args:
            attr: The inner model call.

        Returns:
            The wrapped unary call.

        """

        @functools.wraps(attr)
        async def call(*args: object, **kwargs: object) -> object:
            scope = await self._gate()
            result = await cast("Callable[..., Awaitable[object]]", attr)(*args, **kwargs)
            if scope is not None:
                usage = [result] if isinstance(result, EmbeddingResult) else result
                await self._record(
                    scope, cast("LLMResponse | list[EmbeddingResult]", usage)
                )
            return result

        return call

    def _wrap_stream(
        self,
        attr: Callable[..., object],
        source: Callable[..., object],
        *,
        keep_usage: bool,
    ) -> object:
        """Wrap a stream: gate, open ``source``, relay and record at its terminal usage.

        Args:
            attr: The inner method the caller asked for.
            source: The inner method that yields usage too.
            keep_usage: Whether to re-yield the ``StreamUsage`` item.

        Returns:
            The wrapped streaming call.

        """

        @functools.wraps(attr)
        async def stream(
            *args: object, **kwargs: object
        ) -> AsyncIterator[str | StreamUsage]:
            scope = await self._gate()
            opener = cast(
                "Callable[..., Awaitable[AsyncIterator[str | StreamUsage]]]", source
            )
            items = await opener(*args, **kwargs)
            return self._relay(items, scope, keep_usage=keep_usage)

        return stream

    async def _relay(
        self,
        items: AsyncIterator[str | StreamUsage],
        scope: UsageScope | None,
        *,
        keep_usage: bool,
    ) -> AsyncIterator[str | StreamUsage]:
        """Re-yield ``items`` and record the usage at the end.

        The ``StreamUsage`` is re-yielded only when ``keep_usage``.

        A stream abandoned before its terminal usage records nothing (there are no
        counts).

        Args:
            items: The inner stream.
            scope: Who the usage is billed to; ``None`` records nothing.
            keep_usage: Whether to re-yield the ``StreamUsage`` item.

        Yields:
            Each text item, then the usage when ``keep_usage``.

        """
        usage: StreamUsage | None = None
        async for item in items:
            if isinstance(item, StreamUsage):
                usage = item
                if not keep_usage:
                    continue
            yield item
        if scope is not None and usage is not None:
            await self._record(scope, usage)
