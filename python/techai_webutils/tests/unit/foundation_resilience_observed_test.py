"""Unit tests for the resilience series the decorate layers record.

# Test Coverage

The tests cover:
  - The retry layer: retries, exhaustion (with one Warning) and budget refusals.
  - The timeout layer: only a deadline the layer itself set.
  - The circuit-breaker, shedding-bulkhead and rate-limit layers: their refusals.
  - The circuit breaker itself (``with_observer``): its state, its moves and its
    open Warning.
  - The adaptive bulkhead's limit, and ``build`` handing both their Observer by the
    metrics and logging switches.

# Test Structure

Each layer is built with an ``Observer`` over a real in-process ``CollectorRegistry``
(no external dependency) and a mock ``Logger``; its primitive is a mock of the core
interface it consumes, so each case drives the layer's outcome directly.
"""

from __future__ import annotations

import asyncio
import contextlib
from dataclasses import dataclass
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest
from prometheus_client import CollectorRegistry

from techai_webutils.core.errors.errors import (
    AppError,
    AppTimeoutError,
    InvalidInputError,
    UnavailableError,
)
from techai_webutils.core.interfaces.bulkhead import Bulkhead
from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.core.interfaces.rate_limiter import RateLimiter
from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import (
    Collaborators,
    Deps,
    Observability,
    build,
    exec_op,
    new_bulkhead,
    new_circuit_breaker,
    new_rate_limit,
    new_retry,
    new_timeout,
)
from techai_webutils.foundation.metrics.metrics import new_metrics
from techai_webutils.foundation.resilience.budget import RetryBudgetExhaustedError
from techai_webutils.foundation.resilience.bulkhead import (
    AdaptiveBulkhead,
    BulkheadFullError,
)
from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitBreaker,
    CircuitOpenError,
    CircuitState,
)
from techai_webutils.foundation.resilience.observer import Observer

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

_TIER = "client"
_NAME = "x"
_OP = "Get"
_DEADLINE = 0.02
_SLOW = 1.0


@dataclass(frozen=True, slots=True)
class _Harness:
    """One layer test's registry, the Observer recording into it and its mock logger."""

    registry: CollectorRegistry
    """The in-process registry the recorder writes to."""

    logger: Logger
    """The mock logger receiving Warning lines."""

    observer: Observer
    """The Observer the layer under test reports through."""

    def value(self, metric: str, **labels: str) -> float:
        """Return a sample under the harness's tier and name, 0 when never recorded.

        Args:
            metric: The sample's name.
            **labels: The sample's own labels, after tier and name.

        Returns:
            The sample's value, or 0 when the series does not exist.

        """
        wanted = {"tier": _TIER, "name": _NAME, **labels}
        return self.registry.get_sample_value(metric, wanted) or 0.0


def _harness() -> _Harness:
    """Return an Observer for the client-tier subject ``x`` over a fresh registry.

    Returns:
        The harness.

    """
    registry = CollectorRegistry()
    logger = MagicMock(spec=Logger)
    observer = Observer.of(new_metrics(registry), logger, _TIER, _NAME)
    return _Harness(registry, logger, observer)


def _warnings(logger: Logger, msg: str) -> list[dict[str, object]]:
    """Return the fields of each Warning line ``logger`` got with message ``msg``.

    Args:
        logger: The mock logger.
        msg: The message.

    Returns:
        Each such line's keyword fields, in order.

    """
    warning = cast("MagicMock", logger.warning)
    return [dict(call.kwargs) for call in warning.call_args_list if call.args == (msg,)]


def _unavailable() -> UnavailableError:
    """Return a transient failure.

    Returns:
        A transient failure.

    """
    return UnavailableError("down")


def _scripted_retrier(
    results: list[BaseException | None], refuse_at: int | None = None
) -> MagicMock:
    """Return a mock Retrier that runs the op once per result, the way the real one does.

    It stops at the first success or permanent error, or raises the budget refusal
    before the retry after ``refuse_at`` attempts.

    Args:
        results: What each attempt raises (``None`` succeeds).
        refuse_at: The attempt count after which the budget refuses, or ``None``.

    Returns:
        The mock Retrier.

    """

    async def retry(op: Callable[[], Awaitable[object]]) -> object:
        error: BaseException | None = None
        for attempt in range(len(results)):
            if attempt and attempt == refuse_at:
                raise RetryBudgetExhaustedError
            try:
                return await op()
            except UnavailableError as exc:
                error = exc
        assert error is not None
        raise error

    retrier = MagicMock(spec=Retrier)
    retrier.retry = AsyncMock(side_effect=retry)
    return retrier


def _scripted_op(results: list[BaseException | None]) -> Callable[[], Awaitable[object]]:
    """Return an op that raises each result in turn (``None`` succeeds).

    Args:
        results: What each call raises.

    Returns:
        The op.

    """
    calls = iter(results)

    async def op() -> object:
        await asyncio.sleep(0)
        error = next(calls)
        if error is not None:
            raise error
        return _OP

    return op


@dataclass(frozen=True, slots=True)
class _RetryCase:
    """One retry-layer case: each attempt's result and what the layer must record."""

    results: list[BaseException | None]
    """What each attempt raises."""

    retried: float = 0.0
    """The expected ``resilience_retries_total``."""

    exhausted: float = 0.0
    """The expected ``resilience_retry_exhausted_total``."""

    refused: float = 0.0
    """The expected ``budget_exhausted`` rejections."""

    refuse_at: int | None = None
    """The attempt count after which the budget refuses."""


_RETRY_CASES = {
    "success": _RetryCase([None]),
    "recovers": _RetryCase([_unavailable(), _unavailable(), None], retried=2),
    "exhausted": _RetryCase([_unavailable()] * 3, retried=2, exhausted=1),
    "permanent": _RetryCase([InvalidInputError("bad")]),
    "refused first retry": _RetryCase([_unavailable()] * 2, refused=1, refuse_at=1),
    "refused after one retry": _RetryCase(
        [_unavailable()] * 3, retried=1, refused=1, refuse_at=2
    ),
    "caller cancelled after a retry": _RetryCase(
        [_unavailable(), asyncio.CancelledError()], retried=1
    ),
}
"""The retry layer's table (the Go ``retryCases`` twin)."""


class TestObserverWarn:
    """An Observer's Warning line always names its own tier and subject."""

    @staticmethod
    def test_warn_keeps_its_subject_over_caller_fields() -> None:
        """Test that ``warn`` names its tier and name even when the fields reuse them.

        Why this test is important:
          - a caller passing ``name`` or ``tier`` among its fields made ``warn`` raise
            ``TypeError`` from inside a breaker callback or a retry layer.

        What it tests:
          - ``warn`` with fields ``tier``, ``name`` and ``attempts`` writes one line
            carrying the Observer's tier and name and the caller's ``attempts``.
        """
        harness = _harness()

        harness.observer.warn("m", tier="other", name="other", attempts=2)

        assert _warnings(harness.logger, "m") == [
            {"tier": _TIER, "name": _NAME, "attempts": 2}
        ]


class TestRetryLayer:
    """The retry layer counts retries, exhaustion and budget refusals."""

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize("case", _RETRY_CASES.values(), ids=_RETRY_CASES.keys())
    async def test_retry_layer_records_retries_and_exhaustion(case: _RetryCase) -> None:
        """Test that the retry layer counts one operation's retries and outcome once.

        Why this test is important:
          - retries were invisible; and an operation counted as both exhausted and
            refused would double every alert on it.

        What it tests:
          - each row's retries, exhaustion and budget rejections under the operation;
            one "retries exhausted" Warning exactly when the row is exhausted; a call
            its caller cancelled after a retry counts the retry and no exhaustion.
        """
        harness = _harness()
        layer = new_retry(
            _scripted_retrier(case.results, case.refuse_at), harness.observer
        )

        with contextlib.suppress(
            AppError, RetryBudgetExhaustedError, asyncio.CancelledError
        ):
            _ = await exec_op(layer, _OP, _scripted_op(case.results))

        assert harness.value("resilience_retries_total", operation=_OP) == case.retried
        exhausted = harness.value("resilience_retry_exhausted_total", operation=_OP)
        assert exhausted == case.exhausted
        refused = harness.value(
            "resilience_rejections_total", operation=_OP, reason="budget_exhausted"
        )
        assert refused == case.refused
        warned = _warnings(harness.logger, "retries exhausted")
        assert len(warned) == case.exhausted
        if warned:
            assert warned[0] == {
                "tier": _TIER,
                "name": _NAME,
                "operation": _OP,
                "attempts": len(case.results),
            }


async def _waits() -> object:
    """Sleep past any deadline the tests set.

    Returns:
        Never, in practice.

    """
    await asyncio.sleep(_SLOW)
    return _OP


async def _fast() -> object:
    """Return at once.

    Returns:
        The operation's name.

    """
    await asyncio.sleep(0)
    return _OP


async def _inner_timeout() -> object:
    """Raise a deeper layer's coded timeout at once.

    Raises:
        AppTimeoutError: Always.

    """
    await asyncio.sleep(0)
    msg = "inner"
    raise AppTimeoutError(msg)


async def _builtin_timeout() -> object:
    """Raise a builtin TimeoutError before any deadline (a socket timeout, say).

    Raises:
        TimeoutError: Always.

    """
    await asyncio.sleep(0)
    raise TimeoutError


async def _builtin_timeout_late() -> object:
    """Hold the loop past the deadline, then raise a builtin TimeoutError itself.

    A CPU-bound spin (no await) keeps the layer's timer from firing, so the op's own
    TimeoutError arrives first, after the deadline's clock time.

    Raises:
        TimeoutError: Always.

    """
    await asyncio.sleep(0)
    loop = asyncio.get_running_loop()
    end = loop.time() + _DEADLINE * 2
    while loop.time() < end:
        pass
    raise TimeoutError


async def _late_error() -> object:
    """Ignore the cancellation the deadline sends and raise an unrelated error.

    Returns:
        The operation's name, were it never cancelled.

    Raises:
        ValueError: When cancelled.

    """
    try:
        await asyncio.sleep(_SLOW)
    except asyncio.CancelledError:
        msg = "unrelated"
        raise ValueError(msg) from None
    return _OP


class TestTimeoutLayer:
    """The timeout layer counts only a deadline it set."""

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        ("op", "seconds", "want"),
        [
            (_waits, _DEADLINE, 1.0),
            (_fast, _DEADLINE, 0.0),
            (_inner_timeout, _SLOW, 0.0),
            (_builtin_timeout, _SLOW, 0.0),
            (_builtin_timeout_late, _DEADLINE, 0.0),
            (_late_error, _DEADLINE, 0.0),
        ],
        ids=["cut", "fast", "inner coded", "builtin early", "builtin late", "late error"],
    )
    async def test_timeout_layer_records_its_own_deadline_only(
        op: Callable[[], Awaitable[object]], seconds: float, want: float
    ) -> None:
        """Test that the timeout layer counts an operation its own deadline cut.

        Why this test is important:
          - a timeout counted for a deeper layer's deadline, or for an error that merely
            arrived late, blames the dependency for time it did not spend.

        What it tests:
          - an op slower than the deadline counts 1; a fast op, a deeper layer's coded
            timeout, a builtin TimeoutError the op raised itself (before the deadline,
            or after it but before the layer's timer fired) and an unrelated error
            raised on cancellation count 0.
        """
        harness = _harness()

        with contextlib.suppress(AppError, TimeoutError, ValueError):
            _ = await exec_op(new_timeout(seconds, harness.observer), _OP, op)

        assert harness.value("resilience_timeouts_total", operation=_OP) == want


class TestRefusingLayers:
    """The breaker, shedding-bulkhead and rate-limit layers count what they refuse."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_breaker_layer_records_refusals_only() -> None:
        """Test that the breaker layer counts a call the breaker refused.

        Why this test is important:
          - a refused call and a dependency outage are both UNAVAILABLE; only the
            reason label tells them apart.

        What it tests:
          - an open breaker counts one ``breaker_open`` rejection; the dependency's own
            UnavailableError counts none.
        """
        harness = _harness()
        breaker = MagicMock(spec=CircuitBreakerInterface)
        cast("MagicMock", breaker.__enter__).side_effect = [
            CircuitOpenError("x"),
            breaker,
        ]
        cast("MagicMock", breaker.__exit__).return_value = False
        layer = new_circuit_breaker(breaker, _NAME, harness.observer)

        with pytest.raises(UnavailableError):
            _ = await exec_op(layer, _OP, _fast)
        with pytest.raises(UnavailableError):
            _ = await exec_op(layer, _OP, _scripted_op([_unavailable()]))

        refused = harness.value(
            "resilience_rejections_total", operation=_OP, reason="breaker_open"
        )
        assert refused == 1

    @staticmethod
    @pytest.mark.asyncio
    async def test_shedding_bulkhead_records_shed_calls() -> None:
        """Test that a shedding bulkhead layer counts a call it shed.

        Why this test is important:
          - a shed call and a rate-limited one are both RESOURCE_EXHAUSTED; the reason
            label is how an operator tells overload from a limit.

        What it tests:
          - a full bulkhead in shed mode counts one ``bulkhead_full`` rejection; the
            waiting layer counts none.
        """
        harness = _harness()
        bulkhead = MagicMock(spec=Bulkhead)
        bulkhead.try_execute = AsyncMock(side_effect=BulkheadFullError("full"))
        bulkhead.execute = AsyncMock(return_value=_OP)

        with pytest.raises(AppError):
            _ = await exec_op(
                new_bulkhead(bulkhead, shed=True, observer=harness.observer), _OP, _fast
            )
        _ = await exec_op(
            new_bulkhead(bulkhead, shed=False, observer=harness.observer), _OP, _fast
        )

        shed = harness.value(
            "resilience_rejections_total", operation=_OP, reason="bulkhead_full"
        )
        assert shed == 1

    @staticmethod
    @pytest.mark.asyncio
    async def test_rate_limit_layer_records_refusals() -> None:
        """Test that the rate-limit layer counts an operation it refused.

        Why this test is important:
          - a limited call never reaches the dependency; without the count it looks
            like the dependency failed.

        What it tests:
          - ``allow()`` false counts one ``rate_limited`` rejection and does not run the
            op; true counts none.
        """
        harness = _harness()
        limiter = MagicMock(spec=RateLimiter)
        cast("MagicMock", limiter.allow).side_effect = [False, True]
        runs: list[str] = []

        async def op() -> object:
            await asyncio.sleep(0)
            runs.append(_OP)
            return _OP

        layer = new_rate_limit(limiter, harness.observer)

        with pytest.raises(AppError):
            _ = await exec_op(layer, _OP, op)
        _ = await exec_op(layer, _OP, op)

        assert runs == [_OP]
        limited = harness.value(
            "resilience_rejections_total", operation=_OP, reason="rate_limited"
        )
        assert limited == 1


def _trip(breaker: CircuitBreaker) -> None:
    """Fail one call through ``breaker``.

    Args:
        breaker: The breaker to fail through.

    Raises:
        UnavailableError: Inside the breaker; suppressed once the breaker counts it.

    """
    with contextlib.suppress(UnavailableError), breaker:
        msg = "down"
        raise UnavailableError(msg)


class TestCircuitBreakerReports:
    """A circuit breaker reports its state, its moves and one Warning when it opens."""

    @staticmethod
    def test_breaker_reports_state_moves_and_open_warning() -> None:
        """Test that a breaker with an Observer reports each state it enters.

        Why this test is important:
          - a breaker held open sheds every call to its dependency; the state gauge and
            the Warning are how an operator learns it opened and whether it recovered.

        What it tests:
          - a new breaker reads closed; one failure opens it (state 2, one move to open,
            one Warning naming tier and name); after its recovery timeout a success
            moves it to half_open then closed (state 0), with no further Warning.
        """
        harness = _harness()
        breaker = CircuitBreaker(failure_threshold=1, recovery_timeout=0.0).with_observer(
            harness.observer
        )
        assert harness.registry.get_sample_value(
            "resilience_breaker_state", {"tier": _TIER, "name": _NAME}
        ) == pytest.approx(0)

        _trip(breaker)

        assert harness.value("resilience_breaker_state") == pytest.approx(2)
        assert harness.value("resilience_breaker_transitions_total", to="open") == 1

        with breaker:
            pass

        assert harness.value("resilience_breaker_transitions_total", to="half_open") == 1
        assert harness.value("resilience_breaker_transitions_total", to="closed") == 1
        assert harness.value("resilience_breaker_state") == pytest.approx(0)
        assert _warnings(harness.logger, "circuit breaker opened") == [
            {"tier": _TIER, "name": _NAME}
        ]

    @staticmethod
    def test_breaker_composes_with_callers_transition_callback() -> None:
        """Test that the Observer's reporting and a caller's own callback both run.

        Why this test is important:
          - the decorate builder passes the deprecated ``circuit_breaker_*`` export as
            the breaker's own callback; reporting must not replace it.

        What it tests:
          - one trip calls the caller's callback with (closed, open) and still counts the
            move to open.
        """
        harness = _harness()
        moves: list[tuple[CircuitState, CircuitState]] = []
        breaker = CircuitBreaker(
            failure_threshold=1,
            on_transition=lambda frm, to: moves.append((frm, to)),
        ).with_observer(harness.observer)

        _trip(breaker)

        assert moves == [(CircuitState.CLOSED, CircuitState.OPEN)]
        assert harness.value("resilience_breaker_transitions_total", to="open") == 1


class TestAdaptiveBulkheadReports:
    """An adaptive bulkhead reports its limit."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_adaptive_bulkhead_reports_limit() -> None:
        """Test that the adaptive bulkhead reports its limit when built and adjusted.

        Why this test is important:
          - the adaptive limit is the bulkhead's verdict on its dependency's health: a
            limit pinned at its floor means the dependency is slow or failing.

        What it tests:
          - a new bulkhead reports its initial limit 5; a failed call shrinks it to the
            4 it admits (5 times 0.9); a fast success grows it back to 5.
        """
        harness = _harness()
        bulkhead = AdaptiveBulkhead(
            min_concurrent=1,
            max_concurrent=10,
            initial_concurrent=5,
            rtt_threshold_seconds=0.0,
            backoff_ratio=0.9,
        ).with_recorder(harness.observer.recorder)
        assert harness.value("resilience_bulkhead_limit") == pytest.approx(5)

        with pytest.raises(UnavailableError):
            _ = await bulkhead.try_execute(_scripted_op([_unavailable()]))
        assert harness.value("resilience_bulkhead_limit") == pytest.approx(4)

        _ = await bulkhead.try_execute(_fast)
        assert harness.value("resilience_bulkhead_limit") == pytest.approx(5)


def _built(harness: _Harness, *, switched: bool) -> Deps:
    """Build the client-tier subject ``x``: a one-failure breaker, an adaptive bulkhead.

    Its metrics and logging layers are set to ``switched``.

    Args:
        harness: Supplies the registry and the logger.
        switched: Whether the metrics and logging layers are on.

    Returns:
        The built Deps.

    """
    layers = decoration.Layers(
        metrics=switched,
        logging=switched,
        circuit_breaker=decoration.CircuitBreaker(enabled=True, consecutive_failures=1),
        bulkhead=decoration.Bulkhead(
            enabled=True,
            kind="adaptive",
            max_concurrent=4,
            min_concurrent=1,
            initial_concurrent=2,
            backoff_ratio=0.9,
        ),
    )
    collab = Collaborators(
        observability=Observability(
            logger=harness.logger, metrics=new_metrics(harness.registry)
        )
    )
    return build(_TIER, _NAME, layers, collab)


class TestBuildWiring:
    """``build`` hands the breaker and the adaptive bulkhead their Observer."""

    @staticmethod
    @pytest.mark.parametrize("switched", [True, False], ids=["on", "off"])
    def test_build_reports_by_layer_switches(*, switched: bool) -> None:
        """Test that a built breaker and bulkhead report only when their layers are on.

        Why this test is important:
          - the switches are how a deployment turns observability off for one component;
            a breaker that reported regardless would export series the config said not
            to, and one never handed its Observer would leave its state invisible.

        What it tests:
          - on: one trip reads open in both ``resilience_breaker_state`` and the
            deprecated ``circuit_breaker_state``, counts one move to open in each, warns
            once, and the adaptive bulkhead reports its initial limit 2.
          - off: the same records nothing and logs nothing.
        """
        harness = _harness()
        deps = _built(harness, switched=switched)
        assert deps.breaker is not None

        _trip(cast("CircuitBreaker", deps.breaker))

        want = 2.0 if switched else 0.0
        assert harness.value("resilience_breaker_state") == pytest.approx(want)
        legacy = harness.registry.get_sample_value(
            "circuit_breaker_state", {"dependency": _NAME}
        )
        assert (legacy or 0.0) == pytest.approx(want)
        moved = harness.value("resilience_breaker_transitions_total", to="open")
        assert moved == want / 2
        assert harness.value("resilience_bulkhead_limit") == pytest.approx(want)
        opened = _warnings(harness.logger, "circuit breaker opened")
        assert opened == ([{"tier": _TIER, "name": _NAME}] if switched else [])

    @staticmethod
    @pytest.mark.asyncio
    async def test_build_gives_channel_bulkhead_no_recorder() -> None:
        """Test that a built channel bulkhead does not count its own refusals.

        Why this test is important:
          - the bulkhead layer counts each refusal; a channel bulkhead that also had a
            recorder would count it again, doubling the rate an operator alerts on.

        What it tests:
          - with metrics on, the built one-slot channel bulkhead refuses a try while its
            slot is held and records no rejection itself.
        """
        harness = _harness()
        layers = decoration.Layers(
            metrics=True,
            bulkhead=decoration.Bulkhead(
                enabled=True,
                kind="channel",
                max_concurrent=1,
                min_concurrent=1,
                initial_concurrent=1,
                backoff_ratio=0.9,
            ),
        )
        collab = Collaborators(
            observability=Observability(metrics=new_metrics(harness.registry))
        )
        bulkhead = build(_TIER, _NAME, layers, collab).bulkhead
        assert bulkhead is not None
        held, release = asyncio.Event(), asyncio.Event()

        async def hold() -> object:
            held.set()
            return await release.wait()

        holder = asyncio.create_task(bulkhead.execute(hold))
        _ = await held.wait()
        with pytest.raises(BulkheadFullError):
            _ = await bulkhead.try_execute(hold)
        release.set()
        _ = await holder

        rejections = "resilience_rejections_total"
        assert harness.value(rejections, operation="", reason="bulkhead_full") == 0
