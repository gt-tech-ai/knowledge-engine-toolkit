"""Unit tests for the Python metrics queriers (HTTP mocked.

With ``httpx.MockTransport``).
"""

from __future__ import annotations

import re
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import TYPE_CHECKING, cast
from unittest.mock import MagicMock

import httpx
import pytest

from techai_webutils.clients.prometheus import (
    PrometheusConfig,
    PrometheusKind,
    new_metrics_querier_from_config,
)
from techai_webutils.clients.prometheus.stub.querier import StubMetricsQuerier
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.metrics_query import MetricSample, MetricsQuerier

if TYPE_CHECKING:
    from collections.abc import Callable

_GO_INTERFACE = (
    Path(__file__).resolve().parents[4]
    / "go"
    / "core"
    / "interfaces"
    / "metrics_query.go"
)
_T0 = datetime(2026, 11, 5, tzinfo=UTC)


def _querier(
    handler: Callable[[httpx.Request], httpx.Response], *, max_body_bytes: int = 0
) -> MetricsQuerier:
    """Build the http querier over a mocked transport answering with ``handler``.

    Returns:
        The http querier over a mocked transport answering with ``handler``.

    """
    transport = httpx.MockTransport(handler)
    client = httpx.AsyncClient(transport=transport)
    return new_metrics_querier_from_config(
        PrometheusConfig(
            kind=PrometheusKind.HTTP,
            base_url="http://prom/",
            max_body_bytes=max_body_bytes,
        ),
        client=client,
    )


def _ok(result_type: str, result: list[object]) -> httpx.Response:
    """Return a successful query-API envelope.

    Returns:
        A successful query-API envelope.

    """
    return httpx.Response(
        200,
        json={"status": "success", "data": {"resultType": result_type, "result": result}},
    )


@pytest.mark.asyncio
async def test_http_parses_instant_and_range_responses() -> None:
    """Test that the http querier returns an instant value and a range series.

    From Prometheus.

    **Why this test is important:**
      - The sink write governor reads retrieval p99 through this; a misparse would
        throttle ingestion on a wrong number.

    **What it tests:**
      - an instant query sends ``query`` to ``/api/v1/query`` and returns ``0.25``
      - a range query sends start, end and step to ``/api/v1/query_range`` and returns two
        samples with UTC timestamps
    """
    seen: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        if request.url.path == "/api/v1/query":
            return _ok("vector", [{"value": [1_762_300_800, "0.25"]}])
        return _ok("matrix", [{"values": [[1_762_300_800, "1"], [1_762_300_860.5, "2"]]}])

    querier = _querier(handler)

    value = await querier.query("p99")
    series = await querier.query_range(
        "p99", _T0, _T0 + timedelta(minutes=1), timedelta(seconds=30)
    )

    assert value == pytest.approx(0.25)
    assert seen[0].url.params["query"] == "p99"
    assert seen[1].url.params["step"] == "30"
    assert seen[1].url.params["start"] == str(int(_T0.timestamp()))
    assert series == [
        MetricSample(time=datetime.fromtimestamp(1_762_300_800, UTC), value=1.0),
        MetricSample(time=datetime.fromtimestamp(1_762_300_860.5, UTC), value=2.0),
    ]


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("response", "code"),
    [
        (httpx.Response(503, json={"error": "busy"}), ErrorCode.UNAVAILABLE),
        (httpx.Response(429), ErrorCode.UNAVAILABLE),
        (httpx.Response(400, json={"error": "parse error"}), ErrorCode.INVALID_INPUT),
        (httpx.Response(500), ErrorCode.UPSTREAM),
        (httpx.Response(200, json={"status": "error", "error": "x"}), ErrorCode.UPSTREAM),
        (_ok("vector", []), ErrorCode.NOT_FOUND),
        (_ok("vector", [{"value": [1, "NaN"]}]), ErrorCode.NOT_FOUND),
        (
            _ok("vector", [{"value": [1, "1"]}, {"value": [1, "2"]}]),
            ErrorCode.INVALID_INPUT,
        ),
        (_ok("matrix", []), ErrorCode.INVALID_INPUT),
        (_ok("vector", [{"value": [1, "oops"]}]), ErrorCode.UPSTREAM),
    ],
)
async def test_http_failures_are_coded(response: httpx.Response, code: ErrorCode) -> None:
    """Test that every failure of an instant query raises the right coded error.

    **Why this test is important:**
      - Fail closed: a value that cannot be measured must never read as zero, and a
        throttled server must be retried while a bad query is not.

    **What it tests:**
      - transient statuses → ``UNAVAILABLE``; a rejected query → ``INVALID_INPUT``; other
        statuses and error envelopes → ``UPSTREAM``; no sample or NaN → ``NOT_FOUND``;
        several series or a matrix for an instant query → ``INVALID_INPUT``; an
        unparseable value → ``UPSTREAM``
    """
    querier = _querier(lambda _request: response)

    with pytest.raises(AppError) as info:
        _ = await querier.query("q")

    assert info.value.code is code


@pytest.mark.asyncio
async def test_http_transport_failures_and_limits() -> None:
    """Test timeouts, connection errors, the body cap, bad ranges and an empty range.

    **Why this test is important:**
      - A hung Prometheus must not hang the governor, and a huge response must not be
        buffered.

    **What it tests:**
      - a timeout → ``TIMEOUT``; a connection error → ``UNAVAILABLE``; a body over the cap
        and a negative step → ``INVALID_INPUT``; a range with no series is empty
    """

    def timeout(request: httpx.Request) -> httpx.Response:
        msg = "slow"
        raise httpx.ReadTimeout(msg, request=request)

    def refused(request: httpx.Request) -> httpx.Response:
        msg = "refused"
        raise httpx.ConnectError(msg, request=request)

    with pytest.raises(AppError) as slow:
        _ = await _querier(timeout).query("q")
    with pytest.raises(AppError) as down:
        _ = await _querier(refused).query("q")
    with pytest.raises(AppValueError):
        _ = await _querier(lambda _r: _ok("vector", []), max_body_bytes=10).query("q")
    with pytest.raises(AppValueError):
        _ = await _querier(lambda _r: _ok("matrix", [])).query_range(
            "q", _T0, _T0, timedelta(0)
        )
    empty = await _querier(lambda _r: _ok("matrix", [])).query_range(
        "q", _T0, _T0, timedelta(seconds=1)
    )

    assert slow.value.code is ErrorCode.TIMEOUT
    assert down.value.code is ErrorCode.UNAVAILABLE
    assert empty == []


@pytest.mark.asyncio
async def test_stub_default_and_factory_errors() -> None:
    """Test that the default querier answers zero with no I/O.

    And a bad config fails loudly.

    **Why this test is important:**
      - The graph must boot without a metrics server; a misconfigured querier must stop it
        at boot.

    **What it tests:**
      - ``PrometheusConfig()`` builds the stub: 0 and an empty series; ``http`` without a
        base URL and an unknown kind raise ``AppValueError``; ``http`` without an injected
        client builds one
    """
    stub = new_metrics_querier_from_config(PrometheusConfig())
    bogus = MagicMock(spec=PrometheusConfig)
    bogus.kind = "bogus"

    assert isinstance(stub, StubMetricsQuerier)
    assert await stub.query("q") == pytest.approx(0.0)
    assert await stub.query_range("q", _T0, _T0, timedelta(seconds=1)) == []
    with pytest.raises(AppValueError):
        _ = new_metrics_querier_from_config(PrometheusConfig(kind=PrometheusKind.HTTP))
    with pytest.raises(AppValueError):
        _ = new_metrics_querier_from_config(bogus)
    built = new_metrics_querier_from_config(
        PrometheusConfig(kind=PrometheusKind.HTTP, base_url="http://prom")
    )
    assert not isinstance(built, StubMetricsQuerier)


def test_python_and_go_method_names_match() -> None:
    """Test that ``MetricsQuerier`` has exactly the Go interface's methods, in snake case.

    **Why this test is important:**
      - The two engines are one contract; a renamed method on one side breaks the parity a
        product relies on when it moves a caller between languages.

    **What it tests:**
      - Go ``Query`` / ``QueryRange`` ↔ Python ``query`` / ``query_range``
    """
    source = _GO_INTERFACE.read_text(encoding="utf-8")
    body = re.search(r"type MetricsQuerier interface \{(.*?)\n\}", source, re.DOTALL)
    assert body is not None
    go_methods: set[str] = set(
        cast("list[str]", re.findall(r"^\t([A-Z]\w+)\(", body.group(1), re.MULTILINE))
    )
    snake = {re.sub(r"(?<!^)([A-Z])", r"_\1", name).lower() for name in go_methods}
    assert snake == set(MetricsQuerier.__abstractmethods__)
