"""Tests for the generic decorator bases and Unwrap seam (foundation/decorator.py)."""

from __future__ import annotations

import time
from unittest.mock import MagicMock

import pytest

from techai_webutils.core.errors.errors import AppTimeoutError, InternalError
from techai_webutils.foundation.decorator import (
    AsyncRecoveryDecoratorBase,
    AsyncTimeoutDecoratorBase,
    RecoveryDecoratorBase,
    TimeoutDecoratorBase,
    Unwrappable,
)
from techai_webutils.pipelines.base import BasePipeline
from techai_webutils.pipelines.decorators import (
    LoggingPipelineDecorator,
    MetricsPipelineDecorator,
)


class _SyncInner:
    """Minimal sync execute() double: returns a value, raises, or sleeps."""

    def __init__(
        self, *, result: object = "ok", exc: Exception | None = None, delay: float = 0.0
    ) -> None:
        self._result, self._exc, self._delay = result, exc, delay

    def execute(self, _input: object) -> object:
        if self._delay:
            time.sleep(self._delay)
        if self._exc is not None:
            raise self._exc
        return self._result


class _AsyncInner:
    """Minimal async execute() double: returns a value, raises, or sleeps."""

    def __init__(
        self, *, result: object = "ok", exc: Exception | None = None, delay: float = 0.0
    ) -> None:
        self._result, self._exc, self._delay = result, exc, delay

    async def execute(self, _input: object) -> object:
        if self._delay:
            import asyncio

            await asyncio.sleep(self._delay)
        if self._exc is not None:
            raise self._exc
        return self._result


def test_decorator_unwrap_returns_inner() -> None:
    """A decorator's unwrap() returns the exact inner it wraps, and it is Unwrappable.

    Why this test is important:
      - The Unwrap seam is the sanctioned way a test
        reaches the wrapped handler through the stack instead of touching decorator
        internals; every tier that subclasses the generic bases inherits it.

    What it tests:
      - unwrap() is the exact base instance, and isinstance(decorator, Unwrappable).
    """
    base = BasePipeline(lambda x: x)
    decorated = LoggingPipelineDecorator(base, "seam", MagicMock())

    assert decorated.unwrap() is base
    assert isinstance(decorated, Unwrappable)


def test_decorator_unwrap_walks_the_stack() -> None:
    """Walking unwrap() from the outermost decorator reaches the base in order.

    Why this test is important:
      - Proves the seam composes: a multi-decorator stack can be traversed layer by
        layer down to the underlying handler, which is what makes the seam useful.

    What it tests:
      - outer.unwrap() is the inner decorator; a second unwrap() is the base.
    """
    base = BasePipeline(lambda x: x)
    inner = MetricsPipelineDecorator(base, "seam", MagicMock())
    outer = LoggingPipelineDecorator(inner, "seam", MagicMock())

    assert outer.unwrap() is inner
    assert outer.unwrap().unwrap() is base


def test_sync_metrics_records_duration() -> None:
    """The sync metrics decorator observes an execution-duration sample.

    Why this test is important:
      - Metrics is the only sync base reached through a public class
        (MetricsPipelineDecorator); a broken observe() would silently drop every
        pipeline latency sample.

    What it tests:
      - execute() returns the inner result and records one histogram observation
        under the decorator's name.
    """
    histogram = MagicMock()
    decorated = MetricsPipelineDecorator(
        BasePipeline(lambda x: x.upper()), "m", histogram
    )

    assert decorated.execute("hi") == "HI"
    histogram.observe.assert_called_once()
    assert histogram.observe.call_args.kwargs["name"] == "m"


def test_sync_timeout_raises_apptimeout() -> None:
    """The sync timeout base raises AppTimeoutError when the inner exceeds the deadline.

    Why this test is important:
      - Timeout is the tier's backpressure valve; a slow unit must surface as a
        typed AppTimeoutError (not hang), so callers can shed rather than block.

    What it tests:
      - A 10ms deadline over a 200ms inner raises AppTimeoutError.
    """
    decorated = TimeoutDecoratorBase(_SyncInner(delay=0.2), 0.01, noun="pipeline")
    with pytest.raises(AppTimeoutError):
        decorated.execute("x")


def test_sync_recovery_normalizes_exceptions() -> None:
    """The sync recovery base re-raises AppError and wraps everything else.

    Why this test is important:
      - Recovery is the outermost guard; it must preserve typed AppErrors (so
        their codes reach the transport) yet never leak a raw exception.

    What it tests:
      - An AppError passes through unchanged; a plain exception becomes InternalError.
    """
    app_err = AppTimeoutError("boom")
    assert_passthrough = RecoveryDecoratorBase(_SyncInner(exc=app_err))
    with pytest.raises(AppTimeoutError) as caught:
        assert_passthrough.execute("x")
    assert caught.value is app_err

    wrapped = RecoveryDecoratorBase(_SyncInner(exc=ValueError("raw")))
    with pytest.raises(InternalError):
        wrapped.execute("x")


@pytest.mark.asyncio
async def test_async_timeout_raises_apptimeout() -> None:
    """The async timeout base raises AppTimeoutError when the inner exceeds the deadline.

    Why this test is important:
      - The async path is the one the pipeline/workflow builders actually compose;
        a broken timeout there would let a slow coroutine hang the whole request.

    What it tests:
      - A 10ms deadline over a 200ms coroutine raises AppTimeoutError.
    """
    decorated = AsyncTimeoutDecoratorBase(
        _AsyncInner(delay=0.2), 0.01, noun="async pipeline"
    )
    with pytest.raises(AppTimeoutError):
        await decorated.execute("x")


@pytest.mark.asyncio
async def test_async_recovery_normalizes_exceptions() -> None:
    """The async recovery base re-raises AppError and wraps everything else.

    Why this test is important:
      - Same guarantee as the sync recovery but on the async path the builders use.

    What it tests:
      - An AppError passes through; a plain exception becomes InternalError.
    """
    app_err = AppTimeoutError("boom")
    passthrough = AsyncRecoveryDecoratorBase(_AsyncInner(exc=app_err))
    with pytest.raises(AppTimeoutError) as caught:
        await passthrough.execute("x")
    assert caught.value is app_err

    wrapped = AsyncRecoveryDecoratorBase(_AsyncInner(exc=ValueError("raw")))
    with pytest.raises(InternalError):
        await wrapped.execute("x")
