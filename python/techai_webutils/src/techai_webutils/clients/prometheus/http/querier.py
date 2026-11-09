"""``HttpMetricsQuerier``: instant and range queries over a Prometheus server's HTTP API.

Mirrors Go ``clients/prometheus/http``. Both fail closed: a result that is not exactly one
finite value (or one series of them) is an error. Errors carry codes: a timeout is
``TIMEOUT``, another transport failure or a 429/502/503/504 is ``UNAVAILABLE`` (retried),
a rejected query (400, 422) is ``INVALID_INPUT``, no data is ``NOT_FOUND``, and anything
else the server says is ``UPSTREAM``.
"""

from __future__ import annotations

import math
from datetime import UTC, datetime
from typing import TYPE_CHECKING, cast, final, override

import httpx

from techai_webutils.core.errors import (
    AppError,
    AppTimeoutError,
    AppValueError,
    ErrorCode,
    NotFoundError,
    UnavailableError,
)
from techai_webutils.core.interfaces.metrics_query import MetricSample, MetricsQuerier

if TYPE_CHECKING:
    from datetime import timedelta

_TRANSIENT_STATUSES = frozenset({429, 502, 503, 504})
"""HTTP statuses a later attempt may clear."""
_REJECTED_STATUSES = frozenset({400, 422})
"""HTTP statuses of a query the server refused."""
_OK = 200
"""The only success status."""


@final
class HttpMetricsQuerier(MetricsQuerier):
    """Queries the Prometheus server.

    At ``base_url`` through an injected ``httpx.AsyncClient``.
    """

    def __init__(
        self, client: httpx.AsyncClient, base_url: str, *, max_body_bytes: int = 0
    ) -> None:
        """Send requests through ``client``.

        A body over ``max_body_bytes`` (> 0) is refused.
        """
        self._client = client
        self._base_url = base_url.rstrip("/")
        self._max_body_bytes = max_body_bytes

    @override
    async def query(self, query: str) -> float:
        """Return the single sample of an instant query.

        No sample or several series is an error.

        Returns:
            The single sample of an instant query.

        Raises:
            AppValueError: Query returned ``len(result)`` series, want one (aggregate it,
                e.g. with sum or max).
            NotFoundError: Query returned no sample (no data).

        """
        data = await self._get("/api/v1/query", {"query": query})
        result = _result(data, "vector")
        if not result:
            msg = "query returned no sample (no data)"
            raise NotFoundError(msg)
        if len(result) > 1:
            msg = (
                f"query returned {len(result)} series, want one (aggregate it, e.g. "
                f"with sum or max)"
            )
            raise AppValueError(msg)
        return _sample(cast("list[object]", result[0]["value"])).value

    @override
    async def query_range(
        self, query: str, start: datetime, end: datetime, step: timedelta
    ) -> list[MetricSample]:
        """Return the one series of a range query; no series is an empty list.

        Returns:
            The one series of a range query; no series is an empty list.

        Raises:
            AppValueError: Range query needs a positive step and end >= start (step
                ``step``, ``start``..``end``); query returned ``len(result)`` series, want
                one (aggregate it, e.g. with sum).

        """
        if step.total_seconds() <= 0 or end < start:
            msg = (
                f"range query needs a positive step and end >= start (step {step}, "
                f"{start}..{end})"
            )
            raise AppValueError(msg)
        params = {
            "query": query,
            "start": str(int(start.timestamp())),
            "end": str(int(end.timestamp())),
            "step": f"{step.total_seconds():g}",
        }
        result = _result(await self._get("/api/v1/query_range", params), "matrix")
        if len(result) > 1:
            msg = (
                f"query returned {len(result)} series, want one (aggregate it, e.g. "
                f"with sum)"
            )
            raise AppValueError(msg)
        if not result:
            return []
        return [
            _sample(cast("list[object]", pair))
            for pair in cast("list[object]", result[0]["values"])
        ]

    async def _get(self, path: str, params: dict[str, str]) -> dict[str, object]:
        """Send one query and return its ``data``.

        Raising a coded error on any failure.

        Returns:
            The response's ``data``.

        Raises:
            _status_error: The coded error for an error status.
            AppError: Prometheus query failed: ``body.get('error', '')``.
            AppTimeoutError: Prometheus query timed out.
            AppValueError: Prometheus response is ``len(response.content)`` bytes, over
                ``self._max_body_bytes``.
            UnavailableError: Query prometheus: ``exc``.

        """
        try:
            response = await self._client.get(self._base_url + path, params=params)
        except httpx.TimeoutException as exc:
            msg = "prometheus query timed out"
            raise AppTimeoutError(msg) from exc
        except httpx.HTTPError as exc:
            msg = f"query prometheus: {exc}"
            raise UnavailableError(msg) from exc
        if 0 < self._max_body_bytes < len(response.content):
            msg = (
                f"prometheus response is {len(response.content)} bytes, over "
                f"{self._max_body_bytes}"
            )
            raise AppValueError(msg)
        body = _json(response)
        if response.status_code != _OK:
            raise _status_error(response.status_code, str(body.get("error", "")))
        if body.get("status") != "success":
            msg = f"prometheus query failed: {body.get('error', '')}"
            raise AppError(ErrorCode.UPSTREAM, msg)
        return cast("dict[str, object]", body.get("data", {}))


def _json(response: httpx.Response) -> dict[str, object]:
    """Decode the response envelope; an undecodable one is an empty envelope.

    Returns:
        The response envelope; an undecodable one is an empty envelope.

    """
    try:
        body = cast("object", response.json())
    except ValueError:
        return {}
    return cast("dict[str, object]", body) if isinstance(body, dict) else {}


def _status_error(status: int, detail: str) -> AppError:
    """Map an error status to its coded error, quoting the server's message.

    Returns:
        The coded error.

    """
    msg = f"prometheus query failed (HTTP {status})" + (f": {detail}" if detail else "")
    if status in _TRANSIENT_STATUSES:
        return UnavailableError(msg)
    if status in _REJECTED_STATUSES:
        return AppValueError(msg)
    return AppError(ErrorCode.UPSTREAM, msg)


def _result(data: dict[str, object], want: str) -> list[dict[str, object]]:
    """Return ``data.result``, checking its type is ``want``.

    (``vector`` or ``matrix``).

    Returns:
        ``data.result``, checking its type is ``want``.

    Raises:
        AppValueError: Query returned a ``kind``, want a ``want``.

    """
    kind = data.get("resultType")
    if kind != want:
        msg = f"query returned a {kind!r}, want a {want}"
        raise AppValueError(msg)
    return cast("list[dict[str, object]]", data.get("result", []))


def _sample(pair: list[object]) -> MetricSample:
    """Decode one ``[<unix time>, "<value>"]`` pair; NaN or infinity is no data.

    Returns:
        One ``[<unix time>, "<value>"]`` pair; NaN or infinity is no data.

    Raises:
        AppError: Unparseable prometheus sample ``pair``.
        NotFoundError: Query returned ``pair[1]`` (no data).

    """
    try:
        when = float(cast("float", pair[0]))
        value = float(cast("str", pair[1]))
    except (IndexError, TypeError, ValueError) as exc:
        msg = f"unparseable prometheus sample {pair!r}"
        raise AppError(ErrorCode.UPSTREAM, msg) from exc
    if math.isnan(value) or math.isinf(value):
        msg = f"query returned {pair[1]} (no data)"
        raise NotFoundError(msg)
    return MetricSample(time=datetime.fromtimestamp(when, UTC), value=value)
