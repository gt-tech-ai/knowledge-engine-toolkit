"""Unit tests for ``AuditProxy``.

Safety chains around the model call, one audit record per answer.
"""

from __future__ import annotations

import asyncio
from datetime import UTC, datetime
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.decorators.audit_proxy import (
    AuditEnforcement,
    AuditProxy,
    AuditProxyOptions,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.audit import (
    AuditContext,
    AuditedSource,
    AuditRecord,
    AuditSink,
)
from techai_webutils.core.interfaces.llm import (
    LLMMessage,
    LLMProvider,
    LLMResponse,
    StreamUsage,
)
from techai_webutils.core.interfaces.metrics import MetricsProvider
from techai_webutils.core.interfaces.model_gateway import (
    RouteDecision,
    set_route_decision,
)
from techai_webutils.core.interfaces.safety import (
    SafetyCheck,
    SafetyDecision,
    SafetyResult,
)
from techai_webutils.core.interfaces.semantic_cache import CachedAnswer

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

_NOW = datetime(2026, 10, 31, 12, tzinfo=UTC)
_SOURCE = AuditedSource(
    document_id="d-1",
    s3_key="org-1/d-1.pdf",
    trust_level="high",
    classification="internal",
    score=0.8,
    page_number=3,
)
_CTX = AuditContext(
    tenant_id="org-1",
    workspace_id="ws-1",
    user_id="u-1",
    clearance_level="internal",
    request_id="r-1",
    sources=(_SOURCE,),
)
_MESSAGES = [
    LLMMessage(role="system", content="ctx"),
    LLMMessage(role="user", content="my ssn is 123-45-6789"),
]
_RESPONSE = LLMResponse(
    content="the answer",
    model="sonnet",
    input_tokens=12,
    output_tokens=4,
    finish_reason="stop",
)
_CLEAN = SafetyResult(flags=(), decision=SafetyDecision.ALLOW, redacted=None)


def _check(*results: SafetyResult) -> MagicMock:
    """Return a safety check answering ``results`` in turn.

    Args:
        *results: The verdicts the check returns, in order.

    Returns:
        A safety check answering ``results`` in turn.

    """
    check = MagicMock(spec=SafetyCheck)
    check.scan = AsyncMock(side_effect=list(results))
    return check


def _sink(error: Exception | None = None) -> MagicMock:
    """Return an audit sink that records (or raises ``error``).

    Args:
        error: What the sink raises; ``None`` records.

    Returns:
        An audit sink that records (or raises ``error``).

    """
    sink = MagicMock(spec=AuditSink)
    sink.record = AsyncMock(side_effect=error)
    return sink


def _inner() -> MagicMock:
    """Return a provider answering ``_RESPONSE``.

    Returns:
        A provider answering ``_RESPONSE``.

    """
    inner = MagicMock(spec=LLMProvider)
    inner.complete = AsyncMock(return_value=_RESPONSE)
    return inner


def _audit(
    inner: MagicMock,
    sink: MagicMock,
    *,
    checks: tuple[MagicMock | None, MagicMock | None] = (None, None),
    enforcement: AuditEnforcement = AuditEnforcement.ENFORCED,
    metrics: MetricsProvider | None = None,
) -> AuditProxy:
    """Wrap ``inner`` for the ``generate`` step with a fixed clock and context.

    Args:
        inner: The wrapped client.
        sink: The audit sink.
        checks: The safety checks.
        enforcement: What a blocked call does.
        metrics: Receives the audit series.

    Returns:
        The proxy.

    """
    input_check, output_check = checks
    return AuditProxy(
        inner,
        options=AuditProxyOptions(
            sink=sink,
            input_check=input_check or _check(_CLEAN),
            output_check=output_check or _check(_CLEAN),
            enforcement=enforcement,
            step="generate",
            record_context=lambda: _CTX,
            metrics=metrics,
            clock=lambda: _NOW,
        ),
    )


def _recorded(sink: MagicMock) -> AuditRecord:
    """Return the one record ``sink`` received.

    Args:
        sink: The audit sink.

    Returns:
        The one record ``sink`` received.

    """
    cast("MagicMock", cast("AsyncMock", sink.record).assert_awaited_once)()
    return cast(
        "AuditRecord",
        cast("MagicMock", cast("AsyncMock", sink.record).await_args).args[0],
    )


@pytest.mark.asyncio
async def test_audit_proxy_captures_provenance_flags_traceid_decision() -> None:
    """Test that an answered call is recorded.

    The record has its context, provenance, flags and decision.

    **Why this test is important:**
      - The record is the SOC 2 evidence for the answer; a missing field is a missing
        control.

    **What it tests:**
      - a prompt flagged ``pii.ssn`` and redacted reaches the model redacted
      - the record carries the context, the sources, tokens, the PII flag, ``redacted``
        and ``cache_hit=False``
    """
    inner, sink = _inner(), _sink()
    redacted = SafetyResult(
        flags=("pii.ssn",), decision=SafetyDecision.REDACT, redacted="my ssn is [SSN]"
    )

    response = await cast(
        "LLMProvider",
        cast("object", _audit(inner, sink, checks=(_check(redacted), None))),
    ).complete(_MESSAGES)

    assert response is _RESPONSE
    sent = cast(
        "list[object]",
        cast("MagicMock", cast("AsyncMock", inner.complete).await_args).args[0],
    )
    assert [cast("object", m.content) for m in sent] == [
        "ctx",
        "my ssn is [SSN]",
    ]
    assert _recorded(sink) == AuditRecord(
        request_id="r-1",
        trace_id="",
        tenant_id="org-1",
        workspace_id="ws-1",
        user_id="u-1",
        clearance_level="internal",
        model="sonnet",
        provider="unknown",
        prompt="my ssn is [SSN]",
        answer="the answer",
        sources=(_SOURCE,),
        prompt_tokens=12,
        completion_tokens=4,
        pii_flags=("pii.ssn",),
        safety_flags=(),
        decision="redacted",
        cache_hit=False,
        created_at=_NOW,
    )


@pytest.mark.asyncio
async def test_cache_hit_is_audited() -> None:
    """Test that an answer served from the semantic cache is audited as a cache hit.

    **Why this test is important:**
      - A cache hit is still an answer shown to a user; an unaudited hit is a gap in
        evidence.

    **What it tests:**
      - ``audit_cached`` (the cache's ``on_hit`` hook) records ``cache_hit=True`` with the
        cached answer and zero tokens, without calling the model
    """
    inner, sink = _inner(), _sink()
    cached = LLMResponse(
        content="cached",
        model="sonnet",
        input_tokens=0,
        output_tokens=0,
        finish_reason="cache_hit",
    )
    hit = CachedAnswer(
        payload=b"", similarity=0.99, source_query="q", embedding_model="titan"
    )

    _ = await _audit(inner, sink).audit_cached((_MESSAGES,), {}, cached, hit)

    record = _recorded(sink)
    assert (record.cache_hit, record.answer, record.prompt_tokens, record.decision) == (
        True,
        "cached",
        0,
        "allowed",
    )
    cast("MagicMock", cast("AsyncMock", inner.complete).assert_not_awaited)()


_CACHED = LLMResponse(
    content="cached",
    model="sonnet",
    input_tokens=0,
    output_tokens=0,
    finish_reason="cache_hit",
)
_HIT = CachedAnswer(
    payload=b"", similarity=0.99, source_query="q", embedding_model="titan"
)


@pytest.mark.asyncio
async def test_cache_hit_is_blocked_or_redacted_before_it_is_served() -> None:
    """Test that a hit gets the same safety outcome as a live answer, before it is served.

    **Why this test is important:**
      - A rule tightened after an answer was cached must still stop (or redact) it;
        recording the hit as merely flagged would keep serving the answer the policy now
        forbids.

    **What it tests:**
      - an output ``block`` audits the hit ``blocked`` with no answer and raises
        ``FORBIDDEN``
      - an output ``redact`` audits and returns the redacted answer for the cache to serve
    """
    block = SafetyResult(flags=("toxic",), decision=SafetyDecision.BLOCK, redacted=None)
    redact = SafetyResult(
        flags=("pii.email",), decision=SafetyDecision.REDACT, redacted="[EMAIL]"
    )
    blocked_sink, redacted_sink = _sink(), _sink()

    with pytest.raises(AppError) as caught:
        _ = await _audit(
            _inner(), blocked_sink, checks=(None, _check(block))
        ).audit_cached((_MESSAGES,), {}, _CACHED, _HIT)
    served = await _audit(
        _inner(), redacted_sink, checks=(None, _check(redact))
    ).audit_cached((_MESSAGES,), {}, _CACHED, _HIT)

    assert caught.value.code is ErrorCode.FORBIDDEN
    blocked = _recorded(blocked_sink)
    assert (blocked.decision, blocked.answer, blocked.cache_hit) == ("blocked", "", True)
    assert served is not None
    assert served.content == "[EMAIL]"
    assert (
        _recorded(redacted_sink).decision,
        _recorded(redacted_sink).answer,
    ) == ("redacted", "[EMAIL]")


@pytest.mark.asyncio
async def test_cache_hit_records_the_redacted_prompt() -> None:
    """Test that a hit whose prompt the input check redacts is recorded redacted.

    **Why this test is important:**
      - The audit record is append-only and sealed; a raw prompt written there (an SSN)
        can never be scrubbed, and a live call on the same prompt records it redacted.

    **What it tests:**
      - with an input ``redact``, the hit's ``AuditRecord.prompt`` is the redacted text
    """
    redact = SafetyResult(
        flags=("pii.ssn",), decision=SafetyDecision.REDACT, redacted="my ssn is [SSN]"
    )
    sink = _sink()

    _ = await _audit(_inner(), sink, checks=(_check(redact), None)).audit_cached(
        (_MESSAGES,), {}, _CACHED, _HIT
    )

    assert _recorded(sink).prompt == "my ssn is [SSN]"


@pytest.mark.asyncio
async def test_record_takes_the_route_model_only_when_the_call_routed() -> None:
    """Test that a route decision names the record's model only if this call set it.

    **Why this test is important:**
      - The decision ContextVar holds the previous step's route; stamping it on a cache
        hit, or on a call through a plain provider, records a model that never produced
        the answer.

    **What it tests:**
      - with a lingering decision for ``haiku``: a cache hit and a plain provider's call
        each record their own model (``sonnet``)
      - a call that sets a decision (a gateway) records the decision's model
    """
    set_route_decision(RouteDecision(model_id="anthropic/claude-haiku-4-5"))
    hit_sink, plain_sink, routed_sink = _sink(), _sink(), _sink()

    async def routed_complete(*_args: object, **_kwargs: object) -> LLMResponse:
        """Answer as a routed call: set the route decision first.

        Args:
            *_args: Unused.
            **_kwargs: Unused.

        Returns:
            The canned response.

        """
        await asyncio.sleep(0)
        set_route_decision(RouteDecision(model_id="anthropic/claude-sonnet-4-6"))
        return _RESPONSE

    gateway = _inner()
    gateway.complete = AsyncMock(side_effect=routed_complete)

    _ = await _audit(_inner(), hit_sink).audit_cached((_MESSAGES,), {}, _CACHED, _HIT)
    _ = await cast("LLMProvider", cast("object", _audit(_inner(), plain_sink))).complete(
        _MESSAGES
    )
    _ = await cast("LLMProvider", cast("object", _audit(gateway, routed_sink))).complete(
        _MESSAGES
    )

    assert _recorded(hit_sink).model == "sonnet"
    assert _recorded(plain_sink).model == "sonnet"
    assert _recorded(routed_sink).model == "anthropic/claude-sonnet-4-6"


@pytest.mark.asyncio
async def test_enforced_raises_coded_error_on_sink_failure() -> None:
    """Test that enforced auditing fails closed with a coded error.

    **Why this test is important:**
      - Under enforcement an unrecorded answer must not be served.

    **What it tests:**
      - an uncoded sink failure raises ``INTERNAL``; a coded one keeps its code
        (``UNAVAILABLE``)
    """
    with pytest.raises(AppError) as uncoded:
        _ = await cast(
            "LLMProvider", cast("object", _audit(_inner(), _sink(RuntimeError("boom"))))
        ).complete(_MESSAGES)
    with pytest.raises(AppError) as coded:
        _ = await cast(
            "LLMProvider",
            cast(
                "object",
                _audit(_inner(), _sink(AppError(ErrorCode.UNAVAILABLE, "aiops down"))),
            ),
        ).complete(_MESSAGES)

    assert uncoded.value.code is ErrorCode.INTERNAL
    assert coded.value.code is ErrorCode.UNAVAILABLE


@pytest.mark.asyncio
async def test_shadow_swallows_and_logs_sink_failure() -> None:
    """Test that shadow auditing serves the answer through a sink failure and counts it.

    **Why this test is important:**
      - Shadow is the rollout stage: the audit store's outage must not reach users.

    **What it tests:**
      - the response is returned; ``audit_record_failures_total{enforcement="shadow"}``
        increments
    """
    failures = MagicMock()
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter = MagicMock(return_value=failures)

    response = await cast(
        "LLMProvider",
        cast(
            "object",
            _audit(
                _inner(),
                _sink(RuntimeError("boom")),
                enforcement=AuditEnforcement.SHADOW,
                metrics=metrics,
            ),
        ),
    ).complete(_MESSAGES)

    assert response is _RESPONSE
    cast("MagicMock", cast("MagicMock", failures.inc).assert_called_once_with)(
        1.0, enforcement="shadow"
    )


@pytest.mark.asyncio
async def test_block_short_circuits_forbidden_and_still_audited() -> None:
    """Test that blocked prompts and answers are stopped and audited.

    A blocked prompt never reaches the model; a blocked answer is withheld.

    **Why this test is important:**
      - A block that skipped the audit would hide exactly the requests auditors look for.

    **What it tests:**
      - an input ``block`` raises ``FORBIDDEN`` without calling the model; the record says
        ``blocked``
      - an output ``block`` raises ``FORBIDDEN`` after the call; its record says
        ``blocked``
    """
    blocked = SafetyResult(
        flags=("content.violence",), decision=SafetyDecision.BLOCK, redacted=None
    )
    inner, sink = _inner(), _sink()

    with pytest.raises(AppError) as before:
        _ = await cast(
            "LLMProvider",
            cast("object", _audit(inner, sink, checks=(_check(blocked), None))),
        ).complete(_MESSAGES)
    cast("MagicMock", cast("AsyncMock", inner.complete).assert_not_awaited)()
    first = _recorded(sink)
    cast("MagicMock", cast("MagicMock", sink.record).reset_mock)()
    with pytest.raises(AppError) as after:
        _ = await cast(
            "LLMProvider",
            cast("object", _audit(inner, sink, checks=(None, _check(blocked)))),
        ).complete(_MESSAGES)

    assert (before.value.code, after.value.code) == (
        ErrorCode.FORBIDDEN,
        ErrorCode.FORBIDDEN,
    )
    assert (first.decision, first.safety_flags, first.answer) == (
        "blocked",
        ("content.violence",),
        "",
    )
    assert _recorded(sink).decision == "blocked"


async def _items(*items: str | StreamUsage) -> AsyncIterator[str | StreamUsage]:
    """Yield ``items``.

    Args:
        *items: The chunks and usage to stream.

    Yields:
        ``items``.

    """
    for item in items:
        await asyncio.sleep(0)
        yield item


@pytest.mark.asyncio
async def test_stream_is_audited_on_exhaustion() -> None:
    """Test that a stream is audited once it ends, over the text it produced.

    **Why this test is important:**
      - Chat streams; an unaudited stream would leave the main path without evidence.

    **What it tests:**
      - the items pass through; the record has the joined answer and the streamed tokens
      - an output ``block`` on a stream is recorded ``flagged_after_stream`` (tokens
        already sent)
    """
    usage = StreamUsage(
        model="sonnet", input_tokens=5, output_tokens=2, finish_reason="stop"
    )
    blocked = SafetyResult(
        flags=("content.hate",), decision=SafetyDecision.BLOCK, redacted=None
    )
    inner, sink = _inner(), _sink()
    inner.stream_with_usage = AsyncMock(
        side_effect=lambda *_a, **_k: _items("the ", "answer", usage)
    )
    proxy = cast(
        "MagicMock", cast("object", _audit(inner, sink, checks=(None, _check(blocked))))
    )

    items = [
        item
        async for item in cast(
            "AsyncIterator[object]",
            await cast("MagicMock", proxy.stream_with_usage)(_MESSAGES),
        )
    ]

    record = _recorded(sink)
    assert items == ["the ", "answer", usage]
    assert (record.answer, record.prompt_tokens, record.completion_tokens) == (
        "the answer",
        5,
        2,
    )
    assert record.decision == "flagged_after_stream"


@pytest.mark.asyncio
async def test_failed_safety_scan_is_flagged_in_shadow_and_raised_when_enforced() -> None:
    """Test that a safety backend outage follows the audit enforcement mode.

    **Why this test is important:**
      - A managed scanner (Comprehend, Guardrails) can be down; shadow must keep serving
        while recording that the scan didn't happen, and enforcement must not serve
        unscanned.

    **What it tests:**
      - shadow: the answer is served and the record carries ``safety.check_failed``
      - enforced: the scanner's coded ``UNAVAILABLE`` reaches the caller before the model
        call
    """
    down = MagicMock(spec=SafetyCheck)
    down.scan = AsyncMock(side_effect=AppError(ErrorCode.UNAVAILABLE, "comprehend down"))
    inner, sink = _inner(), _sink()

    served = await cast(
        "LLMProvider",
        cast(
            "object",
            _audit(inner, sink, enforcement=AuditEnforcement.SHADOW, checks=(down, None)),
        ),
    ).complete(_MESSAGES)
    flags = _recorded(sink).safety_flags
    cast("MagicMock", cast("MagicMock", inner.complete).reset_mock)()
    with pytest.raises(AppError) as caught:
        _ = await cast(
            "LLMProvider", cast("object", _audit(inner, _sink(), checks=(down, None)))
        ).complete(_MESSAGES)

    assert served is _RESPONSE
    assert flags == ("safety.check_failed",)
    assert caught.value.code is ErrorCode.UNAVAILABLE
    cast("MagicMock", cast("AsyncMock", inner.complete).assert_not_awaited)()
