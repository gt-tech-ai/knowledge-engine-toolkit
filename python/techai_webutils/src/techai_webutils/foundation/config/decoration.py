"""The config surface for every decorated component (mirrors Go ``schema/decoration``).

Controllers, clients, workflows, pipelines, services, repositories, stores and jobs each
have a tier of default ``Layers`` and per-component overrides keyed by the component's
name; every layer (recovery, tracing, metrics, logging, auth, timeout, retry, circuit
breaker, bulkhead, rate limit) is switched and tuned here, while its order stays the
tier's, in code. ``foundation.decorate.Decorations`` turns a component's resolved layers
into its ``Deps``.

The section is the same YAML the Go services read (``decoration:`` in the layered config),
with the same keys; durations are Go duration strings (``"30s"``). Keys this schema does
not know are ignored, as Go's decoder ignores them.

The default observes each request at its edges — its controller and workflow, and every
client and job it calls — and leaves the pipeline, service, repository and store tiers
unobserved until switched on; resilience runs where today's components run it.
"""

from __future__ import annotations

import dataclasses
import typing
from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import cast

from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.config.duration import parse_duration

if typing.TYPE_CHECKING:
    from _typeshed import DataclassInstance

    from techai_webutils.core.interfaces.config_loader import ConfigLoader

SECTION_KEY = "decoration"
"""The config key the decoration section lives under."""

_DURATION = {"duration": True}
"""Field metadata marking a duration read from a Go duration string."""


@dataclass(frozen=True, slots=True)
class Retry:
    """Switches and tunes the retry layer.

    (exponential backoff over transient failures).
    """

    enabled: bool | None = None
    """Turns the layer on; None inherits."""

    initial_interval: float = field(default=0.0, metadata=_DURATION)
    """The first backoff, in seconds."""

    max_interval: float = field(default=0.0, metadata=_DURATION)
    """The longest single backoff, in seconds."""

    max_elapsed_time: float = field(default=0.0, metadata=_DURATION)
    """The total time budget across attempts, in seconds."""

    multiplier: float = 0.0
    """The backoff growth factor (>= 1)."""

    max_retries: int = 0
    """The retries after the first attempt (>= 0)."""

    def validate(self) -> None:
        """Raise ``AppValueError`` on an out-of-range tuning."""
        _require(ok=self.max_retries >= 0, msg="retry.max_retries must be >= 0")
        _require(ok=self.initial_interval >= 0, msg="retry.initial_interval must be >= 0")
        _require(ok=self.max_interval >= 0, msg="retry.max_interval must be >= 0")
        _require(ok=self.multiplier >= 1, msg="retry.multiplier must be >= 1")


@dataclass(frozen=True, slots=True)
class CircuitBreaker:
    """Switches and tunes the circuit-breaker layer.

    The breaker is named after the component.
    """

    enabled: bool | None = None
    """Turns the layer on; None inherits."""

    interval: float = field(default=0.0, metadata=_DURATION)
    """The closed-state window the failure counts reset over, in seconds (Go only)."""

    timeout: float = field(default=0.0, metadata=_DURATION)
    """How long the breaker stays open before a trial call, in seconds."""

    failure_ratio: float = 0.0
    """The failure ratio that trips the breaker once min_requests are seen (Go only)."""

    max_requests: int = 0
    """The trial calls admitted at once while half-open; 0 admits every caller."""

    consecutive_failures: int = 0
    """The consecutive failures that trip the breaker."""

    min_requests: int = 0
    """The calls the failure ratio needs before it can trip (Go only)."""

    def validate(self) -> None:
        """Raise ``AppValueError`` on an out-of-range tuning."""
        _require(
            ok=0 <= self.failure_ratio <= 1,
            msg="circuit_breaker.failure_ratio must be in [0, 1]",
        )
        _require(ok=self.interval >= 0, msg="circuit_breaker.interval must be >= 0")
        _require(ok=self.timeout >= 0, msg="circuit_breaker.timeout must be >= 0")
        _require(
            ok=self.max_requests >= 0, msg="circuit_breaker.max_requests must be >= 0"
        )


@dataclass(frozen=True, slots=True)
class Bulkhead:
    """Switches and tunes the bulkhead layer."""

    enabled: bool | None = None
    """Turns the layer on; None inherits."""

    shed: bool | None = None
    """Rejects an operation when the bulkhead is full (RESOURCE_EXHAUSTED) instead of
    waiting."""

    kind: str = ""
    """The limiter: "channel" (Go's name for the fixed limit; "semaphore" here too) or
    "adaptive"."""

    rtt_threshold: float = field(default=0.0, metadata=_DURATION)
    """The adaptive limiter's slow-sample threshold, in seconds."""

    backoff_ratio: float = 0.0
    """The adaptive limiter's multiplicative decrease, in (0, 1]."""

    max_concurrent: int = 0
    """The concurrency limit (the adaptive ceiling)."""

    min_concurrent: int = 0
    """The adaptive floor."""

    initial_concurrent: int = 0
    """The adaptive starting limit."""

    def validate(self) -> None:
        """Raise ``AppValueError`` on an out-of-range tuning."""
        _require(ok=self.max_concurrent >= 1, msg="bulkhead.max_concurrent must be >= 1")
        _require(ok=self.min_concurrent >= 0, msg="bulkhead.min_concurrent must be >= 0")
        _require(
            ok=0 < self.backoff_ratio <= 1, msg="bulkhead.backoff_ratio must be in (0, 1]"
        )


@dataclass(frozen=True, slots=True)
class RateLimit:
    """Switches and tunes the rate-limit layer (a token bucket)."""

    enabled: bool | None = None
    """Turns the layer on; None inherits."""

    rate: float = 0.0
    """The sustained operations per second."""

    burst: int = 0
    """The bucket capacity."""

    def validate(self) -> None:
        """Raise ``AppValueError`` on an out-of-range tuning."""
        _require(ok=self.rate >= 0, msg="rate_limit.rate must be >= 0")
        _require(ok=self.burst >= 0, msg="rate_limit.burst must be >= 0")


@dataclass(frozen=True, slots=True)
class Layers:
    """Switches and tunes each decoration layer of one component.

    A None switch (or timeout) inherits; ``merge`` lays one Layers over another. A layer
    that is on still needs its collaborator (a tracer for tracing, an authorizer for
    auth), and runs only where the tier orders it.
    """

    recovery: bool | None = None
    """Codes an unexpected exception as INTERNAL."""

    tracing: bool | None = None
    """Opens a span per operation."""

    metrics: bool | None = None
    """Records the tier's operation, error and duration series."""

    logging: bool | None = None
    """Logs each operation."""

    auth: bool | None = None
    """Authorizes each operation as ``<name>.<op>``."""

    timeout: float | None = field(default=None, metadata=_DURATION)
    """Bounds each operation (each attempt, under retry), in seconds; zero turns it
    off."""

    retry: Retry = field(default_factory=Retry)
    """Retries transient failures with exponential backoff."""

    circuit_breaker: CircuitBreaker = field(default_factory=CircuitBreaker)
    """Sheds calls while the component keeps failing."""

    rate_limit: RateLimit = field(default_factory=RateLimit)
    """Rejects operations over a token-bucket rate."""

    bulkhead: Bulkhead = field(default_factory=Bulkhead)
    """Bounds the operations in flight."""


def on(*, switch: bool | None) -> bool:
    """Report whether a layer switch is on (None is off).

    Args:
        switch: The layer's switch.

    Returns:
        Whether the switch is on.

    """
    return switch is True


def merge[T](base: T, over: T) -> T:
    """Return ``base`` with every value ``over`` sets laid on top.

    A set switch or timeout (a field whose unset value is None, so zero still wins — a
    timeout of zero turns it off) and a non-zero tuning value win; anything else keeps
    ``base``.

    Args:
        base: The lower value.
        over: The overriding value; its set fields win.

    Returns:
        ``base`` with every value ``over`` sets laid on top.

    """
    changes: dict[str, object] = {}
    for f in dataclasses.fields(typing.cast("DataclassInstance", base)):
        b: object = cast("object", getattr(base, f.name))
        o: object = cast("object", getattr(over, f.name))
        if dataclasses.is_dataclass(o):
            changes[f.name] = merge(b, o)
        elif o is not None and (f.default is None or bool(o)):
            changes[f.name] = o
    replaced = dataclasses.replace(typing.cast("DataclassInstance", base), **changes)
    return typing.cast("T", replaced)


@dataclass(frozen=True, slots=True)
class Tier:
    """One tier's decoration.

    The defaults every component gets and per-component overrides.
    """

    overrides: Mapping[str, Layers] = field(default_factory=dict[str, Layers])
    """Maps a component name to the layers it lays over the defaults."""

    defaults: Layers = field(default_factory=Layers)
    """Every component's layers."""

    def layers_for(self, name: str) -> Layers:
        """Return the layers of the component named ``name`` (Go ``Tier.For``).

        The defaults, then the override of each dotted prefix of ``name`` from the
        shortest, then ``name``'s own: ``api.documents.create`` gets the defaults,
        ``api``, ``api.documents``, then its own, so one key covers a group and a deeper
        key one component.

        Args:
            name: The component's dotted name.

        Returns:
            The layers of the component named ``name`` (Go ``Tier.For``).

        """
        return merge(self.defaults, self.overriding(name))

    def sources(self, name: str) -> list[str]:
        """Return the config keys ``layers_for(name)`` merges.

        In order (Go ``Tier.Sources``).

        ``defaults`` when the tier has any, then each override of a dotted prefix of
        ``name`` from the shortest, then ``name``'s own.

        Args:
            name: The component's dotted name.

        Returns:
            The config keys ``layers_for(name)`` merges.

        """
        out = ["defaults"] if self.defaults != Layers() else []
        parts = name.split(".")
        for i in range(1, len(parts) + 1):
            key = ".".join(parts[:i])
            if key in self.overrides:
                out.append(f"overrides.{key}")
        return out

    def overriding(self, name: str) -> Layers:
        """Return the layers the overrides lay over the defaults.

        For ``name`` (Go ``Overriding``).

        Args:
            name: The component's dotted name.

        Returns:
            The layers the overrides lay over the defaults.

        """
        layers = Layers()
        parts = name.split(".")
        for i in range(1, len(parts) + 1):
            over = self.overrides.get(".".join(parts[:i]))
            if over is not None:
                layers = merge(layers, over)
        return layers

    def overlay(self, over: Tier) -> Tier:
        """Lay ``over`` on this tier: its defaults over these.

        Each override over the same name's.

        Args:
            over: The overriding tier.

        Returns:
            The merged tier.

        """
        overrides = dict(self.overrides)
        for name, layers in over.overrides.items():
            overrides[name] = merge(overrides.get(name, Layers()), layers)
        return Tier(overrides=overrides, defaults=merge(self.defaults, over.defaults))


@dataclass(frozen=True, slots=True)
class DecorationConfig:
    """The decoration of every tier (the ``decoration`` config section)."""

    controllers: Tier = field(default_factory=Tier)
    """The controller (transport-handler) tier (``decoration.controllers``)."""

    clients: Tier = field(default_factory=Tier)
    """The clients tier (``decoration.clients``)."""

    workflows: Tier = field(default_factory=Tier)
    """The workflow tier (``decoration.workflows``)."""

    pipelines: Tier = field(default_factory=Tier)
    """The pipeline tier (``decoration.pipelines``)."""

    services: Tier = field(default_factory=Tier)
    """The service tier (``decoration.services``)."""

    repositories: Tier = field(default_factory=Tier)
    """The repository tier (``decoration.repositories``)."""

    stores: Tier = field(default_factory=Tier)
    """The store tier (``decoration.stores``)."""

    jobs: Tier = field(default_factory=Tier)
    """The execution-job tier (``decoration.jobs``)."""

    gates: Tier = field(default_factory=Tier)
    """The execution-gate tier (``decoration.gates``)."""

    def tier(self, name: str) -> Tier | None:
        """Return the decoration of the tier named ``name`` ("controller", "client", …).

        Or None.

        Args:
            name: The tier's name.

        Returns:
            The decoration of the tier named ``name`` ("controller", "client", …).

        """
        attr = _TIER_FIELDS.get(name)
        return None if attr is None else typing.cast("Tier", getattr(self, attr))

    def overlay(self, over: DecorationConfig) -> DecorationConfig:
        """Lay ``over`` on this config, tier by tier (see ``Tier.overlay``).

        Args:
            over: The overriding config.

        Returns:
            The merged config.

        """
        return DecorationConfig(**{
            attr: typing.cast("Tier", getattr(self, attr)).overlay(
                typing.cast("Tier", getattr(over, attr))
            )
            for attr in _TIER_FIELDS.values()
        })


_TIER_FIELDS: dict[str, str] = {
    "controller": "controllers",
    "client": "clients",
    "workflow": "workflows",
    "pipeline": "pipelines",
    "service": "services",
    "repository": "repositories",
    "store": "stores",
    "job": "jobs",
    "gate": "gates",
}
"""Maps each tier name to its DecorationConfig field."""


def sections(config: DecorationConfig) -> list[tuple[str, str, Tier]]:
    """Return every tier's decoration as (config key, tier name, tier).

    In config order (Go ``Sections``).

    Args:
        config: The decoration config.

    Returns:
        Every tier's decoration as (config key, tier name, tier).

    """
    return [(attr, name, getattr(config, attr)) for name, attr in _TIER_FIELDS.items()]


def default_config() -> DecorationConfig:
    """Return today's decoration (the same as Go's ``DefaultConfig``).

    Recovery and auth wherever a tier orders them; every controller, client, workflow, job
    and gate observed, and the pipeline, service, repository and store tiers not; retry
    and a circuit breaker at the repository tier; and a 30 s timeout, a bulkhead and a
    breaker around the storage, outbox and Prometheus clients. Every other resilience
    layer, the controllers' rate limit and bulkhead included, is off; a controller's
    bulkhead, once enabled, sheds.

    Returns:
        Today's decoration (the same as Go's ``DefaultConfig``).

    """
    repos = _base(observe=False)
    repos = dataclasses.replace(
        repos,
        retry=dataclasses.replace(repos.retry, enabled=True),
        circuit_breaker=dataclasses.replace(repos.circuit_breaker, enabled=True),
    )
    controllers = _base(observe=True)
    # A controller's bulkhead, once enabled, sheds: a request queued behind a full
    # bulkhead holds its connection, so the edge rejects it instead.
    controllers = dataclasses.replace(
        controllers, bulkhead=dataclasses.replace(controllers.bulkhead, shed=True)
    )
    guarded = Layers(
        timeout=30.0,
        bulkhead=Bulkhead(enabled=True),
        circuit_breaker=CircuitBreaker(enabled=True),
    )
    return DecorationConfig(
        controllers=Tier(defaults=controllers),
        clients=Tier(
            defaults=_base(observe=True),
            overrides={
                "storage": guarded,
                "outbox.sqs": guarded,
                "outbox.s3": guarded,
                "prometheus": guarded,
            },
        ),
        workflows=Tier(defaults=_base(observe=True)),
        pipelines=Tier(defaults=_base(observe=False)),
        services=Tier(defaults=_base(observe=False)),
        repositories=Tier(defaults=repos),
        stores=Tier(defaults=_base(observe=False)),
        jobs=Tier(defaults=_base(observe=True)),
        gates=Tier(defaults=_base(observe=True)),
    )


def _base(*, observe: bool) -> Layers:
    """Return a tier's default layers: recovery and auth on.

    Observability as ``observe`` says.

    Args:
        observe: Whether the observability layers are on.

    Returns:
        A tier's default layers: recovery and auth on.

    """
    return Layers(
        recovery=True,
        tracing=observe,
        metrics=observe,
        logging=observe,
        auth=True,
        timeout=0.0,
        retry=Retry(
            enabled=False,
            initial_interval=0.1,
            max_interval=5.0,
            max_elapsed_time=30.0,
            multiplier=2.0,
            max_retries=3,
        ),
        circuit_breaker=CircuitBreaker(
            enabled=False,
            interval=60.0,
            timeout=30.0,
            failure_ratio=0.5,
            max_requests=1,
            consecutive_failures=5,
            min_requests=10,
        ),
        rate_limit=RateLimit(enabled=False, rate=100.0, burst=10),
        bulkhead=Bulkhead(
            enabled=False,
            shed=False,
            kind="channel",
            rtt_threshold=0.1,
            backoff_ratio=0.9,
            max_concurrent=10,
            min_concurrent=1,
            initial_concurrent=5,
        ),
    )


def from_mapping(section: object) -> DecorationConfig:
    """Decode a ``decoration`` section and lay it over ``default_config()``.

    A malformed section (a non-mapping, a switch that is not a bool, a bad duration or
    number) raises ``AppValueError``.

    Args:
        section: The raw ``decoration`` section.

    Returns:
        A ``decoration`` section and lay it over ``default_config()``.

    """
    return default_config().overlay(_decode(DecorationConfig, section, SECTION_KEY))


def load(loader: ConfigLoader) -> DecorationConfig:
    """Read the decoration section from ``loader`` over ``default_config()``.

    An absent section keeps the default and a partial one changes only what it sets.

    Args:
        loader: Reads the config.

    Returns:
        The decoration section from ``loader`` over ``default_config()``.

    """
    section = loader.get(SECTION_KEY)
    return default_config() if section is None else from_mapping(section)


def _decode[T](cls: type[T], raw: object, path: str) -> T:
    """Decode the mapping ``raw`` into the dataclass ``cls``, field by field.

    Args:
        cls: The dataclass.
        raw: The raw mapping.
        path: Its config path, for errors.

    Returns:
        The mapping ``raw`` into the dataclass ``cls``, field by field.

    Raises:
        AppValueError: ``path``: expected a mapping, got ``type(raw).__name__``.

    """
    if not isinstance(raw, Mapping):
        msg = f"{path}: expected a mapping, got {type(raw).__name__}"
        raise AppValueError(msg)
    raw_map = typing.cast("Mapping[str, object]", raw)
    hints = typing.get_type_hints(cls)
    values: dict[str, object] = {}
    for f in dataclasses.fields(typing.cast("type[DataclassInstance]", cls)):
        if f.name not in raw_map:
            continue
        values[f.name] = _decode_value(
            typing.cast("object", hints[f.name]), f, raw_map[f.name], f"{path}.{f.name}"
        )
    return cls(**values)


def _field_default(f: dataclasses.Field[object]) -> object:
    """Return the value a field takes when its config is absent.

    Args:
        f: The dataclass field.

    Returns:
        The field's default, its factory's product, or ``None`` when it has neither.

    """
    if f.default is not dataclasses.MISSING:
        return f.default
    if f.default_factory is not dataclasses.MISSING:
        return f.default_factory()
    return None


def _decode_value(
    hint: object, f: dataclasses.Field[object], raw: object, path: str
) -> object:
    """Decode one field's raw value by its type hint.

    Args:
        hint: The field's type hint.
        f: The field.
        raw: Its raw value.
        path: Its config path, for errors.

    Returns:
        One field's raw value by its type hint.

    Raises:
        AppValueError: ``path``: expected a bool, got ``raw``.

    """
    if raw is None:
        return _field_default(f)
    if f.metadata.get("duration"):
        return parse_duration(raw)
    if hint is Tier:
        return _decode_tier(raw, path)
    if dataclasses.is_dataclass(hint):
        return _decode(typing.cast("type[object]", hint), raw, path)
    if bool in typing.get_args(hint) or hint is bool:
        if not isinstance(raw, bool):
            msg = f"{path}: expected a bool, got {raw!r}"
            raise AppValueError(msg)
        return raw
    return _decode_scalar(hint, raw, path)


def _decode_tier(raw: object, path: str) -> Tier:
    """Decode a tier: its defaults and its overrides, each a Layers.

    Args:
        raw: The raw tier.
        path: Its config path, for errors.

    Returns:
        A tier: its defaults and its overrides, each a Layers.

    """
    tier = _decode(
        Tier, {k: v for k, v in _mapping(raw, path).items() if k == "defaults"}, path
    )
    overrides_raw: object = _mapping(raw, path).get("overrides") or {}
    return dataclasses.replace(
        tier, overrides=_decode_overrides(overrides_raw, f"{path}.overrides", "")
    )


def _decode_overrides(raw: object, path: str, prefix: str) -> dict[str, Layers]:
    """Decode the overrides beneath ``prefix`` by full name.

    Components are named with dots (``outbox.sqs``); an override may be written as one
    dotted key or nested by segment, and a segment that sets no layer is no override.

    Args:
        raw: The raw overrides.
        path: Their config path, for errors.
        prefix: The dotted name built so far.

    Returns:
        The overrides beneath ``prefix`` by full name.

    """
    out: dict[str, Layers] = {}
    for key, value in _mapping(raw, path).items():
        if prefix and key in _LAYER_KEYS:
            continue
        name = f"{prefix}.{key}" if prefix else str(key)
        layers = _mapping(value, f"{path}.{key}")
        if any(k in _LAYER_KEYS for k in layers):
            out[name] = _decode(Layers, layers, f"{path}.{key}")
        nested = {k: v for k, v in layers.items() if k not in _LAYER_KEYS}
        out.update(_decode_overrides(nested, f"{path}.{key}", name))
    return out


_LAYER_KEYS = frozenset(f.name for f in dataclasses.fields(Layers))
"""The config keys of Layers' fields."""


def _mapping(raw: object, path: str) -> Mapping[str, object]:
    """Return ``raw`` as a mapping, or raise ``AppValueError``.

    Args:
        raw: The raw value.
        path: Its config path, for errors.

    Returns:
        ``raw`` as a mapping, or raise ``AppValueError``.

    Raises:
        AppValueError: ``path``: expected a mapping, got ``type(raw).__name__``.

    """
    if not isinstance(raw, Mapping):
        msg = f"{path}: expected a mapping, got {type(raw).__name__}"
        raise AppValueError(msg)
    return typing.cast("Mapping[str, object]", raw)


def _decode_scalar(hint: object, raw: object, path: str) -> object:
    """Decode an int, float or str field.

    Args:
        hint: The field's type hint.
        raw: Its raw value.
        path: Its config path, for errors.

    Returns:
        An int, float or str field.

    Raises:
        AppValueError: ``path``: invalid value ``raw``.

    """
    try:
        if hint is int and not isinstance(raw, bool):
            return int(typing.cast("int", raw))
        if hint is float and not isinstance(raw, bool):
            return float(typing.cast("float", raw))
        if hint is str:
            return str(raw)
    except (TypeError, ValueError) as exc:
        msg = f"{path}: invalid value {raw!r}"
        raise AppValueError(msg) from exc
    msg = f"{path}: invalid value {raw!r}"
    raise AppValueError(msg)


def _require(*, ok: bool, msg: str) -> None:
    """Raise ``AppValueError(msg)`` unless ``ok``.

    Args:
        ok: The condition.
        msg: The error's message.

    Raises:
        AppValueError: The check failed.

    """
    if not ok:
        raise AppValueError(msg)
