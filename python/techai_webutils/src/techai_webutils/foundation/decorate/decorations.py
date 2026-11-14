"""Builds every component's ``Deps`` from the decoration config.

It mirrors Go's ``decorations.go``.

The composition root makes one ``Decorations`` from the ``decoration`` config section and
the process collaborators, and hands it to each tier; a tier asks it for a component's
``Deps`` under the component's name.
"""

from __future__ import annotations

import dataclasses
import threading
from dataclasses import dataclass
from typing import TYPE_CHECKING, final

from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.config.decoration import load, on, sections
from techai_webutils.foundation.decorate.contract import ops_of
from techai_webutils.foundation.decorate.primitives import (
    breaker_for,
    bulkhead_for,
    layer_primitive,
    rate_limiter_for,
    retrier_for,
)
from techai_webutils.foundation.decorate.tier import (
    Chains,
    Deps,
    Layer,
    Observability,
    Subject,
)
from techai_webutils.foundation.resilience.observer import Observer

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.core.interfaces.config_loader import ConfigLoader
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


@final
class Decorations:
    """Builds every component's Deps from the decoration config.

    ``Decorations()`` (no config) decorates nothing: every component gets empty Deps, so
    only recovery runs.
    """

    __slots__ = ("_collab", "_config", "_errors", "_fixed", "_lock", "_used")

    def __init__(
        self,
        config: decoration.DecorationConfig | None = None,
        collaborators: Collaborators | None = None,
    ) -> None:
        """Bind the config (None decorates nothing) and the collaborators.

        Args:
            config: The decoration config; ``None`` decorates nothing.
            collaborators: The logger, metrics, tracer and authorizer; ``None`` uses none.

        """
        self._config = config
        self._collab = collaborators or Collaborators()
        self._used: dict[str, set[str]] = {}
        self._errors: list[AppValueError] = []
        self._lock = threading.Lock()
        self._fixed: Deps | None = None

    @classmethod
    def fixed(cls, deps: Deps) -> Decorations:
        """Return the Decorations that resolves every component and operation to ``deps``.

        A test builds a base of any tier over chosen collaborators with it (Go ``Fixed``).

        Args:
            deps: The Deps every component gets.

        Returns:
            The Decorations that resolves every component and operation to ``deps``.

        """
        decs = cls(None, Collaborators(observability=deps.observability))
        decs._fixed = deps
        return decs

    @classmethod
    def load(
        cls, loader: ConfigLoader, collaborators: Collaborators | None = None
    ) -> Decorations:
        """Return the Decorations over the ``decoration`` section ``loader`` holds.

        Go's ``LoadDecorations``.

        The section is laid over ``default_config()``; a malformed one raises
        ``AppValueError``.

        Args:
            loader: Reads the config.
            collaborators: The logger, metrics, tracer and authorizer.

        Returns:
            The Decorations over the ``decoration`` section ``loader`` holds.

        """
        return cls(load(loader), collaborators)

    @property
    def observability(self) -> Observability:
        """The collaborators' observability.

        For a component that logs or meters outside its layers.
        """
        return self._collab.observability

    def deps(self, tier: Tier, name: str) -> Deps:
        """Return the Deps of the component ``name`` in ``tier``.

        Go's ``Decorations.Deps``.

        The tier's defaults with the overrides of ``name`` and its dotted prefixes on top;
        ``name`` is recorded as used for ``verify``. A tier the config does not know, an
        override that turns on a layer ``tier`` does not order, or a layer whose tuning is
        invalid, raises ``AppValueError``.

        Args:
            tier: The tier.
            name: The component's dotted name.

        Returns:
            The Deps of the component ``name``.

        Raises:
            AppValueError: Decoration: no config for tier ``tier.name``.

        """
        with self._lock:
            self._used.setdefault(tier.name, set()).add(name)
        if self._fixed is not None:
            return self._fixed
        if self._config is None:
            return Deps()
        tc = self._config.tier(tier.name)
        if tc is None:
            msg = f"decoration: no config for tier {tier.name!r}"
            raise AppValueError(msg)
        _check(tier, name, tc.overriding(name))
        return build(tier.name, name, tc.layers_for(name), self._collab)

    def subject(self, tier: Tier, name: str) -> Subject:
        """Return the Subject of the component ``name`` in ``tier``.

        It is decorated as ``deps`` resolves it.

        A component built where raising is unwelcome (a stage, a job, a gate) resolves
        through it: a resolution error is recorded for ``verify`` and the Subject runs
        with recovery only until boot fails on it.

        Args:
            tier: The tier.
            name: The component's dotted name.

        Returns:
            The Subject of the component ``name`` in ``tier``.

        """
        return tier.subject(name, self.resolve(tier, name))

    def resolve(self, tier: Tier, name: str) -> Deps:
        """Return ``deps(tier, name)``.

        A resolution error is recorded for ``verify`` instead.

        On an error the component gets empty Deps (recovery only) until boot fails on it.

        Args:
            tier: The tier.
            name: The component's dotted name.

        Returns:
            ``deps(tier, name)``.

        """
        try:
            return self.deps(tier, name)
        except AppValueError as exc:
            with self._lock:
                self._errors.append(exc)
            return Deps()

    def new_subject(
        self,
        tier: Tier,
        name: str,
        ops: list[str] | None,
        adjust: tuple[Callable[[Deps], Deps], ...] = (),
    ) -> Subject:
        """Return the Subject of component ``name`` in ``tier`` over ``ops``.

        ``ops`` are its operations.

        The component resolves as ``name`` and each operation as ``<name>.<op>`` (Go
        ``NewSubject``). An operation reuses its component's breaker, bulkhead and rate
        limiter unless its own override configures that layer, so a limit set on the
        component bounds its operations together. Each ``adjust`` runs on every resolved
        Deps. ``ops=None`` runs any operation on the component's chains, for wire-level
        operation names. A resolution error raises ``AppValueError``.

        Args:
            tier: The tier.
            name: The component's dotted name.
            ops: The component's operations; ``None`` allows any.
            adjust: Adjust each operation's Deps.

        Returns:
            The Subject of the component ``name``.

        """
        comp = _adjusted(self.deps(tier, name), adjust)
        if ops is None:
            return Subject(name, tier.chains(name, comp))
        per_op: dict[str, Chains] = {}
        for op in ops:
            key = f"{name}.{op}"
            deps = _adjusted(
                self._share_stateful(tier, key, self.deps(tier, key), comp), adjust
            )
            per_op[op] = tier.chains(name, deps)
        return Subject(name, tier.chains(name, comp), per_op)

    def subject_of(
        self,
        tier: Tier,
        name: str,
        contract: type,
        adjust: tuple[Callable[[Deps], Deps], ...] = (),
    ) -> Subject:
        """Return the Subject of component ``name`` over ``contract``'s methods.

        Each method is an operation.

        Args:
            tier: The tier.
            name: The component's dotted name.
            contract: The interface whose methods are the operations.
            adjust: Adjust each operation's Deps.

        Returns:
            The Subject.

        """
        return self.new_subject(tier, name, ops_of(contract), adjust)

    def _share_stateful(self, tier: Tier, key: str, op: Deps, comp: Deps) -> Deps:
        """Return ``op`` with its component's stateful layers filled in.

        Only where its own override leaves them unset.

        Args:
            tier: The tier.
            key: The operation's dotted name.
            op: The operation's Deps.
            comp: The component's Deps.

        Returns:
            ``op`` with its component's stateful layers.

        """
        own = self._own_override(tier, key)
        if own is None or own.circuit_breaker.enabled is None:
            op = dataclasses.replace(op, breaker=comp.breaker)
        if own is None or own.bulkhead.enabled is None:
            op = dataclasses.replace(op, bulkhead=comp.bulkhead, shed_load=comp.shed_load)
        if own is None or own.rate_limit.enabled is None:
            op = dataclasses.replace(op, rate_limiter=comp.rate_limiter)
        return op

    def _own_override(self, tier: Tier, name: str) -> decoration.Layers | None:
        """Return the override configured for exactly ``name`` in ``tier``, or None.

        Args:
            tier: The tier.
            name: The component's dotted name.

        Returns:
            The override configured for exactly ``name`` in ``tier``, or None.

        """
        if self._config is None or self._fixed is not None:
            return None
        tc = self._config.tier(tier.name)
        return None if tc is None else tc.overrides.get(name)

    def inventory(self) -> list[Resolved]:
        """Return every component and operation resolved, by tier then name.

        Go's ``Inventory``.

        What a deployable prints with ``--print-decorations``. A Decorations with no
        config, or a fixed one, reports no layers.

        Returns:
            Every component and operation resolved.

        """
        out: list[Resolved] = []
        for tier_name, name in self.components():
            tc = None
            if self._config is not None and self._fixed is None:
                tc = self._config.tier(tier_name)
            out.append(
                Resolved(
                    tier=tier_name,
                    name=name,
                    sources=tc.sources(name) if tc is not None else [],
                    layers=tc.layers_for(name) if tc is not None else None,
                )
            )
        return out

    def components(self) -> list[tuple[str, str]]:
        """Return every component resolved as (tier name, component name).

        Sorted (Go's ``Components``).

        The inventory a generator writes one decoration entry for.

        Returns:
            Every component resolved as (tier name, component name).

        """
        with self._lock:
            return sorted((t, n) for t, names in self._used.items() for n in names)

    def verify_explicit(self, *prefixes: str) -> None:
        """Run ``verify``, and also fail on a component with no override of its own name.

        In a deployable whose decoration file is generated with one entry per component, a
        component without one was added after the file was generated and takes only its
        tier's defaults (Go ``VerifyExplicit``). Raises ``AppValueError`` naming every
        one.

        Args:
            *prefixes: The names the graph owns.

        Raises:
            AppValueError: The check failed.

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
        override owned by one of ``prefixes`` (named a prefix, or beneath one) that
        applies to no component resolved — a misspelled or stale key that would otherwise
        configure nothing. Several deployables share one config, so each judges only its
        own prefixes.

        Args:
            *prefixes: The names the graph owns.

        Raises:
            AppValueError: The check failed.

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


@dataclass(frozen=True, slots=True)
class Resolved:
    """One component or operation a Decorations resolved (Go ``Resolved``)."""

    tier: str
    """The tier's name ("service", "repository", …)."""

    name: str
    """The resolved dotted name: a component or one of its operations."""

    sources: list[str]
    """The config keys merged into ``layers``, in order."""

    layers: decoration.Layers | None
    """The layers the name resolved to (None without a config)."""


def _adjusted(deps: Deps, adjust: tuple[Callable[[Deps], Deps], ...]) -> Deps:
    """Return ``deps`` after each ``adjust``, in order.

    Args:
        deps: The Deps.
        adjust: The adjustments, in order.

    Returns:
        ``deps`` after each ``adjust``, in order.

    """
    for a in adjust:
        deps = a(deps)
    return deps


def _owned(key: str, prefixes: tuple[str, ...]) -> bool:
    """Report whether ``key`` is one of ``prefixes`` or beneath one.

    Args:
        key: An override key.
        prefixes: The names the graph owns.

    Returns:
        Whether ``key`` is owned.

    """
    return any(key == p or key.startswith(p + ".") for p in prefixes)


def _covers(used: set[str], key: str) -> bool:
    """Report whether the override ``key`` applies to a used component.

    It applies to ``key`` or a component beneath it.

    Args:
        used: The names resolved so far.
        key: An override key.

    Returns:
        Whether the override applies.

    """
    return any(name == key or name.startswith(key + ".") for name in used)


def _check(tier: Tier, name: str, layers: decoration.Layers) -> None:
    """Raise ``AppValueError`` when ``layers`` turns on a layer ``tier`` does not order.

    Such a layer would be accepted and never run, so the operator would believe a
    protection is in place that is not.

    Args:
        tier: The tier.
        name: The component's dotted name.
        layers: Its resolved layers.

    Raises:
        AppValueError: Decoration: ``tier.name`` ``name`` turns on ``key``, which the
            ``tier.name`` tier does not run.

    """
    switched = (
        ("recovery", Layer.RECOVERY, on(switch=layers.recovery)),
        ("tracing", Layer.TRACING, on(switch=layers.tracing)),
        ("metrics", Layer.METRICS, on(switch=layers.metrics)),
        ("logging", Layer.LOGGING, on(switch=layers.logging)),
        ("auth", Layer.AUTH, on(switch=layers.auth)),
        ("timeout", Layer.TIMEOUT, bool(layers.timeout)),
        ("retry", Layer.RETRY, on(switch=layers.retry.enabled)),
        (
            "circuit_breaker",
            Layer.CIRCUIT_BREAKER,
            on(switch=layers.circuit_breaker.enabled),
        ),
        ("bulkhead", Layer.BULKHEAD, on(switch=layers.bulkhead.enabled)),
        ("rate_limit", Layer.RATE_LIMIT, on(switch=layers.rate_limit.enabled)),
    )
    for key, layer, enabled in switched:
        if enabled and layer not in tier.order:
            msg = (
                f"decoration: {tier.name} {name} turns on {key}, "
                f"which the {tier.name} tier does not run"
            )
            raise AppValueError(msg)


def build(tier: str, name: str, layers: decoration.Layers, collab: Collaborators) -> Deps:
    """Return the Deps of the component ``name`` in ``tier`` from its resolved ``layers``.

    Each layer that is on gets its collaborator from ``collab``, or its primitive built
    from its tuning (the breaker guards ``name``). The breaker and an adaptive bulkhead
    report their own state through an ``Observer`` bound to ``tier`` and ``name``, whose
    recorder follows the metrics layer and whose logger follows the logging layer; the
    retrier, limiter and semaphore bulkhead get none, since their layers record for them.
    An invalid tuning raises ``AppValueError``.

    Args:
        tier: The tier's name (Go ``Build``'s tier).
        name: The component's dotted name.
        layers: Its resolved layers.
        collab: The process collaborators.

    Returns:
        The Deps of the component ``name`` from its resolved ``layers``.

    """
    obs = collab.observability
    metrics = obs.metrics if on(switch=layers.metrics) else None
    logger = obs.logger if on(switch=layers.logging) else None
    observer = Observer.of(metrics, logger, tier, name)
    return Deps(
        observability=Observability(
            logger=logger,
            metrics=metrics,
            tracer=obs.tracer if on(switch=layers.tracing) else None,
        ),
        auth=collab.auth if on(switch=layers.auth) else None,
        timeout=layers.timeout or 0.0,
        skip_recovery=not on(switch=layers.recovery),
        panic_logger=obs.logger if on(switch=layers.recovery) else None,
        retrier=layer_primitive(
            name,
            "retry",
            enabled=on(switch=layers.retry.enabled),
            make=lambda: retrier_for(layers.retry),
        ),
        breaker=layer_primitive(
            name,
            "circuit_breaker",
            enabled=on(switch=layers.circuit_breaker.enabled),
            make=lambda: breaker_for(layers.circuit_breaker, metrics, observer),
        ),
        bulkhead=layer_primitive(
            name,
            "bulkhead",
            enabled=on(switch=layers.bulkhead.enabled),
            make=lambda: bulkhead_for(layers.bulkhead, observer.recorder),
        ),
        shed_load=on(switch=layers.bulkhead.enabled) and on(switch=layers.bulkhead.shed),
        rate_limiter=layer_primitive(
            name,
            "rate_limit",
            enabled=on(switch=layers.rate_limit.enabled),
            make=lambda: rate_limiter_for(layers.rate_limit),
        ),
    )
