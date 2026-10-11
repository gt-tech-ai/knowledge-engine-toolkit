"""Unit tests for the analytics ``Fact`` wire format and the ``FactPublisher`` tier."""

from __future__ import annotations

import asyncio
import json
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.clients.facts import (
    FactPublisherConfig,
    FactPublisherKind,
    new_fact_publisher_from_config,
)
from techai_webutils.clients.facts.messaging import MessagingFactPublisher
from techai_webutils.clients.facts.stub import StubFactPublisher
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.fact_publisher import FactPublisher
from techai_webutils.core.interfaces.messaging import MessagePublisher
from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
from techai_webutils.core.types.fact import Fact

if TYPE_CHECKING:
    from collections.abc import Coroutine

_GOLDEN = Path(__file__).resolve().parents[4] / "testdata" / "analytics_fact.golden.json"


def _fact(i: int = 0) -> Fact:
    """Return the golden fact (``i == 0``) or a variant with a distinct idempotency key."""
    return Fact(
        cube="genai_calls",
        org_id="org-1",
        ts=datetime(2026, 10, 9, 12, 34, 56, 789000, tzinfo=UTC),
        dims={
            "provider": "aws.bedrock",
            "model": "nova-lite",
            "step": "generate",
            "team": "t-1",
        },
        measures={"tokens_in": 42, "tokens_out": 7, "duration_s": 1.25},
        idempotency_key="4bf92f3577b34da6a3ce929d0e0e4736:00f067aa0ba902b7:generate"
        + ("" if i == 0 else f"-{i}"),
    )


def _dropped_counter() -> tuple[MagicMock, MagicMock]:
    """Return a metrics provider whose only counter (``gen_ai_fact_dropped_total``) is returned too."""
    counter = MagicMock(spec=MetricCounter)
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter.return_value = counter
    return metrics, counter


def test_fact_json_matches_golden() -> None:
    """Test that ``Fact.to_json`` is byte-identical to the shared golden fixture.

    **Why this test is important:**
      - Python producers and the Go analytics consumer share this wire format; the Go suite
        asserts the same file, so any drift in key order, number or timestamp rendering fails one
        side before it corrupts the fact lane.

    **What it tests:**
      - sorted keys, compact separators, integral measures rendered as integers, RFC 3339 UTC
        timestamp with trailing zeros trimmed (``…56.789Z``), and ``schema`` 1
    """
    assert _fact().to_json() == _GOLDEN.read_bytes()


def test_factory_builds_stub_by_default_and_rejects_unknown_kinds() -> None:
    """Test the config-selected factory: stub by default, coded errors on misconfiguration.

    **Why this test is important:**
      - The graph must boot with zero infrastructure, and a typo'd kind or a messaging kind with
        no transport must fail at construction, not drop every fact silently.

    **What it tests:**
      - the default config builds a ``StubFactPublisher`` whose ``publish`` accepts facts
      - an unknown kind, ``messaging`` without a publisher and ``messaging`` without a queue each
        raise ``AppError(INVALID_INPUT)``
    """
    stub = new_fact_publisher_from_config(FactPublisherConfig(), publisher=None)
    stub.publish([_fact()])

    assert isinstance(stub, StubFactPublisher)
    assert isinstance(stub, FactPublisher)
    for config, publisher in (
        (FactPublisherConfig(kind="kafka"), None),
        (FactPublisherConfig(kind=FactPublisherKind.MESSAGING, queue="facts"), None),
        (
            FactPublisherConfig(kind=FactPublisherKind.MESSAGING),
            MagicMock(spec=MessagePublisher),
        ),
    ):
        with pytest.raises(AppError) as caught:
            new_fact_publisher_from_config(config, publisher=publisher)
        assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_messaging_publisher_drops_and_counts_when_full() -> None:
    """Test that a full buffer drops the fact, counts it, and never blocks or raises.

    **Why this test is important:**
      - Facts are emitted on the generation hot path; a slow queue must cost a dropped fact, not
        user latency or an error.

    **What it tests:**
      - with ``max_buffer=2`` and the sender not started, three publishes return immediately and
        exactly one ``inc(reason="buffer_full")`` is recorded
      - a publish error drops the batch and counts ``inc(2, reason="publish_error")``
    """
    metrics, dropped = _dropped_counter()
    transport = MagicMock(spec=MessagePublisher)
    transport.publish_batch = AsyncMock(side_effect=RuntimeError("sqs down"))
    publisher = new_fact_publisher_from_config(
        FactPublisherConfig(
            kind=FactPublisherKind.MESSAGING,
            queue="facts",
            max_buffer=2,
            flush_interval_s=0.01,
        ),
        publisher=transport,
        metrics=metrics,
    )

    publisher.publish([_fact(1), _fact(2), _fact(3)])
    assert dropped.inc.call_args_list == [((), {"reason": "buffer_full"})]

    async with publisher:
        pass

    assert dropped.inc.call_args_list[1] == ((2,), {"reason": "publish_error"})
    metrics.counter.assert_called_once_with(
        "gen_ai_fact_dropped_total",
        "Analytics facts dropped before publish, by reason.",
        ["reason"],
    )


@pytest.mark.asyncio
async def test_messaging_publisher_closes_when_the_sender_dies_and_after_aclose() -> None:
    """Test that a dead sender task closes the publisher instead of buffering into the void.

    **Why this test is important:**
      - A sender that died silently would leave every later fact buffered until ``buffer_full``,
        hiding the real cause behind a climbing drop counter; a fact published after shutdown
        would likewise vanish uncounted.

    **What it tests:**
      - the drop counter raising inside the sender's error path kills the sender; the next
        ``publish`` of two facts counts ``inc(2, reason="closed")`` and buffers nothing
      - ``aclose`` returns normally (the sender's exception is not re-raised)
      - after ``aclose``, one more published fact counts ``inc(reason="closed")``
    """
    metrics, dropped = _dropped_counter()
    dropped.inc.side_effect = [RuntimeError("metrics down"), None, None]
    transport = MagicMock(spec=MessagePublisher)
    transport.publish_batch = AsyncMock(side_effect=RuntimeError("sqs down"))
    publisher = MessagingFactPublisher(
        transport,
        "facts",
        max_buffer=10,
        flush_interval_s=0.0,
        drain_timeout_s=0.5,
        metrics=metrics,
    )

    senders: list[asyncio.Task[None]] = []
    create_task = asyncio.create_task

    def _track_sender(coro: Coroutine[object, object, None]) -> asyncio.Task[None]:
        """Create the task as asyncio does and keep the handle whose death is under test."""
        task = create_task(coro)
        senders.append(task)
        return task

    with patch("asyncio.create_task", side_effect=_track_sender):
        async with publisher:
            publisher.publish([_fact(1)])
            [sender] = senders
            await asyncio.wait([sender])
            publisher.publish([_fact(2), _fact(3)])
            await publisher.aclose()
            publisher.publish([_fact(4)])

    assert dropped.inc.call_args_list == [
        ((), {"reason": "publish_error"}),
        ((2,), {"reason": "closed"}),
        ((), {"reason": "closed"}),
    ]
    assert transport.publish_batch.await_count == 1


@pytest.mark.asyncio
async def test_messaging_publisher_batches_by_ten() -> None:
    """Test that buffered facts are sent in batches of at most ten and fully drained on close.

    **Why this test is important:**
      - SQS ``SendMessageBatch`` takes at most ten entries; batching keeps the request count low,
        and a graceful close must not lose the facts still buffered.

    **What it tests:**
      - 25 buffered facts reach ``publish_batch("facts", …)`` as batches of 10, 10 and 5, in order,
        with each payload equal to the fact's ``to_json()``
    """
    transport = MagicMock(spec=MessagePublisher)
    transport.publish_batch = AsyncMock(return_value=None)
    publisher = new_fact_publisher_from_config(
        FactPublisherConfig(
            kind=FactPublisherKind.MESSAGING, queue="facts", flush_interval_s=0.01
        ),
        publisher=transport,
    )
    facts = [_fact(i) for i in range(1, 26)]

    publisher.publish(facts)
    async with publisher:
        pass

    sent = [c.args for c in transport.publish_batch.await_args_list]
    assert [topic for topic, _ in sent] == ["facts", "facts", "facts"]
    assert [len(batch) for _, batch in sent] == [10, 10, 5]
    assert [payload for _, batch in sent for payload in batch] == [
        f.to_json() for f in facts
    ]


def test_fact_json_escapes_like_go() -> None:
    r"""Test that ``to_json`` escapes the characters Go's ``encoding/json`` escapes by default.

    **Why this test is important:**
      - The idempotency key and dims are free-form; a ``<`` or ``&`` rendered raw in Python but
        escaped in Go would make the same fact two different byte strings.

    **What it tests:**
      - ``<``, ``>``, ``&``, U+2028 and U+2029 in a dim value are rendered as ``\\u003c``,
        ``\\u003e``, ``\\u0026``, ``\\u2028``, ``\\u2029``; other non-ASCII stays raw UTF-8
    """
    fact = Fact(
        cube="c",
        org_id="o",
        ts=datetime(2026, 10, 9, tzinfo=UTC),
        dims={"team": "a<b>&c\u2028\u2029\u00e9"},
        measures={},
        idempotency_key="k",
    )

    assert fact.to_json() == (
        b'{"cube":"c","dims":{"team":"a\\u003cb\\u003e\\u0026c\\u2028\\u2029\xc3\xa9"},'
        b'"idempotency_key":"k","measures":{},"org_id":"o","schema":1,"ts":"2026-10-09T00:00:00Z"}'
    )


def test_fact_json_renders_bool_measures_as_numbers() -> None:
    """Test that a ``bool`` measure is written as ``1`` / ``0``, never as a JSON boolean.

    **Why this test is important:**
      - The Go consumer decodes measures into ``float64``; a ``true`` there fails the whole fact,
        and Python accepts ``True`` wherever a ``float`` is typed.

    **What it tests:**
      - measures ``{"cached": True, "failed": False}`` serialize as ``{"cached":1,"failed":0}``
    """
    fact = Fact(
        cube="genai_calls",
        org_id="org-1",
        ts=datetime(2026, 10, 9, tzinfo=UTC),
        dims={},
        measures={"cached": True, "failed": False},
        idempotency_key="k",
    )

    assert json.loads(fact.to_json())["measures"] == {"cached": 1, "failed": 0}
    assert b'"measures":{"cached":1,"failed":0}' in fact.to_json()


_TEXT = st.text(max_size=12)
"""Any short string, including the characters Go escapes (``<``, ``>``, ``&``, U+2028/9)."""

_MEASURE = st.one_of(
    st.integers(min_value=-(2**53), max_value=2**53),
    st.floats(allow_nan=False, allow_infinity=False, width=64),
    st.booleans(),
)
"""Measure values the wire accepts: finite floats, exact integers, and bools (coerced to 0/1)."""


@given(
    cube=_TEXT,
    org_id=_TEXT,
    micros=st.integers(min_value=0, max_value=10**15),
    dims=st.dictionaries(_TEXT, _TEXT, max_size=4),
    measures=st.dictionaries(_TEXT, _MEASURE, max_size=4),
    key=_TEXT,
)
def test_fact_json_round_trips(
    cube: str,
    org_id: str,
    micros: int,
    dims: dict[str, str],
    measures: dict[str, float],
    key: str,
) -> None:
    """Test, over generated facts, that ``to_json`` decodes back to the fact's own values.

    **Why this test is important:**
      - The golden file pins one fact; escaping, integral-float rendering or timestamp trimming
        that breaks for other values would corrupt facts the Go consumer then rejects or misreads.

    **What it tests:**
      - the JSON decodes to exactly the fact's cube, org_id, dims, idempotency key and schema
      - every measure decodes to a number equal to the input (bools as 1/0), never a JSON bool
      - ``ts`` decodes to the same instant, in UTC with a ``Z`` suffix
      - none of ``<``, ``>``, ``&`` appears raw in the bytes (Go's HTML-safe escaping)
    """
    ts = datetime(2020, 1, 1, tzinfo=UTC) + timedelta(microseconds=micros)
    fact = Fact(
        cube=cube, org_id=org_id, ts=ts, dims=dims, measures=measures, idempotency_key=key
    )

    raw = fact.to_json()
    body = json.loads(raw)

    assert (
        body["cube"],
        body["org_id"],
        body["dims"],
        body["idempotency_key"],
        body["schema"],
    ) == (
        cube,
        org_id,
        dims,
        key,
        fact.schema,
    )
    assert body["measures"] == {k: float(v) for k, v in measures.items()}
    assert not any(isinstance(v, bool) for v in body["measures"].values())
    assert body["ts"].endswith("Z")
    assert datetime.fromisoformat(body["ts"]) == ts
    assert not any(c in raw for c in (b"<", b">", b"&"))
