"""Builds every component's ``Deps`` from the decoration config (mirrors Go ``decorations.go``).

The composition root makes one ``Decorations`` from the ``decoration`` config section and the
process collaborators, and hands it to each tier; a tier asks it for a component's ``Deps``
under the component's name.
"""

from __future__ import annotations

import dataclasses
import threading
from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.config.decoration import load, on, sections
from techai_webutils.foundation.decorate.scope import Scope
from techai_webutils.foundation.decorate.tier import Deps, Layer, Observability
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
    from techai_webutils.core.interfaces.config_loader import ConfigLoader
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter
    from techai_webutils.core.interfaces.retrier import Retrier
    from techai_webutils.foundation.config import decoration
    from techai_webutils.foundation.decorate.middleware import Authorizer
    from techai_webutils.foundation.decorate.tier import Subject, Tier


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

    __slots__ = ("_collab", "_config", "_errors", "_lock", "_used")

    def __init__(
        self,
        config: decoration.DecorationConfig | None = None,
        collaborators: Collaborators | None = None,
    ) -> None:
        """Bind the config (None decorates nothing) and the collaborators."""
        self._config = config
        self._collab = collaborators or Collaborators()
        self._used: dict[str, set[str]] = {}
        self._errors: list[AppValueError] = []
        self._lock = threading.Lock()

    @classmethod
    def load(
        cls, loader: ConfigLoader, collaborators: Collaborators | None = None
    ) -> Decorations:
        """Return the Decorations over the ``decoration`` section ``loader`` holds (Go ``LoadDecorations``).

        The section is laid over ``default_config()``; a malformed one raises ``AppValueError``.
        """
        return cls(load(loader), collaborators)

    @property
    def observability(self) -> Observability:
        """The collaborators' observability, for a component that logs or meters outside its layers."""
        return self._collab.observability

    def deps(self, tier: Tier, name: str) -> Deps:
        """Return the Deps of the component ``name`` in ``tier`` (Go ``Decorations.Deps``).

        The tier's defaults with the overrides of ``name`` and its dotted prefixes on top;
        ``name`` is recorded as used for ``verify``. A tier the config does not know, an
        override that turns on a layer ``tier`` does not order, or a layer whose tuning is
        invalid, raises ``AppValueError``.
        """
        with self._lock:
            self._used.setdefault(tier.name, set()).add(name)
        if self._config is None:
            return Deps()
        tc = self._config.tier(tier.name)
        if tc is None:
            msg = f"decoration: no config for tier {tier.name!r}"
            raise AppValueError(msg)
        _check(tier, name, tc.overriding(name))
        return build(name, tc.layers_for(name), self._collab)

    def subject(self, tier: Tier, name: str) -> Subject:
        """Return the Subject of the component ``name`` in ``tier``, decorated as ``deps`` resolves it.

        A component built where raising is unwelcome (a stage, a job, a gate) resolves through
        it: a resolution error is recorded for ``verify`` and the Subject runs with recovery only
        until boot fails on it.
        """
        return tier.subject(name, self.resolve(tier, name))

    def resolve(self, tier: Tier, name: str) -> Deps:
        """Return ``deps(tier, name)``, recording a resolution error for ``verify`` instead.

        On an error the component gets empty Deps (recovery only) until boot fails on it.
        """
        try:
            return self.deps(tier, name)
        except AppValueError as exc:
            with self._lock:
                self._errors.append(exc)
            return Deps()

    def scope(self, tier: Tier, name: str) -> Scope:
        """Return the Scope of the component ``name`` in ``tier``: each operation resolved by name."""
        return Scope(tier, name, Deps(), self)

    def components(self) -> list[tuple[str, str]]:
        """Return every component resolved as (tier name, component name), sorted (Go ``Components``).

        The inventory a generator writes one decoration entry for.
        """
        with self._lock:
            return sorted((t, n) for t, names in self._used.items() for n in names)

    def verify_explicit(self, *prefixes: str) -> None:
        """Run ``verify``, and also fail on a component with no override of its own name.

        In a deployable whose decoration file is generated with one entry per component, a
        component without one was added after the file was generated and takes only its tier's
        defaults (Go ``VerifyExplicit``). Raises ``AppValueError`` naming every one.
        """
        problems: list[str] = []
        try:
            self.verify(*prefixes)
        except AppValueError as exc:
            problems.append(str(exc))
        tiers = sections(self._config) if self._config is not None else []
        keys = {tier_name: key for key, tier_name, _ in tiers}
        overrides = {tier_name: tier.overrides for _, tier_name, tier in tiers}
        missing = [
            f"decoration.{keys.get(t, t)}.overrides.{n}"
            for t, n in self.components()
            if _owned(n, prefixes) and n not in overrides.get(t, {})
        ]
        if missing:
            problems.append("no entry for " + ", ".join(missing))
        if problems:
            msg = "decoration: " + "; ".join(problems)
            raise AppValueError(msg)

    def verify(self, *prefixes: str) -> None:
        """Check the decoration of a built graph (Go ``Decorations.Verify``).

        The composition root calls it once every component is built. It raises
        ``AppValueError`` with every resolution error ``subject`` recorded, and with every
        override owned by one of ``prefixes`` (named a prefix, or beneath one) that applies to
        no component resolved — a misspelled or stale key that would otherwise configure
        nothing. Several deployables share one config, so each judges only its own prefixes.
        """
        tiers = sections(self._config) if self._config is not None else []
        with self._lock:
            problems = [str(e) for e in self._errors]
            unused = [
                f"decoration.{key}.overrides.{name}"
                for key, tier_name, tier in tiers
                for name in sorted(tier.overrides)
                if _owned(name, prefixes)
                and not _covers(self._used.get(tier_name, set()), name)
            ]
        if unused:
            problems.append("no component uses " + ", ".join(unused))
        if problems:
            msg = "decoration: " + "; ".join(problems)
            raise AppValueError(msg)


def _owned(key: str, prefixes: tuple[str, ...]) -> bool:
    """Report whether ``key`` is one of ``prefixes`` or beneath one."""
    return any(key == p or key.startswith(p + ".") for p in prefixes)


def _covers(used: set[str], key: str) -> bool:
    """Report whether the override ``key`` applies to a used component: ``key`` or one beneath it."""
    return any(name == key or name.startswith(key + ".") for name in used)


def _check(tier: Tier, name: str, layers: decoration.Layers) -> None:
    """Raise ``AppValueError`` when ``layers`` turns on a layer ``tier`` does not order.

    Such a layer would be accepted and never run, so the operator would believe a protection is
    in place that is not.
    """
    switched = (
        ("recovery", Layer.RECOVERY, on(layers.recovery)),
        ("tracing", Layer.TRACING, on(layers.tracing)),
        ("metrics", Layer.METRICS, on(layers.metrics)),
        ("logging", Layer.LOGGING, on(layers.logging)),
        ("auth", Layer.AUTH, on(layers.auth)),
        ("timeout", Layer.TIMEOUT, bool(layers.timeout)),
        ("retry", Layer.RETRY, on(layers.retry.enabled)),
        ("circuit_breaker", Layer.CIRCUIT_BREAKER, on(layers.circuit_breaker.enabled)),
        ("bulkhead", Layer.BULKHEAD, on(layers.bulkhead.enabled)),
        ("rate_limit", Layer.RATE_LIMIT, on(layers.rate_limit.enabled)),
    )
    for key, layer, enabled in switched:
        if enabled and layer not in tier.order:
            msg = (
                f"decoration: {tier.name} {name} turns on {key}, "
                f"which the {tier.name} tier does not run"
            )
            raise AppValueError(msg)


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
