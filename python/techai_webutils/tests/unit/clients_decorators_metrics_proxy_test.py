"""Unit tests for ``MetricsProxy`` and the RED metrics the Python client stack now emits."""

from __future__ import annotations

from typing import Protocol, cast
from unittest.mock import AsyncMock, MagicMock, call, patch

import pytest
from structlog.testing import capture_logs

from techai_webutils.clients.decorators.ai_enricher import AiSpanEnricher
from techai_webutils.clients.decorators.metrics_proxy import MetricsProxy
from techai_webutils.clients.decorators.proxy import (
    ClientStackConfig,
    new_client_stack_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.llm import LLMMessage, LLMProvider, LLMResponse
from techai_webutils.core.interfaces.metrics import MetricCounter


class _KbClient(Protocol):
    """The consumer-side surface of the wrapped client the mock is spec'd on."""

    async def fetch(self) -> str:
        """Return the fetched payload."""
        ...

    async def missing(self) -> str:
        """Fail with a coded ``NOT_FOUND``."""
        ...

    async def broken(self) -> str:
        """Fail with an uncoded exception."""
        ...


def _client() -> MagicMock:
    """Return a spec'd client mock: ``fetch`` returns ``"payload"``, the other two raise."""
    client = MagicMock(spec=_KbClient)
    client.fetch = AsyncMock(return_value="payload")
    client.missing = AsyncMock(side_effect=AppError(ErrorCode.NOT_FOUND, "gone"))
    client.broken = AsyncMock(side_effect=RuntimeError("socket closed"))
    return client


@pytest.mark.asyncio
async def test_client_stack_emits_red_metrics(
    metrics_mock: tuple[MagicMock, dict[str, MagicMock]],
) -> None:
    """Test that the composed client stack records rate, errors and duration per client and method.

    **Why this test is important:**
      - The client-health dashboard and its alerts read these three series; before this the
        Python stack emitted none, so a failing dependency was invisible outside traces.

    **What it tests:**
      - success → ``client_operations_total{client="kb",method="fetch",outcome="ok"}`` and one
        duration observation of exactly 0.25 s
      - an ``AppError`` → ``outcome="error"`` and ``client_errors_total{code="NOT_FOUND"}``
      - a non-coded exception → ``client_errors_total{code="UNKNOWN"}``; both errors re-raise
    """
    provider, instruments = metrics_mock
    stacked = new_client_stack_from_config(
        _client(),
        "kb",
        ClientStackConfig(timeout_seconds=None, bulkhead_max_concurrent=None),
        metrics=provider,
    )

    with patch(
        "techai_webutils.clients.decorators.metrics_proxy.perf_counter",
        side_effect=[1.0, 1.25, 2.0, 2.1, 3.0, 3.1],
    ):
        assert await stacked.fetch() == "payload"
        with pytest.raises(AppError):
            await stacked.missing()
        with pytest.raises(RuntimeError):
            await stacked.broken()

    assert instruments["client_operations_total"].inc.call_args_list == [
        call(client="kb", method="fetch", outcome="ok"),
        call(client="kb", method="missing", outcome="error"),
        call(client="kb", method="broken", outcome="error"),
    ]
    assert instruments["client_errors_total"].inc.call_args_list == [
        call(client="kb", method="missing", code="NOT_FOUND"),
        call(client="kb", method="broken", code="UNKNOWN"),
    ]
    assert instruments["client_operation_duration_seconds"].observe.call_args_list[
        0
    ] == call(0.25, client="kb", method="fetch")


def test_metrics_proxy_declares_instruments_once_with_contract_labels(
    metrics_mock: tuple[MagicMock, dict[str, MagicMock]],
) -> None:
    """Test that ``MetricsProxy`` declares the three RED instruments once, with the contract labels.

    **Why this test is important:**
      - Instruments created per call would re-register on every request; mislabelled ones would
        not join the dashboards' queries.

    **What it tests:**
      - exactly one declaration each of ``client_operations_total{client,method,outcome}``,
        ``client_errors_total{client,method,code}`` and
        ``client_operation_duration_seconds{client,method}``
      - a non-callable attribute passes through
    """
    provider, _ = metrics_mock
    client = MagicMock()
    client.endpoint = "http://x"
    client.fetch = AsyncMock(return_value=1)

    proxy = MetricsProxy(client, "kb", provider)

    assert [c.args[0] for c in provider.counter.call_args_list] == [
        "client_operations_total",
        "client_errors_total",
    ]
    assert provider.counter.call_args_list[0].args[2] == ["client", "method", "outcome"]
    assert provider.counter.call_args_list[1].args[2] == ["client", "method", "code"]
    assert (
        provider.histogram.call_args_list[0].args[0]
        == "client_operation_duration_seconds"
    )
    assert provider.histogram.call_args_list[0].args[2] == ["client", "method"]
    assert proxy.endpoint == "http://x"


@pytest.mark.asyncio
async def test_metrics_emit_failure_does_not_propagate(
    metrics_mock: tuple[MagicMock, dict[str, MagicMock]],
) -> None:
    """Test that a failing metrics backend never changes a call's result, for both emitters.

    **Why this test is important:**
      - Metrics are best-effort; a broken registry (duplicate registration, label mismatch) must
        not turn every client call or generation into an error.

    **What it tests:**
      - with every ``inc`` raising, ``MetricsProxy`` returns the exact client result and the
        enricher returns the exact ``LLMResponse``
      - each logs one warning naming the failed emission
    """
    provider, _ = metrics_mock
    failing = MagicMock(spec=MetricCounter)
    failing.inc.side_effect = RuntimeError("duplicate timeseries")
    provider.counter.side_effect = None
    provider.counter.return_value = failing
    response = LLMResponse(
        content="ok", model="m", input_tokens=1, output_tokens=1, finish_reason="stop"
    )
    llm = MagicMock(spec=LLMProvider)
    llm.complete = AsyncMock(return_value=response)

    with (
        capture_logs() as logs,
        patch("techai_webutils.clients.decorators.ai_enricher.trace.get_current_span"),
    ):
        client = cast("_KbClient", MetricsProxy(_client(), "kb", provider))
        enricher = cast(
            "LLMProvider",
            AiSpanEnricher(llm, step="generate", capture_content=False, metrics=provider),
        )
        client_result = await client.fetch()
        llm_result = await enricher.complete([LLMMessage(role="user", content="hi")])

    assert client_result == "payload"
    assert llm_result is response
    warnings = [(e["event"], e["log_level"]) for e in logs if e["log_level"] == "warning"]
    assert warnings == [
        ("client metrics emit failed", "warning"),
        ("ai span enrichment failed", "warning"),
    ]
