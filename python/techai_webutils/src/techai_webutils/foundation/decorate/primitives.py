"""The primitives the decoration builder makes for a component's resilience layers.

``build`` (``decorations``) turns each switched-on layer's tuning into its primitive
here: the exponential retrier, the circuit breaker (exporting its state), the bulkhead
and the token-bucket limiter. A bad tuning names the component and the layer.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.resilience.bulkhead import (
    BulkheadConfig,
    BulkheadKind,
    bulkhead_from_config,
)
from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitBreaker,
    CircuitState,
)
from techai_webutils.foundation.resilience.classify import is_transient
from techai_webutils.foundation.resilience.rate_limiter import TokenBucketRateLimiter
from techai_webutils.foundation.resilience.retrier import (
    ExponentialRetrier,
    ExponentialRetrierConfig,
)

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.core.interfaces.bulkhead import Bulkhead
    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter
    from techai_webutils.core.interfaces.retrier import Retrier
    from techai_webutils.foundation.config import decoration
    from techai_webutils.foundation.resilience.observer import Observer
    from techai_webutils.foundation.resilience.series import Recorder


def layer_primitive[C](
    name: str, layer: str, *, enabled: bool, make: Callable[[], C]
) -> C | None:
    """Build a layer's primitive when it is on.

    A bad tuning names the component and the layer.

    Args:
        name: The component's dotted name.
        layer: The layer.
        enabled: Whether it is on.
        make: Builds the primitive.

    Returns:
        A layer's primitive when it is on.

    Raises:
        AppValueError: Decoration: ``name``.``layer``: ``exc``.

    """
    if not enabled:
        return None
    try:
        return make()
    except AppValueError as exc:
        msg = f"decoration: {name}.{layer}: {exc}"
        raise AppValueError(msg) from exc


def retrier_for(r: decoration.Retry) -> Retrier:
    """Build the exponential-backoff retrier ``r`` tunes.

    Args:
        r: The retry settings.

    Returns:
        The exponential-backoff retrier ``r`` tunes.

    """
    r.validate()
    return ExponentialRetrier(
        ExponentialRetrierConfig(
            max_retries=r.max_retries,
            initial_interval=r.initial_interval,
            max_interval=r.max_interval,
            multiplier=r.multiplier,
            max_elapsed_time=r.max_elapsed_time,
        )
    )


_BREAKER_STATE_VALUES = {
    CircuitState.CLOSED: 0.0,
    CircuitState.HALF_OPEN: 1.0,
    CircuitState.OPEN: 2.0,
}
"""``circuit_breaker_state``'s value per state."""


def _export_transitions(
    metrics: MetricsProvider, name: str
) -> Callable[[CircuitState, CircuitState], None]:
    """Return a transition callback for ``name``'s breaker.

    It exports the breaker's state and counts its moves.

    ``circuit_breaker_state{dependency}`` reads 0 closed, 1 half-open and 2 open, starting
    at 0; ``circuit_breaker_transitions_total{dependency, to}`` counts each state entered.

    Deprecated: ``circuit_breaker_*`` is superseded by ``resilience_breaker_state`` and
    ``resilience_breaker_transitions_total`` and is removed after this release.

    Args:
        metrics: Where the transition counter is created.
        name: The component's dotted name.

    Returns:
        A transition callback that exports ``name``'s breaker state.

    """
    state = metrics.gauge(
        "circuit_breaker_state",
        "A dependency's circuit breaker state (0 closed, 1 half-open, 2 open).",
        ["dependency"],
    )
    moves = metrics.counter(
        "circuit_breaker_transitions_total",
        "A dependency's circuit breaker state changes, by the state entered.",
        ["dependency", "to"],
    )
    state.set(0.0, dependency=name)

    def on_transition(_: CircuitState, to: CircuitState) -> None:
        state.set(_BREAKER_STATE_VALUES[to], dependency=name)
        moves.inc(dependency=name, to=to.value)

    return on_transition


def breaker_for(
    b: decoration.CircuitBreaker, metrics: MetricsProvider | None, observer: Observer
) -> CircuitBreakerInterface:
    """Build the breaker ``b`` tunes for the subject ``observer`` names.

    Only a transient failure counts against it; ``max_requests`` bounds the half-open
    trial calls. It reports ``resilience_breaker_*`` and its open Warning through
    ``observer``; with ``metrics`` (the metrics layer on) it also exports the deprecated
    ``circuit_breaker_*`` series (``_export_transitions``) for one more release.

    Args:
        b: The breaker settings.
        metrics: The component's provider when its metrics layer is on, else ``None``.
        observer: Where the breaker reports.

    Returns:
        The breaker ``b`` tunes.

    """
    b.validate()
    return CircuitBreaker(
        failure_threshold=max(b.consecutive_failures, 1),
        recovery_timeout=b.timeout,
        is_failure=is_transient,
        half_open_max_calls=b.max_requests,
        on_transition=(
            None if metrics is None else _export_transitions(metrics, observer.name)
        ),
    ).with_observer(observer)


_BULKHEAD_KINDS: dict[str, BulkheadKind] = {
    "": BulkheadKind.SEMAPHORE,
    "channel": BulkheadKind.SEMAPHORE,
    "semaphore": BulkheadKind.SEMAPHORE,
    "adaptive": BulkheadKind.ADAPTIVE,
}
"""Maps the config's bulkhead kind (Go's "channel" is the fixed limit) to a backend."""


def bulkhead_for(b: decoration.Bulkhead, recorder: Recorder) -> Bulkhead:
    """Build the bulkhead ``b`` tunes; an adaptive one reports its limit to ``recorder``.

    Args:
        b: The bulkhead settings.
        recorder: Where an adaptive bulkhead reports its limit.

    Returns:
        The bulkhead ``b`` tunes.

    Raises:
        AppValueError: Unknown bulkhead kind ``b.kind`` (want channel|adaptive).

    """
    b.validate()
    kind = _BULKHEAD_KINDS.get(b.kind)
    if kind is None:
        msg = f"unknown bulkhead kind {b.kind!r} (want channel|adaptive)"
        raise AppValueError(msg)
    return bulkhead_from_config(
        BulkheadConfig(
            kind=kind,
            max_concurrent=b.max_concurrent,
            min_concurrent=b.min_concurrent,
            initial_concurrent=b.initial_concurrent,
            rtt_threshold_seconds=b.rtt_threshold,
            backoff_ratio=b.backoff_ratio,
        ),
        recorder,
    )


def rate_limiter_for(r: decoration.RateLimit) -> RateLimiter:
    """Build the token-bucket limiter ``r`` tunes.

    Args:
        r: The rate-limit settings.

    Returns:
        The token-bucket limiter ``r`` tunes.

    """
    r.validate()
    return TokenBucketRateLimiter(rate=r.rate, burst=r.burst)
