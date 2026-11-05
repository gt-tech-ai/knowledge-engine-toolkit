"""Unit and property tests for the routed gateway's health-aware fallback executor."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Any, cast
from unittest.mock import AsyncMock, MagicMock

import pytest
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.clients.model_gateway.routed.catalog import ModelCatalog
from techai_webutils.clients.model_gateway.routed.executor import ChainExecutor
from techai_webutils.core.errors import AppError, ErrorCode, UnavailableError
from techai_webutils.core.interfaces.llm import LLMProvider, LLMResponse, StreamUsage
from techai_webutils.core.types.llm import LlmConfig, LlmKind
from techai_webutils.core.types.routing import Capabilities, CatalogEndpointRef

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

_CAPS = Capabilities(context_tokens=10_000, max_output=1000)


def _ref(name: str) -> CatalogEndpointRef:
    """Return an endpoint ref named ``m/<name>@e``."""
    return CatalogEndpointRef(
        f"m/{name}", "e", _CAPS, 0.5, 100, "us-east-1", zero_retention=True
    )


def _response(model: str) -> LLMResponse:
    """Return a completion attributed to ``model``."""
    return LLMResponse(
        content="ok", model=model, input_tokens=3, output_tokens=2, finish_reason="stop"
    )


def _provider(model: str, error: Exception | None = None) -> MagicMock:
    """Return a provider that answers ``model`` or raises ``error``."""
    provider = MagicMock(spec=LLMProvider)
    provider.complete = AsyncMock(side_effect=error, return_value=_response(model))
    return provider


async def _trip(executor: ChainExecutor, ref: CatalogEndpointRef) -> None:
    """Open ``ref``'s breaker (threshold 1) by failing one guarded call."""
    with pytest.raises(UnavailableError):
        await executor.complete(
            (ref,), lambda _r: _provider("x", UnavailableError()), _call
        )


async def _call(provider: LLMProvider) -> LLMResponse:
    """Complete on ``provider`` with an empty prompt."""
    return await provider.complete([])


@pytest.mark.asyncio
async def test_chain_fails_over_on_transient_error() -> None:
    """Test that a transient failure moves to the next endpoint.

    **Why this test is important:**
      - A regional throttle must fail over, not fail the answer.

    **What it tests:**
      - the primary raises ``UNAVAILABLE``, the secondary answers; attempts are
        ``failed (UNAVAILABLE)`` then ``ok``
    """
    a, b = _ref("a"), _ref("b")
    providers = {
        a.key: _provider("a", UnavailableError("throttled")),
        b.key: _provider("b"),
    }

    response, attempts, served = await ChainExecutor(max_attempts=2).complete(
        (a, b), lambda r: providers[r.key], _call
    )

    assert (response.model, served) == ("b", b)
    assert [(x.endpoint, x.outcome, x.error_code) for x in attempts] == [
        ("m/a@e", "failed", "UNAVAILABLE"),
        ("m/b@e", "ok", ""),
    ]


@pytest.mark.asyncio
async def test_chain_surfaces_permanent_error_without_failover() -> None:
    """Test that a permanent failure is not retried elsewhere.

    **Why this test is important:**
      - A malformed request fails on every model; failing over only multiplies the cost.

    **What it tests:**
      - an ``INVALID_INPUT`` from the primary surfaces and the secondary is never called
    """
    a, b = _ref("a"), _ref("b")
    secondary = _provider("b")
    providers = {
        a.key: _provider("a", AppError(ErrorCode.INVALID_INPUT, "bad prompt")),
        b.key: secondary,
    }

    with pytest.raises(AppError) as caught:
        await ChainExecutor(max_attempts=2).complete(
            (a, b), lambda r: providers[r.key], _call
        )

    assert caught.value.code is ErrorCode.INVALID_INPUT
    secondary.complete.assert_not_awaited()


@pytest.mark.asyncio
async def test_permanent_error_leaves_the_breaker_closed() -> None:
    """Test that a request's own permanent failure never opens an endpoint's breaker.

    **Why this test is important:**
      - A few over-long prompts would otherwise open a healthy endpoint for every tenant and
        push all traffic to a costlier fallback.

    **What it tests:**
      - with a failure threshold of 1, an ``INVALID_INPUT`` on ``a`` leaves ``a`` callable:
        the next call is served by ``a``
    """
    a = _ref("a")
    executor = ChainExecutor(max_attempts=1, failure_threshold=1, recovery_timeout=60)
    with pytest.raises(AppError):
        await executor.complete(
            (a,),
            lambda _r: _provider("a", AppError(ErrorCode.INVALID_INPUT, "too long")),
            _call,
        )

    response, _, _ = await executor.complete((a,), lambda _r: _provider("a"), _call)

    assert response.model == "a"


@pytest.mark.asyncio
async def test_chain_skips_open_breaker_endpoint() -> None:
    """Test that an endpoint whose breaker is open is skipped without a call.

    **Why this test is important:**
      - Calling a known-down endpoint adds its timeout to every request.

    **What it tests:**
      - after tripping ``a``'s breaker, a call skips ``a`` (recorded ``breaker_open``) and
        ``b`` answers; ``a``'s provider is not called again
    """
    a, b = _ref("a"), _ref("b")
    executor = ChainExecutor(max_attempts=2, failure_threshold=1, recovery_timeout=60)
    await _trip(executor, a)
    primary = _provider("a")
    providers = {a.key: primary, b.key: _provider("b")}

    response, attempts, _ = await executor.complete(
        (a, b), lambda r: providers[r.key], _call
    )

    assert response.model == "b"
    assert [(x.endpoint, x.outcome) for x in attempts] == [
        ("m/a@e", "breaker_open"),
        ("m/b@e", "ok"),
    ]
    primary.complete.assert_not_awaited()


@pytest.mark.asyncio
async def test_chain_exhaustion_raises_coded_unavailable_error() -> None:
    """Test that every endpoint failing is a coded ``UNAVAILABLE``.

    **Why this test is important:**
      - The terminal must be the same coded error ``FallbackLlmProvider`` raises, so the
        transport maps it to 503 / gRPC 14.

    **What it tests:**
      - two transient failures raise ``AppError`` with code ``UNAVAILABLE``
      - ``max_attempts=1`` stops after the first endpoint
    """
    a, b = _ref("a"), _ref("b")
    second = _provider("b", UnavailableError())
    providers = {a.key: _provider("a", UnavailableError()), b.key: second}

    with pytest.raises(AppError) as caught:
        await ChainExecutor(max_attempts=2).complete(
            (a, b), lambda r: providers[r.key], _call
        )
    second.complete.reset_mock()
    with pytest.raises(AppError):
        await ChainExecutor(max_attempts=1).complete(
            (a, b), lambda r: providers[r.key], _call
        )

    assert caught.value.code is ErrorCode.UNAVAILABLE
    second.complete.assert_not_awaited()


async def _items(
    *items: str | StreamUsage, fail_after: int | None = None
) -> AsyncIterator[str | StreamUsage]:
    """Yield ``items``, raising ``UNAVAILABLE`` after ``fail_after`` of them."""
    for index, item in enumerate(items):
        if fail_after is not None and index == fail_after:
            msg = "dropped"
            raise UnavailableError(msg)
        await asyncio.sleep(0)
        yield item


@pytest.mark.asyncio
async def test_stream_fails_over_before_first_token_only() -> None:
    """Test that a stream fails over before its first token and surfaces after it.

    **Why this test is important:**
      - Two models' tokens can't be spliced into one answer; a mid-stream failure must
        surface, while a failure to start can still go elsewhere.

    **What it tests:**
      - a primary that fails before any token fails over to the secondary's tokens
      - a primary that fails after one token yields that token then raises, with no failover
    """
    a, b = _ref("a"), _ref("b")

    async def opener(provider: LLMProvider) -> AsyncIterator[str | StreamUsage]:
        return await provider.stream_with_usage([])

    dead, good = MagicMock(spec=LLMProvider), MagicMock(spec=LLMProvider)
    dead.stream_with_usage = AsyncMock(return_value=_items("x", fail_after=0))
    good.stream_with_usage = AsyncMock(return_value=_items("hello", " world"))
    items, attempts, served = await ChainExecutor(max_attempts=2).stream(
        (a, b), lambda r: {a.key: dead, b.key: good}[r.key], opener
    )
    chunks = [item async for item in items]

    late = MagicMock(spec=LLMProvider)
    late.stream_with_usage = AsyncMock(return_value=_items("one", "two", fail_after=1))
    spare = MagicMock(spec=LLMProvider)
    spare.stream_with_usage = AsyncMock(return_value=_items("never"))
    partial, _, _ = await ChainExecutor(max_attempts=2).stream(
        (a, b), lambda r: {a.key: late, b.key: spare}[r.key], opener
    )
    seen: list[str | StreamUsage] = []

    async def drain() -> None:
        async for item in partial:
            seen.append(item)  # noqa: PERF401 - items before the failure must be kept

    with pytest.raises(UnavailableError):
        await drain()

    assert (chunks, served) == (["hello", " world"], b)
    assert [x.outcome for x in attempts] == ["failed", "ok"]
    assert seen == ["one"]
    spare.stream_with_usage.assert_not_awaited()


def test_passthrough_and_routed_never_stack_fallback(
    catalog_raw: dict[str, object],
) -> None:
    """Test that a routed endpoint never carries the LLM tier's own fallback.

    **Why this test is important:**
      - Stacking ``FallbackLlmProvider`` under the routed chain would retry inside an attempt
        and double the calls the attempt bound promises.

    **What it tests:**
      - an endpoint configured with ``fallback_model`` is built with it cleared, so
        ``new_llm_from_config`` returns a bare provider
    """
    raw = dict(catalog_raw)
    sonnet: dict[str, Any] = dict(cast("dict[str, Any]", raw["anthropic/sonnet"]))
    endpoint: dict[str, Any] = dict(sonnet["endpoints"][0])
    endpoint["llm"] = {"kind": "bedrock", "model": "m", "fallback_model": "f"}
    sonnet["endpoints"] = [endpoint]
    raw["anthropic/sonnet"] = sonnet

    config = ModelCatalog.from_mapping(raw, priced_ids=None).llm_configs[
        "anthropic/sonnet@use1"
    ]

    assert config == LlmConfig(kind=LlmKind.BEDROCK, model="m", fallback_model="")


_OUTCOMES = st.lists(
    st.sampled_from(["ok", "transient", "permanent", "open"]), min_size=1, max_size=5
)
_ERRORS: dict[str, Exception] = {
    "transient": UnavailableError(),
    "permanent": AppError(ErrorCode.INVALID_INPUT, "bad prompt"),
}
"""The error each failing outcome raises."""


@given(outcomes=_OUTCOMES, max_attempts=st.integers(1, 5))
def test_executor_attempt_bound_and_breaker_invariant(
    outcomes: list[str], max_attempts: int
) -> None:
    """Test the executor's bounds over generated failure patterns.

    **Why this test is important:**
      - The attempt bound caps what a degraded region can cost; the breaker rule caps the
        latency of a known-down endpoint. Both must hold for every pattern, not three.

    **What it tests:**
      - at most ``max_attempts`` provider calls are made
      - an open-breaker endpoint is never called
      - the first non-transient outcome the bound reaches decides: ``ok`` serves, a permanent
        error surfaces (no failover); with none, ``UNAVAILABLE``
    """

    async def scenario() -> None:
        executor = ChainExecutor(
            max_attempts=max_attempts, failure_threshold=1, recovery_timeout=60
        )
        refs = tuple(_ref(str(i)) for i in range(len(outcomes)))
        for ref, outcome in zip(refs, outcomes, strict=True):
            if outcome == "open":
                await _trip(executor, ref)
        providers = {
            ref.key: _provider(ref.model_id, _ERRORS.get(o))
            for ref, o in zip(refs, outcomes, strict=True)
        }
        callable_outcomes = [o for o in outcomes if o != "open"][:max_attempts]
        expected = next(
            (i for i, o in enumerate(callable_outcomes) if o != "transient"), None
        )
        if expected is None or callable_outcomes[expected] == "permanent":
            with pytest.raises(AppError) as caught:
                await executor.complete(refs, lambda r: providers[r.key], _call)
            want = ErrorCode.UNAVAILABLE if expected is None else ErrorCode.INVALID_INPUT
            assert caught.value.code is want
        else:
            response, _, _ = await executor.complete(
                refs, lambda r: providers[r.key], _call
            )
            healthy = [r for r, o in zip(refs, outcomes, strict=True) if o != "open"]
            assert response.model == healthy[expected].model_id
        calls = sum(p.complete.await_count for p in providers.values())
        assert calls <= max_attempts
        for ref, outcome in zip(refs, outcomes, strict=True):
            if outcome == "open":
                providers[ref.key].complete.assert_not_awaited()

    asyncio.run(scenario())
