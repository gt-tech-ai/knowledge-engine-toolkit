"""Unit tests.

For the GenAI metrics ``AiSpanEnricher`` emits through an injected ``MetricsProvider``.
"""

from __future__ import annotations

from datetime import UTC, datetime
from typing import cast
from unittest.mock import AsyncMock, MagicMock, call, patch

import pytest
from opentelemetry.trace import SpanContext

from techai_webutils.clients.decorators.ai_enricher import (
    AiSpanEnricher,
    AiSpanEnricherOptions,
)
from techai_webutils.core.interfaces.fact_publisher import FactPublisher
from techai_webutils.core.interfaces.llm import (
    LLMConfig,
    LLMMessage,
    LLMProvider,
    LLMResponse,
)
from techai_webutils.core.types.fact import Fact
from tests.json_path import items

_MODULE = "techai_webutils.clients.decorators.ai_enricher"
"""Import site of the enricher (its OTel span lookup and clock are patched here)."""


def _llm() -> MagicMock:
    """Return an ``LLMProvider`` mock returning a fixed response.

    With 42 input / 7 output tokens.

    Returns:
        An ``LLMProvider`` mock returning a fixed response.

    """
    inner = MagicMock(spec=LLMProvider)
    inner.complete = AsyncMock(
        return_value=LLMResponse(
            content="ok",
            model="nova",
            input_tokens=42,
            output_tokens=7,
            finish_reason="stop",
        )
    )
    return inner


@pytest.mark.asyncio
async def test_enricher_emits_gen_ai_tokens_total_from_response(
    metrics_mock: tuple[MagicMock, dict[str, MagicMock]],
) -> None:
    """Test that one ``complete`` increments ``gen_ai_tokens_total`` once per token type.

    **Why this test is important:**
      - The token-usage panels and cost alerts are built on this counter and its exact
        labels.

    **What it tests:**
      - the counter is declared once with labels ``[step, model, type]``
      - it receives ``inc(42, step="generate", model="nova", type="input")`` and ``inc(7,
        step="generate", model="nova", type="output")``, nothing else

    Args:
        metrics_mock: A ``MetricsProvider`` mock plus its instruments.

    """
    provider, instruments = metrics_mock
    enricher = cast(
        "LLMProvider",
        cast(
            "object",
            AiSpanEnricher(
                _llm(),
                options=AiSpanEnricherOptions(
                    step="generate", capture_content=False, metrics=provider
                ),
            ),
        ),
    )

    with patch(f"{_MODULE}.trace.get_current_span", return_value=MagicMock()):
        _ = await enricher.complete([LLMMessage(role="user", content="hi")])

    assert call(
        "gen_ai_tokens_total",
        "GenAI tokens by step, model and type.",
        ["step", "model", "type"],
    ) in (cast("MagicMock", provider.counter).call_args_list)
    assert cast(
        "object", cast("MagicMock", instruments["gen_ai_tokens_total"].inc).call_args_list
    ) == [
        call(42, step="generate", model="nova", type="input"),
        call(7, step="generate", model="nova", type="output"),
    ]


@pytest.mark.asyncio
async def test_enricher_observes_request_duration(
    metrics_mock: tuple[MagicMock, dict[str, MagicMock]],
) -> None:
    """Test that one ``complete`` observes its wall time.

    On ``gen_ai_request_duration_seconds``.

    **Why this test is important:**
      - The p95 generation-latency panel and its SLO read this histogram; the buckets must
        reach 60 s so slow generations are not all collapsed into ``+Inf``.

    **What it tests:**
      - the histogram is declared with labels ``[step, model]`` and buckets 0.05 … 60
      - a call timed 10.0 → 12.5 s observes exactly 2.5 with ``step="generate",
        model="nova"``

    Args:
        metrics_mock: A ``MetricsProvider`` mock plus its instruments.

    """
    provider, instruments = metrics_mock
    enricher = cast(
        "LLMProvider",
        cast(
            "object",
            AiSpanEnricher(
                _llm(),
                options=AiSpanEnricherOptions(
                    step="generate", capture_content=False, metrics=provider
                ),
            ),
        ),
    )

    with (
        patch(f"{_MODULE}.trace.get_current_span", return_value=MagicMock()),
        patch(f"{_MODULE}.perf_counter", side_effect=[10.0, 12.5]),
    ):
        _ = await enricher.complete([LLMMessage(role="user", content="hi")])

    cast("MagicMock", cast("MagicMock", provider.histogram).assert_called_once_with)(
        "gen_ai_request_duration_seconds",
        "GenAI request duration in seconds by step and model.",
        ["step", "model"],
        [0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 20.0, 30.0, 60.0],
    )
    cast(
        "MagicMock",
        cast(
            "MagicMock", instruments["gen_ai_request_duration_seconds"].observe
        ).assert_called_once_with,
    )(2.5, step="generate", model="nova")


@pytest.mark.asyncio
async def test_enricher_reports_metrics_and_fact_under_the_served_model(
    metrics_mock: tuple[MagicMock, dict[str, MagicMock]],
) -> None:
    """Test that metrics and the fact carry the served model and the fact is stamped.

    At call start.

    **Why this test is important:**
      - Token cost is priced per served model; an alias or inference profile in the
        request would price the call wrongly.
      - A fact stamped at the call's end lands a long generation in the next time bucket.

    **What it tests:**
      - with ``LLMConfig(model="nova-alias")`` and a response from ``nova``, the token
        counter and the fact's ``model`` dim are ``nova``
      - the fact's ``ts`` is the clock's first reading (12:00), not the later one (12:05)

    Args:
        metrics_mock: A ``MetricsProvider`` mock plus its instruments.

    """
    provider, instruments = metrics_mock
    facts = MagicMock(spec=FactPublisher)
    start, end = (
        datetime(2026, 10, 9, 12, 0, tzinfo=UTC),
        datetime(2026, 10, 9, 12, 5, tzinfo=UTC),
    )
    enricher = cast(
        "LLMProvider",
        cast(
            "object",
            AiSpanEnricher(
                _llm(),
                options=AiSpanEnricherOptions(
                    step="generate",
                    capture_content=False,
                    metrics=provider,
                    facts=facts,
                    fact_dimensions=lambda: {"org_id": "org-1"},
                ),
            ),
        ),
    )

    with (
        patch(f"{_MODULE}.trace.get_current_span", return_value=_span(1, 2)),
        patch(f"{_MODULE}._utcnow", side_effect=[start, end]),
    ):
        _ = await enricher.complete(
            [LLMMessage(role="user", content="hi")], LLMConfig(model="nova-alias")
        )

    assert cast(
        "object", cast("MagicMock", instruments["gen_ai_tokens_total"].inc).call_args_list
    ) == [
        call(42, step="generate", model="nova", type="input"),
        call(7, step="generate", model="nova", type="output"),
    ]
    (fact,) = items(
        cast(
            "list[object]",
            cast("MagicMock", cast("MagicMock", facts.publish).call_args).args,
        )[0]
    )
    assert cast("dict[str, object]", fact.dims)["model"] == "nova"
    assert cast("object", fact.ts) == start


def _span(trace_id: int, span_id: int) -> MagicMock:
    """Return a mock current span whose context carries the given.

    (valid) trace and span ids.

    Args:
        trace_id: The trace's id.
        span_id: The span's id.

    Returns:
        A mock current span whose context carries the given.

    """
    span = MagicMock()
    cast("MagicMock", span.get_span_context).return_value = SpanContext(
        trace_id=trace_id, span_id=span_id, is_remote=False
    )
    return span


@pytest.mark.asyncio
async def test_enricher_emits_one_fact_per_call_with_configured_dims(
    metrics_mock: tuple[MagicMock, dict[str, MagicMock]],
) -> None:
    """Test that each model call publishes exactly one ``genai_calls`` fact.

    With the product dims.

    **Why this test is important:**
      - The console's token-by-team and latency panels aggregate these facts; the KE stays
        product-free, so team/workspace come only from the injected ``fact_dimensions``.

    **What it tests:**
      - one ``publish([fact])`` per ``complete``
      - ``org_id`` is taken out of the dims; dims are provider/model/step plus the product
        dims
      - measures are ``duration_s`` 2.5, ``tokens_in`` 42, ``tokens_out`` 7
      - ``idempotency_key`` is ``<trace_id>:<span_id>:<step>`` in hex and ``ts`` is the
        clock's now
      - Prometheus labels stay ``{step, model, type}`` (no product dims leak into them)

    Args:
        metrics_mock: A ``MetricsProvider`` mock plus its instruments.

    """
    provider, instruments = metrics_mock
    facts = MagicMock(spec=FactPublisher)
    now = datetime(2026, 10, 9, 12, 0, tzinfo=UTC)
    enricher = cast(
        "LLMProvider",
        cast(
            "object",
            AiSpanEnricher(
                _llm(),
                options=AiSpanEnricherOptions(
                    step="generate",
                    capture_content=False,
                    metrics=provider,
                    facts=facts,
                    fact_dimensions=lambda: {
                        "org_id": "org-1",
                        "team": "t-1",
                        "workspace": "w-1",
                    },
                ),
            ),
        ),
    )

    with (
        patch(f"{_MODULE}.trace.get_current_span", return_value=_span(0xABC, 0xDEF)),
        patch(f"{_MODULE}.perf_counter", side_effect=[10.0, 12.5]),
        patch(f"{_MODULE}._utcnow", return_value=now),
    ):
        _ = await enricher.complete([LLMMessage(role="user", content="hi")])

    cast("MagicMock", cast("MagicMock", facts.publish).assert_called_once_with)([
        Fact(
            cube="genai_calls",
            org_id="org-1",
            ts=now,
            dims={
                "provider": "unknown",
                "model": "nova",
                "step": "generate",
                "team": "t-1",
                "workspace": "w-1",
            },
            measures={"duration_s": 2.5, "tokens_in": 42, "tokens_out": 7},
            idempotency_key=f"{0xABC:032x}:{0xDEF:016x}:generate",
        )
    ])
    assert {
        tuple(sorted(cast("dict[str, object]", c.kwargs)))
        for c in cast(
            "list[object]",
            cast("MagicMock", instruments["gen_ai_tokens_total"].inc).call_args_list,
        )
    } == {("model", "step", "type")}


@pytest.mark.asyncio
async def test_enricher_fact_publish_error_never_fails_call() -> None:
    """Test that a raising fact publisher.

    Or dimensions callable never fails the model call.

    **Why this test is important:**
      - Facts feed analytics, not the answer; a bad product callable or a broken publisher
        must not turn a successful generation into an error.

    **What it tests:**
      - with ``publish`` raising, and separately with ``fact_dimensions`` raising,
        ``complete`` returns the exact inner response
      - a fact without an ``org_id`` dimension is not published
    """
    raising_publisher = MagicMock(spec=FactPublisher)
    cast("MagicMock", raising_publisher.publish).side_effect = RuntimeError("queue gone")
    quiet_publisher = MagicMock(spec=FactPublisher)

    def _bad_dims() -> dict[str, str]:
        msg = "team"
        raise KeyError(msg)

    cases = [
        (raising_publisher, lambda: {"org_id": "org-1"}),
        (quiet_publisher, _bad_dims),
        (quiet_publisher, lambda: {"team": "t-1"}),
    ]
    with patch(f"{_MODULE}.trace.get_current_span", return_value=_span(1, 2)):
        for publisher, dims in cases:
            inner = _llm()
            expected = cast("object", cast("MagicMock", inner.complete).return_value)
            enricher = cast(
                "LLMProvider",
                cast(
                    "object",
                    AiSpanEnricher(
                        inner,
                        options=AiSpanEnricherOptions(
                            step="generate",
                            capture_content=False,
                            facts=publisher,
                            fact_dimensions=dims,
                        ),
                    ),
                ),
            )
            assert (
                await enricher.complete([LLMMessage(role="user", content="hi")])
                is expected
            )

    cast("MagicMock", cast("MagicMock", raising_publisher.publish).assert_called_once)()
    cast("MagicMock", cast("MagicMock", quiet_publisher.publish).assert_not_called)()
