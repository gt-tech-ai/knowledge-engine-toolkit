"""Builds every component's ``Deps`` from the decoration config (mirrors Go ``decorations.go``).

The composition root makes one ``Decorations`` from the ``decoration`` config section and the
process collaborators, and hands it to each tier; a tier asks it for a component's ``Deps``
under the component's name.
"""

from __future__ import annotations

import dataclasses
from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.config.decoration import on
from techai_webutils.foundation.decorate.tier import Deps, Observability
from techai_webutils.foundation.resilience.bulkhead import (
    BulkheadConfig,
    BulkheadKind,
    bulkhead_from_config,
)
from techai_webutils.foundation.resilience.circuit_breaker import CircuitBreaker
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
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter
    from techai_webutils.core.interfaces.retrier import Retrier
    from techai_webutils.foundation.config import decoration
    from techai_webutils.foundation.decorate.middleware import Authorizer
    from techai_webutils.foundation.decorate.tier import Tier


@dataclass(frozen=True, slots=True)
class Collaborators:
    """The process collaborators the decoration layers draw on."""

    observability: Observability = dataclasses.field(default_factory=Observability)
    """Backs the tracing, metrics and logging layers."""

    auth: Authorizer | None = None
    """Backs the auth layer: it authorizes an operation by its ``<name>.<op>`` action."""


class Decorations:
    """Builds every component's Deps from the decoration config.

    ``Decorations()`` (no config) decorates nothing: every component gets empty Deps, so only
    recovery runs.
    """

    __slots__ = ("_collab", "_config")

    def __init__(
        self,
        config: decoration.DecorationConfig | None = None,
        collaborators: Collaborators | None = None,
    ) -> None:
        """Bind the config (None decorates nothing) and the collaborators."""
        self._config = config
        self._collab = collaborators or Collaborators()

    @property
    def observability(self) -> Observability:
        """The collaborators' observability, for a component that logs or meters outside its layers."""
        return self._collab.observability

    def deps(self, tier: Tier, name: str) -> Deps:
        """Return the Deps of the component ``name`` in ``tier``: defaults with its override on top.

        A tier the config does not know, or a layer whose tuning is invalid, raises
        ``AppValueError``.
        """
        if self._config is None:
            return Deps()
        tc = self._config.tier(tier.name)
        if tc is None:
            msg = f"decoration: no config for tier {tier.name!r}"
            raise AppValueError(msg)
        return build(name, tc.layers_for(name), self._collab)


def build(name: str, layers: decoration.Layers, collab: Collaborators) -> Deps:
    """Return the Deps of the component ``name`` from its resolved ``layers``.

    Each layer that is on gets its collaborator from ``collab``, or its primitive built from its
    tuning (the breaker guards ``name``). An invalid tuning raises ``AppValueError``.
    """
    obs = collab.observability
    return Deps(
        observability=Observability(
            logger=obs.logger if on(layers.logging) else None,
            metrics=obs.metrics if on(layers.metrics) else None,
            tracer=obs.tracer if on(layers.tracing) else None,
        ),
        auth=collab.auth if on(layers.auth) else None,
        timeout=layers.timeout or 0.0,
        skip_recovery=not on(layers.recovery),
        panic_logger=obs.logger if on(layers.recovery) else None,
        retrier=_layer(
            name, "retry", on(layers.retry.enabled), lambda: _retrier(layers.retry)
        ),
        breaker=_layer(
            name,
            "circuit_breaker",
            on(layers.circuit_breaker.enabled),
            lambda: _breaker(layers.circuit_breaker),
        ),
        bulkhead=_layer(
            name,
            "bulkhead",
            on(layers.bulkhead.enabled),
            lambda: _bulkhead(layers.bulkhead),
        ),
        shed_load=on(layers.bulkhead.enabled) and on(layers.bulkhead.shed),
        rate_limiter=_layer(
            name,
            "rate_limit",
            on(layers.rate_limit.enabled),
            lambda: _rate_limiter(layers.rate_limit),
        ),
    )


def _layer[C](name: str, layer: str, enabled: bool, make: Callable[[], C]) -> C | None:  # noqa: FBT001 - the layer's switch
    """Build a layer's primitive when it is on, naming the component and layer on a bad tuning."""
    if not enabled:
        return None
    try:
        return make()
    except AppValueError as exc:
        msg = f"decoration: {name}.{layer}: {exc}"
        raise AppValueError(msg) from exc


def _retrier(r: decoration.Retry) -> Retrier:
    """Build the exponential-backoff retrier ``r`` tunes."""
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


def _breaker(b: decoration.CircuitBreaker) -> CircuitBreakerInterface:
    """Build the breaker ``b`` tunes; only a transient failure counts against it."""
    b.validate()
    return CircuitBreaker(
        failure_threshold=max(b.consecutive_failures, 1),
        recovery_timeout=b.timeout,
        is_failure=is_transient,
    )


_BULKHEAD_KINDS: dict[str, BulkheadKind] = {
    "": BulkheadKind.SEMAPHORE,
    "channel": BulkheadKind.SEMAPHORE,
    "semaphore": BulkheadKind.SEMAPHORE,
    "adaptive": BulkheadKind.ADAPTIVE,
}
"""Maps the config's bulkhead kind (Go's "channel" is the fixed limit) to a backend."""


def _bulkhead(b: decoration.Bulkhead) -> Bulkhead:
    """Build the bulkhead ``b`` tunes."""
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
        )
    )


def _rate_limiter(r: decoration.RateLimit) -> RateLimiter:
    """Build the token-bucket limiter ``r`` tunes."""
    r.validate()
    return TokenBucketRateLimiter(rate=r.rate, burst=r.burst)
