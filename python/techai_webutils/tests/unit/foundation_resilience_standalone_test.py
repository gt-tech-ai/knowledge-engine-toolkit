"""Unit tests for the resilience series the standalone primitives record.

# Test Coverage

The tests cover:
  - The retriers (``ExponentialRetrier``, ``retry_transient``, ``retry_transient_async``):
    retries and exhaustion with no operation.
  - ``AsyncCircuitBreaker``: its state, its moves and one Warning when it opens.
  - The refusing primitives (``TokenBucketRateLimiter``, the per-key limiters built by
    ``per_key_rate_limiter_from_config``, ``SemaphoreBulkhead`` built directly or by
    ``bulkhead_from_config``): one refusal, one
    rejection with its reason.
  - ``AdaptiveThrottler``: its rejection-ratio gauge and each shed call; the
    ``DelayHedger`` from ``hedger_from_config``: each backup fired and each it won.
  - ``DeadLetterQueue``: each letter sent or failed; ``MemoryDeduplicator``: each
    duplicate.

# Test Structure

Each primitive is built over a ``Recorder`` bound to tier ``standalone`` and the name
``x`` over a real in-process ``CollectorRegistry`` (no external dependency); the op it
runs is a scripted local function.
"""

from __future__ import annotations

import asyncio
import contextlib
from typing import TYPE_CHECKING, cast, final
from unittest.mock import AsyncMock, MagicMock

import pytest
from prometheus_client import CollectorRegistry

from techai_webutils.core.errors.errors import InvalidInputError, UnavailableError
from techai_webutils.core.interfaces.dlq import DeadLetter, DeadLetterBackend
from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.foundation.metrics.metrics import new_metrics
from techai_webutils.foundation.resilience.adaptive_throttler import (
    AdaptiveThrottler,
    ThrottledError,
)
from techai_webutils.foundation.resilience.async_circuit_breaker import (
    AsyncCircuitBreaker,
)
from techai_webutils.foundation.resilience.async_retry import retry_transient_async
from techai_webutils.foundation.resilience.bulkhead import (
    BulkheadConfig,
    BulkheadFullError,
    BulkheadKind,
    SemaphoreBulkhead,
    bulkhead_from_config,
)
from techai_webutils.foundation.resilience.dedup import MemoryDeduplicator
from techai_webutils.foundation.resilience.dlq import DeadLetterQueue
from techai_webutils.foundation.resilience.hedge import (
    HedgeConfig,
    HedgeKind,
    hedger_from_config,
)
from techai_webutils.foundation.resilience.observer import Observer
from techai_webutils.foundation.resilience.per_key_builder import (
    PerKeyRateLimiterConfig,
    PerKeyRateLimiterKind,
    per_key_rate_limiter_from_config,
)
from techai_webutils.foundation.resilience.rate_limiter import TokenBucketRateLimiter
from techai_webutils.foundation.resilience.retrier import (
    ExponentialRetrier,
    ExponentialRetrierConfig,
)
from techai_webutils.foundation.resilience.retry import retry_transient
from techai_webutils.foundation.resilience.series import (
    TIER_STANDALONE,
    Reason,
    Recorder,
)

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Sequence

    from techai_webutils.core.errors.errors import AppError
    from techai_webutils.core.interfaces.bulkhead import Bulkhead

_NAME = "x"
_RETRIES = 3
_GAUGE_OPEN = 2.0
"""``resilience_breaker_state`` while the breaker is open."""


def _recorder() -> tuple[CollectorRegistry, Recorder]:
    """Return a fresh registry and a standalone recorder over it.

    Returns:
        The registry and the recorder.

    """
    registry = CollectorRegistry()
    return registry, Recorder(new_metrics(registry), TIER_STANDALONE, _NAME)


def _value(registry: CollectorRegistry, metric: str, **labels: str) -> float:
    """Return a standalone sample's value, 0 when never recorded.

    Args:
        registry: Where the series live.
        metric: The sample's name.
        **labels: The sample's own labels, after tier and name.

    Returns:
        The sample's value.

    """
    wanted = {"tier": TIER_STANDALONE, "name": _NAME, **labels}
    return registry.get_sample_value(metric, wanted) or 0.0


async def _no_sleep(_: float) -> None:
    """Wait no time between attempts.

    Args:
        _: The backoff, ignored.

    """
    await asyncio.sleep(0)


@final
class _Script:
    """Raises each scripted error in turn, then succeeds; counts its calls."""

    def __init__(self, failures: Sequence[BaseException]) -> None:
        """Bind the failures to raise, in order.

        Args:
            failures: The errors the leading calls raise.

        """
        self._failures = failures
        self.calls = 0

    def __call__(self) -> str:
        """Run one attempt.

        Returns:
            The name, once the failures are spent.

        """
        self.calls += 1
        if self.calls <= len(self._failures):
            raise self._failures[self.calls - 1]
        return _NAME


def _down(count: int) -> list[AppError]:
    """Return ``count`` transient failures.

    Args:
        count: How many.

    Returns:
        The failures.

    """
    return [UnavailableError("down") for _ in range(count)]


_CASES = {
    "first try": ([], 0.0, 0.0),
    "recovers": (_down(2), 2.0, 0.0),
    "exhausted": (_down(_RETRIES + 1), float(_RETRIES), 1.0),
    "permanent": ([InvalidInputError("bad")], 0.0, 0.0),
    "cancelled after a retry": ([*_down(1), asyncio.CancelledError()], 1.0, 0.0),
}
"""Each case's failures, then its expected retries and exhaustions."""


def _assert_counted(
    registry: CollectorRegistry, retried: float, exhausted: float
) -> None:
    """Assert the retries and exhaustions a standalone retrier recorded.

    Args:
        registry: Where the series live.
        retried: The expected retries.
        exhausted: The expected exhaustions.

    """
    assert _value(registry, "resilience_retries_total", operation="") == retried
    assert _value(registry, "resilience_retry_exhausted_total", operation="") == exhausted


class TestRetriers:
    """A retrier handed a recorder counts its retries and exhaustion."""

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        ("failures", "retried", "exhausted"), _CASES.values(), ids=_CASES.keys()
    )
    async def test_exponential_retrier_counts(
        failures: Sequence[BaseException], retried: float, exhausted: float
    ) -> None:
        """Test that ``ExponentialRetrier`` with a recorder counts each call it retried.

        Why this test is important:
          - a retrier used outside a decorate tier had no layer to count for it.

        What it tests:
          - each case's retries and exhaustions under tier standalone, operation "";
            a call its caller cancelled after a retry counts the retry, no exhaustion.
        """
        registry, recorder = _recorder()
        retrier = ExponentialRetrier(
            ExponentialRetrierConfig(max_retries=_RETRIES, initial_interval=0.0),
            sleep=_no_sleep,
            recorder=recorder,
        )
        script = _Script(failures)

        async def op() -> object:
            await asyncio.sleep(0)
            return script()

        with contextlib.suppress(
            UnavailableError, InvalidInputError, asyncio.CancelledError
        ):
            _ = await retrier.retry(op)

        _assert_counted(registry, retried, exhausted)

    @staticmethod
    @pytest.mark.parametrize(
        ("failures", "retried", "exhausted"), _CASES.values(), ids=_CASES.keys()
    )
    def test_retry_transient_counts(
        failures: Sequence[BaseException], retried: float, exhausted: float
    ) -> None:
        """Test that ``retry_transient`` with a recorder counts each call it retried.

        Why this test is important:
          - the sync decorator is the other way code retries outside a tier.

        What it tests:
          - each case's retries and exhaustions under tier standalone, operation "";
            a call its caller cancelled after a retry counts the retry, no exhaustion.
        """
        registry, recorder = _recorder()
        script = _Script(failures)
        retried_fn = retry_transient(
            max_attempts=_RETRIES + 1, base_delay=0.0, max_delay=0.0, recorder=recorder
        )(script)

        with contextlib.suppress(
            UnavailableError, InvalidInputError, asyncio.CancelledError
        ):
            _ = retried_fn()

        _assert_counted(registry, retried, exhausted)

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        ("failures", "retried", "exhausted"), _CASES.values(), ids=_CASES.keys()
    )
    async def test_retry_transient_async_counts(
        failures: Sequence[BaseException], retried: float, exhausted: float
    ) -> None:
        """Test that ``retry_transient_async`` with a recorder counts each retried call.

        Why this test is important:
          - the async decorator wraps coroutines that retry outside a tier.

        What it tests:
          - each case's retries and exhaustions under tier standalone, operation "";
            a call its caller cancelled after a retry counts the retry, no exhaustion.
        """
        registry, recorder = _recorder()
        script = _Script(failures)

        async def op() -> object:
            await asyncio.sleep(0)
            return script()

        retried_fn = retry_transient_async(
            max_attempts=_RETRIES + 1, base_delay=0.0, sleep=_no_sleep, recorder=recorder
        )(op)

        with contextlib.suppress(
            UnavailableError, InvalidInputError, asyncio.CancelledError
        ):
            _ = await retried_fn()

        _assert_counted(registry, retried, exhausted)


class TestAsyncCircuitBreaker:
    """The async breaker reports like the sync one."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_async_breaker_reports_state_moves_and_open_warning() -> None:
        """Test that an ``AsyncCircuitBreaker`` with an Observer reports each state.

        Why this test is important:
          - the async breaker guards the routed model gateway; held open it sheds every
            call, and only its gauge and Warning tell an operator so.

        What it tests:
          - a new breaker reads closed; one failure opens it (state 2, one move to open,
            one Warning naming tier standalone and the breaker); after its recovery
            timeout a success moves it to half_open then closed (state 0).
        """
        registry = CollectorRegistry()
        logger = MagicMock(spec=Logger)
        observer = Observer.of(new_metrics(registry), logger, TIER_STANDALONE, _NAME)
        breaker = AsyncCircuitBreaker(
            failure_threshold=1, recovery_timeout=0.0
        ).with_observer(observer)
        state = {"tier": TIER_STANDALONE, "name": _NAME}
        assert registry.get_sample_value("resilience_breaker_state", state) == 0

        with contextlib.suppress(UnavailableError):
            async with breaker:
                _ = _Script(_down(1))()

        assert _value(registry, "resilience_breaker_state") == _GAUGE_OPEN
        assert _value(registry, "resilience_breaker_transitions_total", to="open") == 1

        async with breaker:
            await asyncio.sleep(0)

        moves = "resilience_breaker_transitions_total"
        assert _value(registry, moves, to="half_open") == 1
        assert _value(registry, moves, to="closed") == 1
        assert _value(registry, "resilience_breaker_state") == 0
        cast("MagicMock", logger.warning).assert_called_once_with(
            "circuit breaker opened", tier=TIER_STANDALONE, name=_NAME
        )


def _redis(counts: list[int]) -> MagicMock:
    """Return a Redis client whose pipeline's ``INCR`` answers ``counts`` in turn.

    Args:
        counts: The count each call returns.

    Returns:
        The mocked client.

    """
    pipe = MagicMock()
    pipe.__aenter__ = AsyncMock(return_value=pipe)
    pipe.__aexit__ = AsyncMock(return_value=False)
    pipe.execute = AsyncMock(side_effect=[[count, True] for count in counts])
    redis = MagicMock()
    redis.pipeline = MagicMock(return_value=pipe)
    return redis


async def _token_bucket(recorder: Recorder) -> list[bool]:
    """Ask a one-token bucket twice.

    Args:
        recorder: Where the bucket reports.

    Returns:
        Each answer.

    """
    await asyncio.sleep(0)
    bucket = TokenBucketRateLimiter(rate=0.001, burst=1).with_recorder(recorder)
    return [bucket.allow(), bucket.allow()]


async def _per_key(recorder: Recorder, config: PerKeyRateLimiterConfig) -> list[bool]:
    """Ask a per-key limiter of one call per window twice for the same key.

    Args:
        recorder: Where the limiter reports.
        config: Selects the backend.

    Returns:
        Each answer.

    """
    limiter = per_key_rate_limiter_from_config(
        config, redis=_redis([1, 2]), recorder=recorder
    )
    return [await limiter.allow_key("k"), await limiter.allow_key("k")]


async def _memory(recorder: Recorder) -> list[bool]:
    """Ask the memory per-key limiter twice.

    Args:
        recorder: Where the limiter reports.

    Returns:
        Each answer.

    """
    return await _per_key(recorder, PerKeyRateLimiterConfig(max_per_window=1))


async def _shared(recorder: Recorder) -> list[bool]:
    """Ask the Redis per-key limiter twice.

    Args:
        recorder: Where the limiter reports.

    Returns:
        Each answer.

    """
    config = PerKeyRateLimiterConfig(kind=PerKeyRateLimiterKind.REDIS, max_per_window=1)
    return await _per_key(recorder, config)


async def _semaphore(recorder: Recorder) -> list[bool]:
    """Try a one-slot ``SemaphoreBulkhead`` while its slot is held.

    Args:
        recorder: Where the bulkhead reports.

    Returns:
        Whether the try ran.

    """
    return await _try_held(SemaphoreBulkhead(1).with_recorder(recorder))


async def _semaphore_from_config(recorder: Recorder) -> list[bool]:
    """Try a one-slot semaphore bulkhead built by ``bulkhead_from_config``.

    Args:
        recorder: Where the bulkhead reports.

    Returns:
        Whether the try ran.

    """
    config = BulkheadConfig(kind=BulkheadKind.SEMAPHORE, max_concurrent=1)
    return await _try_held(bulkhead_from_config(config, recorder))


async def _try_held(bulkhead: Bulkhead) -> list[bool]:
    """Try ``bulkhead`` once while another call holds its only slot.

    Args:
        bulkhead: A one-slot bulkhead.

    Returns:
        Whether the try ran.

    """
    held = asyncio.Event()
    release = asyncio.Event()

    async def hold() -> object:
        held.set()
        _ = await release.wait()
        return None

    async def idle() -> object:
        await asyncio.sleep(0)
        return None

    holder = asyncio.create_task(bulkhead.execute(hold))
    _ = await held.wait()
    ran = True
    try:
        _ = await bulkhead.try_execute(idle)
    except BulkheadFullError:
        ran = False
    release.set()
    _ = await holder
    return [ran]


_REFUSERS: dict[str, tuple[Callable[[Recorder], Awaitable[list[bool]]], Reason]] = {
    "token bucket": (_token_bucket, Reason.RATE_LIMITED),
    "memory per key": (_memory, Reason.RATE_LIMITED),
    "redis per key": (_shared, Reason.RATE_LIMITED),
    "semaphore bulkhead": (_semaphore, Reason.BULKHEAD_FULL),
    "semaphore from config": (_semaphore_from_config, Reason.BULKHEAD_FULL),
}
"""Each refusing primitive: a run that refuses once, and the reason it records."""


class TestRefusals:
    """A refusing primitive handed a recorder counts each refusal."""

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize(("run", "reason"), _REFUSERS.values(), ids=_REFUSERS.keys())
    async def test_refusal_counts_one_rejection(
        run: Callable[[Recorder], Awaitable[list[bool]]], reason: Reason
    ) -> None:
        """Test that one refusal records one rejection with the primitive's reason.

        Why this test is important:
          - a limiter or bulkhead used outside a decorate tier sheds load silently;
            the rejection count is how an operator sees it.

        What it tests:
          - exactly one ``resilience_rejections_total`` under the primitive's reason
            and operation "", and none under the other refusal reason.
        """
        registry, recorder = _recorder()

        answers = await run(recorder)

        assert False in answers
        rejections = "resilience_rejections_total"
        assert _value(registry, rejections, operation="", reason=reason) == 1
        other = (
            Reason.BULKHEAD_FULL if reason is Reason.RATE_LIMITED else Reason.RATE_LIMITED
        )
        assert _value(registry, rejections, operation="", reason=other) == 0


class TestThrottlerAndHedger:
    """The throttler reports its ratio and sheds; the hedger its backups."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_throttler_reports_ratio_and_shed_calls() -> None:
        """Test that an ``AdaptiveThrottler`` with a recorder reports what it sheds.

        Why this test is important:
          - a throttler sheds calls before they leave the process; without the
            gauge and the count an operator sees only an unexplained drop in traffic.

        What it tests:
          - one failed call raises the rejection probability and the ratio gauge
            reports it; the next call, shed without running, counts one
            ``throttled`` rejection.
        """
        registry, recorder = _recorder()
        throttler = AdaptiveThrottler(rand=lambda: 0.0).with_recorder(recorder)
        script = _Script(_down(1))

        async def op() -> str:
            await asyncio.sleep(0)
            return script()

        with contextlib.suppress(UnavailableError):
            _ = await throttler.do(op)
        ratio = _value(registry, "resilience_throttle_rejection_ratio")
        assert ratio > 0
        assert ratio == pytest.approx(throttler.rejection_probability)
        with pytest.raises(ThrottledError):
            _ = await throttler.do(op)

        assert script.calls == 1
        rejections = "resilience_rejections_total"
        assert _value(registry, rejections, operation="", reason=Reason.THROTTLED) == 1

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        ("delay", "fired"), [(0.0, 1.0), (60.0, 0.0)], ids=["backup wins", "first wins"]
    )
    async def test_hedger_counts_backups_fired_and_won(
        delay: float, fired: float
    ) -> None:
        """Test that a delay hedger with a recorder counts each backup and each win.

        Why this test is important:
          - a hedge doubles load on a slow dependency; the fired count is that
            extra load and the won count is what it bought.

        What it tests:
          - when the first attempt outlasts the delay, one backup fires and wins
            (fired 1, won 1); when it answers within the delay, neither is counted.
        """
        registry, recorder = _recorder()
        hedger = hedger_from_config(
            HedgeConfig(kind=HedgeKind.DELAY, delay_seconds=delay), recorder=recorder
        )
        stalled = asyncio.Event()
        calls = [0]

        async def op() -> str:
            calls[0] += 1
            if calls[0] == 1 and fired > 0:
                _ = await stalled.wait()
            await asyncio.sleep(0)
            return _NAME

        assert await hedger.hedge(op) == _NAME

        hedges = "resilience_hedges_total"
        assert _value(registry, hedges, outcome="fired") == fired
        assert _value(registry, hedges, outcome="won") == fired


class TestDeadLettersAndDedup:
    """The dead-letter queue counts each outcome; the deduplicator each duplicate."""

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        ("failure", "outcome"),
        [(None, "sent"), (UnavailableError("down"), "failed")],
        ids=["sent", "failed"],
    )
    async def test_dead_letter_counts_outcome(
        failure: AppError | None, outcome: str
    ) -> None:
        """Test that a ``DeadLetterQueue`` with a recorder counts each letter's outcome.

        Why this test is important:
          - a dead letter is a message the system gave up on, and a failed one is a
            message it may lose; both must show on a dashboard.

        What it tests:
          - a letter the backend takes counts ``sent``; one it refuses counts
            ``failed``, and the other outcome stays 0.
        """
        registry, recorder = _recorder()
        backend = MagicMock(spec=DeadLetterBackend)
        backend.send = AsyncMock(side_effect=failure)
        queue = DeadLetterQueue(backend).with_recorder(recorder)

        sent = await queue.send(DeadLetter(id="m", payload=b"", reason="poison"))

        assert sent is (failure is None)
        letters = "resilience_dead_letters_total"
        other = "failed" if outcome == "sent" else "sent"
        assert _value(registry, letters, outcome=outcome) == 1
        assert _value(registry, letters, outcome=other) == 0

    @staticmethod
    @pytest.mark.asyncio
    async def test_dedup_counts_each_duplicate() -> None:
        """Test that a ``MemoryDeduplicator`` with a recorder counts each duplicate.

        Why this test is important:
          - a rising duplicate count is redelivery or a replay upstream, which an
            operator otherwise cannot see.

        What it tests:
          - a key seen three times counts two duplicates; a new key counts none.
        """
        registry, recorder = _recorder()
        dedup = MemoryDeduplicator().with_recorder(recorder)

        answers = [await dedup.seen(key) for key in ("a", "a", "a", "b")]

        assert answers == [False, True, True, False]
        assert _value(registry, "resilience_duplicates_total") == len(answers) - 2
