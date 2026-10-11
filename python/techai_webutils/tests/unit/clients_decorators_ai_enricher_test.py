"""Unit tests for ``AiSpanEnricher``: GenAI attributes stamped on the current span.

The enricher opens no span; it stamps the span already current (the step pipeline's span) with the
GenAI semantic attributes, so the OTel span is the only boundary mocked here.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Any, cast
from unittest.mock import AsyncMock, MagicMock, call, patch

import pytest

from techai_webutils.clients.decorators.ai_enricher import (
    GEN_AI_SYSTEM_BY_PROVIDER,
    AiSpanEnricher,
)
from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.core.interfaces.llm import (
    LLMConfig,
    LLMMessage,
    LLMProvider,
    LLMResponse,
    StreamUsage,
)
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

_SPAN_SOURCE = "techai_webutils.clients.decorators.ai_enricher.trace.get_current_span"
"""Import site of the OTel current-span lookup the enricher stamps."""


def _messages() -> list[LLMMessage]:
    """Return a system + user message pair."""
    return [
        LLMMessage(role="system", content="be brief"),
        LLMMessage(role="user", content="hello"),
    ]


def _attributes(span: MagicMock) -> dict[str, Any]:
    """Collect every ``set_attribute(key, value)`` call on the mock span into a dict."""
    return {c.args[0]: c.args[1] for c in span.set_attribute.call_args_list}


def _llm(response: LLMResponse) -> MagicMock:
    """Return an ``LLMProvider`` mock (spec-bound) whose ``complete`` returns ``response``."""
    inner = MagicMock(spec=LLMProvider)
    inner.complete = AsyncMock(return_value=response)
    return inner


@pytest.mark.asyncio
async def test_ai_span_enricher_stamps_gen_ai_usage_from_llm_response() -> None:
    """Test that ``complete`` stamps the GenAI usage, model, finish reason, system and step.

    **Why this test is important:**
      - The trace explorer and the token panels read these exact attribute names; a renamed or
        missing attribute leaves the AI dashboards empty while the call itself succeeds.

    **What it tests:**
      - the inner result is returned unchanged
      - the current span receives exactly ``gen_ai.system``, ``gen_ai.step``,
        ``gen_ai.request.model``, ``gen_ai.response.model``, ``gen_ai.usage.input_tokens``,
        ``gen_ai.usage.output_tokens`` and ``gen_ai.response.finish_reason`` with the response's values
      - no span is opened (no span ``end``)
    """
    response = LLMResponse(
        content="hi",
        model="nova-lite",
        input_tokens=42,
        output_tokens=7,
        finish_reason="stop",
    )
    inner = _llm(response)
    span = MagicMock()
    enricher = cast(
        "LLMProvider", AiSpanEnricher(inner, step="generate", capture_content=False)
    )

    with patch(_SPAN_SOURCE, return_value=span):
        result = await enricher.complete(_messages(), LLMConfig(model="nova-lite"))

    assert result is response
    assert _attributes(span) == {
        "gen_ai.system": "unknown",
        "gen_ai.step": "generate",
        "gen_ai.request.model": "nova-lite",
        "gen_ai.response.model": "nova-lite",
        "gen_ai.usage.input_tokens": 42,
        "gen_ai.usage.output_tokens": 7,
        "gen_ai.response.finish_reason": "stop",
    }
    span.end.assert_not_called()


@pytest.mark.asyncio
async def test_ai_span_enricher_maps_provider_class_to_gen_ai_system() -> None:
    """Test that the provider's class name selects ``gen_ai.system`` and other attributes pass through.

    **Why this test is important:**
      - Dashboards split cost and latency by provider; the mapping is keyed by class name so a
        wrapped provider must still report the right system.

    **What it tests:**
      - ``GEN_AI_SYSTEM_BY_PROVIDER`` maps the three built-in providers exactly
      - wrapping a real ``StubLlmProvider`` stamps ``gen_ai.system == "stub"``
      - an unenriched method (``model_name``) passes through untouched
    """
    span = MagicMock()
    enricher = cast(
        "LLMProvider",
        AiSpanEnricher(StubLlmProvider(), step="rewrite", capture_content=False),
    )

    with patch(_SPAN_SOURCE, return_value=span):
        await enricher.complete(_messages())

    assert GEN_AI_SYSTEM_BY_PROVIDER == {
        "BedrockLlmProvider": "aws.bedrock",
        "FallbackLlmProvider": "aws.bedrock",
        "OllamaLlmProvider": "ollama",
        "StubLlmProvider": "stub",
    }
    assert _attributes(span)["gen_ai.system"] == "stub"
    assert enricher.model_name() == "stub-llm"


async def _usage_stream(*items: str | StreamUsage) -> AsyncIterator[str | StreamUsage]:
    """Yield the given stream items in order."""
    await asyncio.sleep(0)
    for item in items:
        yield item


@pytest.mark.asyncio
async def test_ai_span_enricher_stream_stamps_usage_at_exhaustion() -> None:
    """Test that ``stream`` re-yields only text and stamps usage when the ``StreamUsage`` arrives.

    **Why this test is important:**
      - The WS query path streams; usage is known only at the end, and a usage item leaking into
        the text stream would corrupt the answer.

    **What it tests:**
      - the caller sees exactly ``["Hel", "lo"]``
      - the span gets the usage counts, model and finish reason from the terminal ``StreamUsage``
      - nothing is stamped before the stream is consumed
    """
    usage = StreamUsage(
        model="nova-lite", input_tokens=30, output_tokens=2, finish_reason="end_turn"
    )
    inner = MagicMock(spec=LLMProvider)
    inner.stream_with_usage = AsyncMock(return_value=_usage_stream("Hel", "lo", usage))
    span = MagicMock()
    enricher = cast(
        "LLMProvider", AiSpanEnricher(inner, step="generate", capture_content=False)
    )

    with patch(_SPAN_SOURCE, return_value=span):
        iterator = await enricher.stream(_messages())
        assert span.set_attribute.call_count == 0
        tokens = [t async for t in iterator]

    assert tokens == ["Hel", "lo"]
    assert _attributes(span) == {
        "gen_ai.system": "unknown",
        "gen_ai.step": "generate",
        "gen_ai.request.model": "nova-lite",
        "gen_ai.response.model": "nova-lite",
        "gen_ai.usage.input_tokens": 30,
        "gen_ai.usage.output_tokens": 2,
        "gen_ai.response.finish_reason": "end_turn",
    }


@pytest.mark.asyncio
async def test_ai_span_enricher_stream_with_usage_keeps_usage_and_stamps() -> None:
    """Test that ``stream_with_usage`` is enriched and still hands the caller its ``StreamUsage``.

    **Why this test is important:**
      - A caller that reads usage itself (cost accounting) calls ``stream_with_usage`` directly;
        passing it through unwrapped left those generations off the span, metrics and facts.

    **What it tests:**
      - the caller sees exactly ``["Hel", "lo", usage]`` (the usage item is not swallowed)
      - the span gets the requested model from the config and the served model from the usage
    """
    usage = StreamUsage(
        model="nova-lite-v1", input_tokens=30, output_tokens=2, finish_reason="end_turn"
    )
    inner = MagicMock(spec=LLMProvider)
    inner.stream_with_usage = AsyncMock(return_value=_usage_stream("Hel", "lo", usage))
    span = MagicMock()
    enricher = cast(
        "LLMProvider", AiSpanEnricher(inner, step="generate", capture_content=False)
    )

    with patch(_SPAN_SOURCE, return_value=span):
        iterator = await enricher.stream_with_usage(
            _messages(), LLMConfig(model="nova-lite")
        )
        items = [t async for t in iterator]

    assert items == ["Hel", "lo", usage]
    assert _attributes(span) == {
        "gen_ai.system": "unknown",
        "gen_ai.step": "generate",
        "gen_ai.request.model": "nova-lite",
        "gen_ai.response.model": "nova-lite-v1",
        "gen_ai.usage.input_tokens": 30,
        "gen_ai.usage.output_tokens": 2,
        "gen_ai.response.finish_reason": "end_turn",
    }


@pytest.mark.asyncio
async def test_ai_span_enricher_reports_requested_and_served_model() -> None:
    """Test that ``complete`` stamps the requested and the served model separately.

    **Why this test is important:**
      - A request may name an alias or an inference profile while another model serves it; cost
        and latency must be attributed to the model that ran, not the one asked for.

    **What it tests:**
      - with ``LLMConfig(model="nova-alias")`` and a response from ``nova-lite-v1``,
        ``gen_ai.request.model == "nova-alias"`` and ``gen_ai.response.model == "nova-lite-v1"``
      - with no config, both are the response's model
    """
    response = LLMResponse(
        content="hi",
        model="nova-lite-v1",
        input_tokens=1,
        output_tokens=1,
        finish_reason="stop",
    )
    aliased, plain = MagicMock(), MagicMock()
    enricher = cast(
        "LLMProvider",
        AiSpanEnricher(_llm(response), step="generate", capture_content=False),
    )

    with patch(_SPAN_SOURCE, return_value=aliased):
        await enricher.complete(_messages(), LLMConfig(model="nova-alias"))
    with patch(_SPAN_SOURCE, return_value=plain):
        await enricher.complete(_messages())

    assert _attributes(aliased)["gen_ai.request.model"] == "nova-alias"
    assert _attributes(aliased)["gen_ai.response.model"] == "nova-lite-v1"
    assert _attributes(plain)["gen_ai.request.model"] == "nova-lite-v1"
    assert _attributes(plain)["gen_ai.response.model"] == "nova-lite-v1"


def _result(i: int) -> RetrievalResult:
    """Return a retrieval result whose document id is ``doc-<i>``."""
    return RetrievalResult(
        document_id=f"doc-{i}",
        document_name=f"d{i}",
        chunk_content="x",
        score=1.0,
        page_number=None,
        metadata={},
    )


@pytest.mark.asyncio
async def test_ai_span_enricher_stamps_retrieval_shape() -> None:
    """Test that ``retrieve`` stamps top_k, the result count and at most 20 document ids.

    **Why this test is important:**
      - Retrieval quality triage needs the requested ``top_k`` and the ids actually returned;
        an unbounded id list would blow the span attribute size limit.

    **What it tests:**
      - 21 results → ``retrieval.result_count == 21`` and exactly the first 20 ids, comma-joined
      - ``retrieval.top_k`` is the argument (25) and ``gen_ai.step`` is the configured step
      - the inner result list is returned unchanged
    """
    results = [_result(i) for i in range(21)]
    inner = MagicMock(spec=RetrievalEngine)
    inner.retrieve = AsyncMock(return_value=results)
    span = MagicMock()
    enricher = cast(
        "RetrievalEngine", AiSpanEnricher(inner, step="retrieve", capture_content=False)
    )

    with patch(_SPAN_SOURCE, return_value=span):
        got = await enricher.retrieve("q", top_k=25, filters={"org": "o1"})

    assert got is results
    inner.retrieve.assert_awaited_once_with("q", top_k=25, filters={"org": "o1"})
    assert _attributes(span) == {
        "gen_ai.step": "retrieve",
        "retrieval.top_k": 25,
        "retrieval.result_count": 21,
        "retrieval.document_ids": ",".join(f"doc-{i}" for i in range(20)),
    }
    assert span.set_attribute.call_args_list[0] == call("gen_ai.step", "retrieve")


@pytest.mark.asyncio
async def test_ai_span_enricher_capture_content_false_records_no_text() -> None:
    """Test that with ``capture_content=False`` no prompt or completion text reaches the span.

    **Why this test is important:**
      - Prompts and answers carry customer data; content capture is off by default and must
        record nothing at all when off.

    **What it tests:**
      - after ``complete`` and a drained ``stream``, ``add_event`` was never called
    """
    response = LLMResponse(
        content="secret", model="m", input_tokens=1, output_tokens=1, finish_reason="stop"
    )
    inner = _llm(response)
    inner.stream_with_usage = AsyncMock(return_value=_usage_stream("secret"))
    span = MagicMock()
    enricher = cast(
        "LLMProvider", AiSpanEnricher(inner, step="generate", capture_content=False)
    )

    with patch(_SPAN_SOURCE, return_value=span):
        await enricher.complete(_messages())
        _ = [t async for t in await enricher.stream(_messages())]

    span.add_event.assert_not_called()


@pytest.mark.asyncio
async def test_ai_span_enricher_capture_content_true_redacts_pii() -> None:
    """Test that captured prompt and completion events are PII-redacted and capped at 4 KiB.

    **Why this test is important:**
      - Even when an operator turns capture on for debugging, e-mails and tokens must not land in
        the trace store, and one huge prompt must not exceed span size limits.

    **What it tests:**
      - ``complete`` adds ``gen_ai.content.prompt`` with the joined, redacted message contents and
        ``gen_ai.content.completion`` with the redacted completion
      - a streamed completion is captured as the joined text, truncated to exactly 4096 bytes
    """
    messages = [
        LLMMessage(role="system", content="be brief"),
        LLMMessage(role="user", content="mail bob@x.io"),
    ]
    response = LLMResponse(
        content="reply to bob@x.io",
        model="m",
        input_tokens=1,
        output_tokens=1,
        finish_reason="stop",
    )
    inner = _llm(response)
    inner.stream_with_usage = AsyncMock(
        return_value=_usage_stream("a" * 3000, "b" * 3000)
    )
    span = MagicMock()
    enricher = cast(
        "LLMProvider", AiSpanEnricher(inner, step="generate", capture_content=True)
    )

    with patch(_SPAN_SOURCE, return_value=span):
        await enricher.complete(messages)
        _ = [t async for t in await enricher.stream(messages)]

    events = span.add_event.call_args_list
    assert events[0] == call(
        "gen_ai.content.prompt", {"gen_ai.prompt": "be brief\nmail [REDACTED]"}
    )
    assert events[1] == call(
        "gen_ai.content.completion", {"gen_ai.completion": "reply to [REDACTED]"}
    )
    assert events[2] == call(
        "gen_ai.content.prompt", {"gen_ai.prompt": "be brief\nmail [REDACTED]"}
    )
    assert events[3] == call(
        "gen_ai.content.completion", {"gen_ai.completion": "a" * 3000 + "b" * 1096}
    )
    assert len(events) == 4


@pytest.mark.asyncio
async def test_ai_span_enricher_enrichment_error_does_not_propagate() -> None:
    """Test that a failure while stamping never fails or alters the wrapped call.

    **Why this test is important:**
      - Observability is best-effort; a broken span exporter or attribute validator must never
        turn a successful generation or retrieval into a user-facing error.

    **What it tests:**
      - with ``set_attribute`` raising, ``complete`` returns the exact inner response, ``stream``
        yields the exact text and ``retrieve`` returns the exact result list
    """
    response = LLMResponse(
        content="ok", model="m", input_tokens=1, output_tokens=1, finish_reason="stop"
    )
    llm = _llm(response)
    llm.stream_with_usage = AsyncMock(
        return_value=_usage_stream(
            "o",
            "k",
            StreamUsage(model="m", input_tokens=1, output_tokens=1, finish_reason="stop"),
        )
    )
    results = [_result(0)]
    engine = MagicMock(spec=RetrievalEngine)
    engine.retrieve = AsyncMock(return_value=results)
    span = MagicMock()
    span.set_attribute.side_effect = RuntimeError("exporter down")

    llm_enricher = cast(
        "LLMProvider", AiSpanEnricher(llm, step="generate", capture_content=True)
    )
    engine_enricher = cast(
        "RetrievalEngine", AiSpanEnricher(engine, step="retrieve", capture_content=False)
    )
    with patch(_SPAN_SOURCE, return_value=span):
        got_response = await llm_enricher.complete(_messages())
        tokens = [t async for t in await llm_enricher.stream(_messages())]
        got_results = await engine_enricher.retrieve("q")

    assert got_response is response
    assert tokens == ["o", "k"]
    assert got_results is results
