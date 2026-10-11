"""``AiSpanEnricher`` — stamps GenAI semantic attributes on the current span (ARCHITECTURE.md#decorator-order).

A ``__getattr__`` proxy in the ``TracingProxy`` style that wraps an ``LLMProvider`` or a
``RetrievalEngine``. It opens no span of its own: the composition root already runs each step inside a
span (the step pipeline's), so the enricher stamps that span with what only the client boundary
knows — the model, the token usage and the finish reason of a generation, or the shape of a
retrieval. One mechanism covers ``complete`` and both streamed paths: ``stream`` consumes the
provider's ``stream_with_usage`` and stamps when the terminal ``StreamUsage`` arrives, and
``stream_with_usage`` itself is enriched the same way while still handing the caller its
``StreamUsage``.

Attributes (the contract dashboards read):

- LLM: ``gen_ai.system``, ``gen_ai.step``, ``gen_ai.request.model`` (the ``LLMConfig.model`` asked
  for, else the served model), ``gen_ai.response.model`` (the model that served the call, from the
  response or the ``StreamUsage``), ``gen_ai.usage.input_tokens``, ``gen_ai.usage.output_tokens``,
  ``gen_ai.response.finish_reason``.
- Retrieval: ``gen_ai.step``, ``retrieval.top_k``, ``retrieval.result_count``,
  ``retrieval.document_ids`` (the first 20 ids, comma-joined).
- Content (only when ``capture_content``): span events ``gen_ai.content.prompt`` (attribute
  ``gen_ai.prompt``, the joined message contents) and ``gen_ai.content.completion`` (attribute
  ``gen_ai.completion``), each PII-redacted (``redact_pii``) and capped at 4 KiB.

Metrics (only with an injected ``MetricsProvider``): ``gen_ai_tokens_total{step,model,type}``
(``type`` is ``input`` or ``output``) and ``gen_ai_request_duration_seconds{step,model}``; ``model``
is the served model, so cost is attributed to the model that ran.

Facts (only with an injected ``FactPublisher``): one ``genai_calls`` fact per model call, with dims
``provider``/``model``/``step`` plus whatever the injected ``fact_dimensions`` callable returns (the
product's team/workspace/user/…; its ``org_id`` becomes the fact's ``org_id``, and a call without one
publishes no fact), measures ``duration_s``/``tokens_in``/``tokens_out``, ``ts`` the call's start
(so a long generation lands in the bucket it began in), and the idempotency key
``<trace_id>:<span_id>:<step>``.

``gen_ai.system`` is keyed by the wrapped provider's class. A ``FallbackLlmProvider`` reports
``aws.bedrock`` whichever member served; to attribute a fallback to its own system, wrap each
member provider in its own enricher instead of the fallback decorator.
"""

from __future__ import annotations

import functools
import inspect
import uuid
from dataclasses import dataclass
from datetime import UTC, datetime
from time import perf_counter
from types import MappingProxyType
from typing import TYPE_CHECKING, cast

from opentelemetry import trace

from techai_webutils.core.interfaces.llm import StreamUsage
from techai_webutils.core.interfaces.retrieval import RetrievalEngine
from techai_webutils.core.types.fact import Fact
from techai_webutils.foundation.logger.logger import get_logger
from techai_webutils.foundation.logger.redact import redact_pii

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable, Mapping

    from opentelemetry.trace import Span

    from techai_webutils.core.interfaces.fact_publisher import FactPublisher
    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage, LLMResponse
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.retrieval import RetrievalResult

GEN_AI_SYSTEM_BY_PROVIDER: Mapping[str, str] = MappingProxyType({
    "BedrockLlmProvider": "aws.bedrock",
    "FallbackLlmProvider": "aws.bedrock",
    "OllamaLlmProvider": "ollama",
    "StubLlmProvider": "stub",
})
"""``gen_ai.system`` value per provider class name.

``FallbackLlmProvider`` maps to ``aws.bedrock`` because its built-in members are Bedrock models; it
does not track which member served, so wrap each member in its own enricher when that matters.
"""

_UNKNOWN_SYSTEM = "unknown"
"""``gen_ai.system`` for a provider class not in ``GEN_AI_SYSTEM_BY_PROVIDER``."""

_MAX_DOCUMENT_IDS = 20
"""Most document ids stamped on ``retrieval.document_ids`` (keeps the attribute under span limits)."""

_MAX_CONTENT_BYTES = 4096
"""Cap, in UTF-8 bytes, on a captured prompt or completion event attribute."""

DURATION_BUCKETS: tuple[float, ...] = (
    0.05,
    0.1,
    0.25,
    0.5,
    1.0,
    2.5,
    5.0,
    10.0,
    20.0,
    30.0,
    60.0,
)
"""``gen_ai_request_duration_seconds`` buckets, in seconds: 50 ms up to a 60 s generation."""

GEN_AI_CALLS_CUBE = "genai_calls"
"""The analytics cube each model call's fact belongs to."""

_RETRIEVE_SIGNATURE = inspect.signature(RetrievalEngine.retrieve)
"""The ``RetrievalEngine.retrieve`` contract signature, used to resolve the effective ``top_k``."""


def _cap(text: str) -> str:
    """Redact PII in ``text`` and truncate it to ``_MAX_CONTENT_BYTES`` UTF-8 bytes (never mid-character)."""
    encoded = redact_pii(text).encode("utf-8")[:_MAX_CONTENT_BYTES]
    return encoded.decode("utf-8", errors="ignore")


def _utcnow() -> datetime:
    """Return the current UTC time (the fact timestamp; patched in tests as the clock boundary)."""
    return datetime.now(UTC)


def _idempotency_key(span: Span, step: str) -> str:
    """Return ``<trace_id>:<span_id>:<step>`` for a valid span, else a random key (never collides)."""
    ctx = span.get_span_context()
    if not ctx.is_valid:
        return f"{uuid.uuid4().hex}:{step}"
    return f"{ctx.trace_id:032x}:{ctx.span_id:016x}:{step}"


def _messages_arg(
    args: tuple[object, ...], kwargs: dict[str, object]
) -> list[LLMMessage]:
    """Return the ``messages`` argument of an ``LLMProvider`` call (positional or keyword)."""
    return cast("list[LLMMessage]", args[0] if args else kwargs.get("messages", []))


@dataclass(frozen=True, slots=True)
class _LlmCall:
    """What one finished model call reports to the span, the metrics and content capture."""

    model: str
    """The model that served the call (the response's or ``StreamUsage``'s, else the requested one)."""
    requested_model: str
    """The model the call asked for (``LLMConfig.model``, else the served model)."""
    input_tokens: int
    """Prompt tokens."""
    output_tokens: int
    """Completion tokens."""
    finish_reason: str
    """Why generation stopped."""
    duration_s: float
    """Wall time of the call in seconds (a stream: from open until exhaustion)."""
    started_at: datetime
    """UTC time the call started (the fact's ``ts``)."""


def _requested_model(args: tuple[object, ...], kwargs: dict[str, object]) -> str:
    """Return the ``LLMConfig.model`` of an ``LLMProvider`` call, or ``""`` when none was given."""
    config = cast("LLMConfig | None", args[1] if len(args) > 1 else kwargs.get("config"))
    return config.model if config is not None and config.model else ""


@dataclass(frozen=True, slots=True)
class _Opened:
    """What a streamed call records when it is opened, for the report at exhaustion."""

    started_at: datetime
    """UTC time the stream was opened (the fact's ``ts``)."""
    start: float
    """``perf_counter`` reading at open (the duration's origin)."""
    requested_model: str
    """The ``LLMConfig.model`` asked for, or ``""`` when none was given."""


class AiSpanEnricher:
    """Proxy that stamps GenAI attributes on the current span around an LLM or retrieval call.

    ``complete``, ``stream``, ``stream_with_usage`` and ``retrieve`` are enriched; every other attribute (``model_name``,
    the async-context methods, plain values) passes through untouched. Enrichment is best-effort:
    any failure while stamping or emitting is logged at warning (with the step) and the call's own
    result is returned untouched.
    """

    def __init__(
        self,
        inner: object,
        *,
        step: str,
        capture_content: bool,
        metrics: MetricsProvider | None = None,
        facts: FactPublisher | None = None,
        fact_dimensions: Callable[[], Mapping[str, str]] | None = None,
    ) -> None:
        """Wrap ``inner`` (an ``LLMProvider`` or ``RetrievalEngine``) for the pipeline step ``step``.

        Args:
            inner: The provider or engine to wrap.
            step: The pipeline step name stamped as ``gen_ai.step`` (``rewrite``, ``generate``, …).
            capture_content: Whether prompt and completion text are recorded as span events.
            metrics: Emits the GenAI token counter and duration histogram; ``None`` emits nothing.
                The instruments are created once here, never per call.
            facts: Receives one ``genai_calls`` fact per model call; ``None`` publishes nothing.
            fact_dimensions: Returns the product dimensions for the current call (must include
                ``org_id``); called once per model call, so it may read request context.

        """
        self._inner = inner
        self._step = step
        self._capture_content = capture_content
        self._system = GEN_AI_SYSTEM_BY_PROVIDER.get(
            type(inner).__name__, _UNKNOWN_SYSTEM
        )
        self._facts = facts
        self._fact_dimensions = fact_dimensions
        # Resolved by name like LoggingProxy: structlog is configured once at the process root.
        self._logger = get_logger("ai_enricher")
        self._tokens = None
        self._duration = None
        if metrics is not None:
            self._tokens = metrics.counter(
                "gen_ai_tokens_total",
                "GenAI tokens by step, model and type.",
                ["step", "model", "type"],
            )
            self._duration = metrics.histogram(
                "gen_ai_request_duration_seconds",
                "GenAI request duration in seconds by step and model.",
                ["step", "model"],
                list(DURATION_BUCKETS),
            )

    def __getattr__(self, name: str) -> object:
        """Return the inner attribute, enriched when it is a model call or ``retrieve``."""
        attr = getattr(self._inner, name)
        if not callable(attr):
            return attr
        wrap: Callable[[Callable[..., object]], object] | None = {
            "complete": self._wrap_complete,
            "stream": self._wrap_stream,
            "stream_with_usage": self._wrap_stream_with_usage,
            "retrieve": self._wrap_retrieve,
        }.get(name)
        return attr if wrap is None else wrap(attr)

    def _safely(self, enrich: Callable[..., None], *args: object) -> None:
        """Run one enrichment step; log a failure at warning and never let it reach the caller."""
        try:
            enrich(*args)
        except Exception:
            self._logger.warning(
                "ai span enrichment failed", step=self._step, exc_info=True
            )

    def _finish_llm(
        self, span: Span, llm_call: _LlmCall, messages: list[LLMMessage], completion: str
    ) -> None:
        """Report one finished model call: span attributes, content capture and metrics, each isolated."""
        self._safely(self._stamp_llm, span, llm_call)
        self._safely(self._capture, span, messages, completion)
        self._safely(self._emit_metrics, llm_call)
        self._safely(self._publish_fact, span, llm_call)

    def _stamp_llm(self, span: Span, llm_call: _LlmCall) -> None:
        """Stamp the GenAI request/usage attributes of one model call on ``span``."""
        span.set_attribute("gen_ai.system", self._system)
        span.set_attribute("gen_ai.step", self._step)
        span.set_attribute("gen_ai.request.model", llm_call.requested_model)
        span.set_attribute("gen_ai.response.model", llm_call.model)
        span.set_attribute("gen_ai.usage.input_tokens", llm_call.input_tokens)
        span.set_attribute("gen_ai.usage.output_tokens", llm_call.output_tokens)
        span.set_attribute("gen_ai.response.finish_reason", llm_call.finish_reason)

    def _capture(self, span: Span, messages: list[LLMMessage], completion: str) -> None:
        """Add the redacted, capped prompt and completion events to ``span`` when capture is on."""
        if not self._capture_content:
            return
        span.add_event(
            "gen_ai.content.prompt",
            {"gen_ai.prompt": _cap("\n".join(m.content for m in messages))},
        )
        span.add_event(
            "gen_ai.content.completion", {"gen_ai.completion": _cap(completion)}
        )

    def _emit_metrics(self, llm_call: _LlmCall) -> None:
        """Increment the token counter per type and observe the call duration (no-op without metrics)."""
        if self._tokens is not None:
            self._tokens.inc(
                llm_call.input_tokens, step=self._step, model=llm_call.model, type="input"
            )
            self._tokens.inc(
                llm_call.output_tokens,
                step=self._step,
                model=llm_call.model,
                type="output",
            )
        if self._duration is not None:
            self._duration.observe(
                llm_call.duration_s, step=self._step, model=llm_call.model
            )

    def _publish_fact(self, span: Span, llm_call: _LlmCall) -> None:
        """Publish this call's ``genai_calls`` fact (no-op without a publisher or an ``org_id`` dim)."""
        if self._facts is None:
            return
        dims = dict(self._fact_dimensions()) if self._fact_dimensions is not None else {}
        org_id = dims.pop("org_id", "")
        if not org_id:
            return
        fact = Fact(
            cube=GEN_AI_CALLS_CUBE,
            org_id=org_id,
            ts=llm_call.started_at,
            dims={
                "provider": self._system,
                "model": llm_call.model,
                "step": self._step,
                **dims,
            },
            measures={
                "duration_s": llm_call.duration_s,
                "tokens_in": llm_call.input_tokens,
                "tokens_out": llm_call.output_tokens,
            },
            idempotency_key=_idempotency_key(span, self._step),
        )
        self._facts.publish([fact])

    def _wrap_complete(self, attr: Callable[..., object]) -> object:
        """Wrap ``complete``: await it, then report the response's model, usage and finish reason."""

        @functools.wraps(attr)
        async def complete(*args: object, **kwargs: object) -> LLMResponse:
            span = trace.get_current_span()
            started_at = _utcnow()
            start = perf_counter()
            response = await cast("Callable[..., Awaitable[LLMResponse]]", attr)(
                *args, **kwargs
            )
            duration = perf_counter() - start
            requested = _requested_model(args, kwargs)
            served = response.model or requested
            llm_call = _LlmCall(
                model=served,
                requested_model=requested or served,
                input_tokens=response.input_tokens,
                output_tokens=response.output_tokens,
                finish_reason=response.finish_reason,
                duration_s=duration,
                started_at=started_at,
            )
            self._finish_llm(
                span, llm_call, _messages_arg(args, kwargs), response.content
            )
            return response

        return complete

    def _wrap_stream(self, attr: Callable[..., object]) -> object:
        """Wrap ``stream``: consume ``stream_with_usage``, re-yield text, report usage at exhaustion."""
        source = cast(
            "Callable[..., object]", getattr(self._inner, "stream_with_usage", attr)
        )
        return self._wrap_streamed(attr, source, keep_usage=False)

    def _wrap_stream_with_usage(self, attr: Callable[..., object]) -> object:
        """Wrap ``stream_with_usage``: re-yield every item, ``StreamUsage`` included, and report it."""
        return self._wrap_streamed(attr, attr, keep_usage=True)

    def _wrap_streamed(
        self,
        attr: Callable[..., object],
        source: Callable[..., object],
        *,
        keep_usage: bool,
    ) -> object:
        """Wrap a streamed call: open ``source`` now, relay its items and report at exhaustion."""

        @functools.wraps(attr)
        async def stream(
            *args: object, **kwargs: object
        ) -> AsyncIterator[str | StreamUsage]:
            # The span current when the stream is OPENED is the step's span; it is captured now and
            # stamped later, because by exhaustion another span may be current.
            span = trace.get_current_span()
            opened = _Opened(_utcnow(), perf_counter(), _requested_model(args, kwargs))
            opener = cast(
                "Callable[..., Awaitable[AsyncIterator[str | StreamUsage]]]", source
            )
            items = await opener(*args, **kwargs)
            return self._relay(
                items, span, _messages_arg(args, kwargs), opened, keep_usage=keep_usage
            )

        return stream

    async def _relay(
        self,
        items: AsyncIterator[str | StreamUsage],
        span: Span,
        messages: list[LLMMessage],
        opened: _Opened,
        *,
        keep_usage: bool,
    ) -> AsyncIterator[str | StreamUsage]:
        """Yield the items of ``items``; report the call once the terminal ``StreamUsage`` arrives.

        The ``StreamUsage`` is re-yielded only when ``keep_usage`` (the ``stream_with_usage`` path).
        The completion text is accumulated only when content capture is on. A provider that reports
        no usage (the ``stream_with_usage`` default) is reported as nothing: there are no counts to
        stamp or count.
        """
        parts: list[str] = []
        usage: StreamUsage | None = None
        async for item in items:
            if isinstance(item, StreamUsage):
                usage = item
                if keep_usage:
                    yield item
                continue
            if self._capture_content:
                parts.append(item)
            yield item
        if usage is None:
            self._safely(self._capture, span, messages, "".join(parts))
            return
        served = usage.model or opened.requested_model
        llm_call = _LlmCall(
            model=served,
            requested_model=opened.requested_model or served,
            input_tokens=usage.input_tokens,
            output_tokens=usage.output_tokens,
            finish_reason=usage.finish_reason,
            duration_s=perf_counter() - opened.start,
            started_at=opened.started_at,
        )
        self._finish_llm(span, llm_call, messages, "".join(parts))

    def _wrap_retrieve(self, attr: Callable[..., object]) -> object:
        """Wrap ``retrieve``: await it, then stamp top_k, the result count and the first 20 document ids."""

        @functools.wraps(attr)
        async def retrieve(*args: object, **kwargs: object) -> list[RetrievalResult]:
            span = trace.get_current_span()
            results = await cast("Callable[..., Awaitable[list[RetrievalResult]]]", attr)(
                *args, **kwargs
            )
            self._safely(self._stamp_retrieval, span, args, kwargs, results)
            return results

        return retrieve

    def _stamp_retrieval(
        self,
        span: Span,
        args: tuple[object, ...],
        kwargs: dict[str, object],
        results: list[RetrievalResult],
    ) -> None:
        """Stamp the step, the effective top_k, the result count and the first 20 document ids."""
        # Bind against the contract (not the wrapped callable) so the default top_k is resolved the
        # same way whichever engine is wrapped; ``None`` stands in for ``self``.
        bound = _RETRIEVE_SIGNATURE.bind(None, *args, **kwargs)
        bound.apply_defaults()
        span.set_attribute("gen_ai.step", self._step)
        span.set_attribute("retrieval.top_k", int(cast("int", bound.arguments["top_k"])))
        span.set_attribute("retrieval.result_count", len(results))
        span.set_attribute(
            "retrieval.document_ids",
            ",".join(r.document_id for r in results[:_MAX_DOCUMENT_IDS]),
        )
