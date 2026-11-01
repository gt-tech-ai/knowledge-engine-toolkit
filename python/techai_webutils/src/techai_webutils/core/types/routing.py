"""Routing value types: what a routed model gateway filters, plans and decides over.

These are plain data shared by the routing stages (filter, classifier, ranker, planner) and the
policy store. The provider configuration each endpoint is built from stays in the client tier's
catalog; a stage sees only a ``CatalogEndpointRef`` — the endpoint's identity, capabilities,
priors and data-policy facts.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from types import MappingProxyType
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Mapping
    from decimal import Decimal


@dataclass(frozen=True, slots=True)
class Capabilities:
    """What a model can do (the filter's capability check)."""

    context_tokens: int
    """Prompt plus output tokens the model accepts."""
    max_output: int
    """Most tokens the model generates in one call."""
    streaming: bool = True
    """Whether the model streams."""
    tools: bool = False
    """Whether the model accepts tool definitions."""
    json: bool = False
    """Whether the model has a JSON output mode."""


@dataclass(frozen=True, slots=True)
class CatalogEndpointRef:
    """One servable endpoint of a catalog model, as the routing stages see it."""

    model_id: str
    """The catalog id (``provider/model``)."""
    endpoint: str
    """The endpoint's name, unique within its model."""
    capabilities: Capabilities
    """The model's capabilities."""
    quality: float
    """The model's quality prior in ``[0, 1]``."""
    ttft_ms: int
    """The model's time-to-first-token prior, in milliseconds."""
    region: str
    """Where the endpoint runs (data residency)."""
    zero_retention: bool
    """Whether the endpoint retains no prompt or completion data."""
    leaves_cloud_boundary: bool = False
    """Whether calls leave the deployment's own cloud account."""

    @property
    def key(self) -> str:
        """The endpoint's catalog-unique key, ``<model_id>@<endpoint>``."""
        return f"{self.model_id}@{self.endpoint}"


@dataclass(frozen=True, slots=True)
class Route:
    """A task class and the models that should serve it."""

    name: str
    """The route's name (a step name serves as that step's default route)."""
    default_model: str
    """The catalog id that serves the route when nothing else decides."""
    preferred: tuple[str, ...] = ()
    """Catalog ids in preference order, after ``default_model``."""
    exemplars: tuple[str, ...] = ()
    """Example utterances a semantic classifier compares a query with."""
    threshold: float = 0.0
    """The classifier confidence below which the step's default route is used."""
    quality_floor: float = 0.0
    """The lowest quality prior a candidate may have for this route."""


@dataclass(frozen=True, slots=True)
class RoutingPolicy:
    """A tenant's (or workspace's) routing rules."""

    mode: str = "user"
    """Who decides: ``user`` (people choose within policy) or ``overlord`` (the planner)."""
    user_override: bool = False
    """In ``overlord`` mode, whether an end user's explicit choice still wins."""
    allow: Mapping[str, tuple[str, ...]] = field(
        default_factory=lambda: MappingProxyType({})
    )
    """Step (or ``*``) → the only catalog ids allowed; absent or empty allows all."""
    deny: Mapping[str, tuple[str, ...]] = field(
        default_factory=lambda: MappingProxyType({})
    )
    """Step (or ``*``) → catalog ids never used."""
    pins: Mapping[str, str] = field(default_factory=lambda: MappingProxyType({}))
    """Step (or ``*``) → the catalog id that serves it directly, skipping the plan."""
    region: str = ""
    """The only region endpoints may run in; empty allows any."""
    zero_retention: bool = False
    """Whether only zero-retention endpoints may serve."""
    allow_off_cloud: bool = False
    """Whether endpoints outside the deployment's cloud account may serve."""
    objective: str = ""
    """The ranker objective when the request names none (``quality``, ``cost``, …)."""
    max_cost_per_request: Decimal | None = None
    """A hard cap on a request's estimated cost, in US dollars."""
    max_latency_ms: int | None = None
    """A hard cap on a candidate's latency prior, in milliseconds."""
    degrade_on_budget: bool = False
    """Whether a near-budget tenant may drop below a route's quality floor."""

    def allowed(self, step: str) -> tuple[str, ...]:
        """Return the ids allowed for ``step`` (its own list plus ``*``); empty allows all."""
        return (*self.allow.get("*", ()), *self.allow.get(step, ()))

    def denied(self, step: str) -> tuple[str, ...]:
        """Return the ids denied for ``step`` (its own list plus ``*``)."""
        return (*self.deny.get("*", ()), *self.deny.get(step, ()))

    def pin(self, step: str) -> str | None:
        """Return the catalog id pinned for ``step`` (a step pin beats ``*``), if any."""
        return self.pins.get(step) or self.pins.get("*")


@dataclass(frozen=True, slots=True)
class RouteRequirements:
    """What one call needs from a model (estimated from the request)."""

    prompt_tokens: int = 0
    """Estimated prompt tokens."""
    max_tokens: int = 0
    """Requested output tokens."""
    streaming: bool = False
    """Whether the call streams."""
    tools: bool = False
    """Whether the call passes tool definitions."""


@dataclass(frozen=True, slots=True)
class RankResult:
    """A ranker's ordering of the filtered candidates."""

    ordered: tuple[CatalogEndpointRef, ...]
    """Candidates, best first."""
    rejections: Mapping[str, str] = field(default_factory=lambda: MappingProxyType({}))
    """Endpoint key → why the ranker dropped it (``over_cap``, ``quality_floor``)."""


@dataclass(frozen=True, slots=True)
class PlanInput:
    """What a planner plans over."""

    query: str
    """The text the classifier reads (the last user message)."""
    candidates: tuple[CatalogEndpointRef, ...]
    """The endpoints left after the filter, in catalog order."""
    requirements: RouteRequirements = field(default_factory=RouteRequirements)
    """What the call needs."""


@dataclass(frozen=True, slots=True)
class Plan:
    """A planner's answer: the endpoints to try, in order, and why."""

    route: str
    """The chosen route's name."""
    confidence: float
    """The classifier's confidence in ``route``."""
    ordered: tuple[CatalogEndpointRef, ...]
    """The endpoints to try, best first."""
    decider: str = "local"
    """Who planned: ``local`` or ``overlord``."""
    reason: str = ""
    """Why (``planned``, ``classifier_degraded``, ``planner_fail_open``, …)."""
    rejections: Mapping[str, str] = field(default_factory=lambda: MappingProxyType({}))
    """Endpoint key → why the planner's ranker dropped it."""
