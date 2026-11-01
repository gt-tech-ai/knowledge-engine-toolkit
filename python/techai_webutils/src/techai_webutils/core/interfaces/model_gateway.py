"""``ModelGateway`` — one home for per-request model selection, failover and cost-aware routing.

A gateway **is** an ``LLMProvider`` (it composes the core contract), so it drops in wherever a
provider is injected; its one extra method, ``await resolve(ctx)``, names the provider that would serve a
``RouteContext``. Request context reaches a gateway through an injected ``RouteContextProvider``;
the toolkit owns no request-scope global.

Every call a gateway serves leaves a ``RouteDecision`` in a request-scoped slot
(``current_route_decision()``), so decorators outside the gateway (audit, tracing, accounting) can
read which model served it — streams included, since a stream yields only text. The slot is an
output: only gateway backends set it.
"""

from __future__ import annotations

from abc import abstractmethod
from collections.abc import Callable, Mapping
from contextvars import ContextVar
from dataclasses import dataclass, field
from types import MappingProxyType
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.llm import LLMProvider

if TYPE_CHECKING:
    from decimal import Decimal


@dataclass(frozen=True, slots=True)
class RouteContext:
    """Who is asking and for what: the inputs to a routing decision."""

    tenant_id: str = ""
    """The organization the request belongs to (keys tenant routing policy)."""
    workspace_id: str = ""
    """The workspace within the tenant (a workspace policy overrides the tenant's)."""
    step: str = ""
    """The pipeline step (``rewrite``, ``generate``, …): each has its own default route."""
    slo: str = ""
    """The objective the ranker optimises (``quality``, ``cost``, ``latency``, ``balanced``);
    empty defers to the tenant policy's objective, then the gateway's default."""
    model_preference: str | None = None
    """The end user's explicit model choice (a catalog id), honoured only within policy."""


@dataclass(frozen=True, slots=True)
class RouteAttempt:
    """One endpoint the executor tried (or skipped) for a call."""

    endpoint: str
    """The endpoint's name."""
    outcome: str
    """``ok``, ``failed`` or ``breaker_open``."""
    error_code: str = ""
    """The ``ErrorCode`` of a failed attempt."""


@dataclass(frozen=True, slots=True)
class RouteDecision:
    """How one call was routed. Every field is defaulted so later stages fill, never reshape it."""

    route: str = ""
    """The task class the classifier chose (``lookup``, ``synthesis``, …)."""
    confidence: float = 0.0
    """The classifier's confidence in ``route``."""
    model_id: str = ""
    """The catalog id (``provider/model``) of the model that served."""
    endpoint: str = ""
    """The endpoint that served."""
    candidates: tuple[str, ...] = ()
    """The ranked endpoints the plan offered, best first."""
    rejections: Mapping[str, str] = field(default_factory=lambda: MappingProxyType({}))
    """Endpoint → why the filter or ranker dropped it (``capability``, ``data_policy``, …)."""
    attempts: tuple[RouteAttempt, ...] = ()
    """What the executor tried, in order."""
    reason: str = ""
    """Why this route (``passthrough``, ``pinned``, ``failover``, ``classifier_degraded``, …)."""
    shadow: bool = False
    """Whether this is a shadow decision (computed, never served)."""
    mode: str = "user"
    """The tenant's routing mode: ``user`` or ``overlord``."""
    decider: str = "local"
    """Who decided: ``user``, ``admin_pin``, ``overlord`` or ``local``."""
    escalated: bool = False
    """Whether the plan escalated to a stronger model."""
    estimated_cost: Decimal | None = None
    """The plan's estimated cost of the call, in US dollars."""
    actual_cost: Decimal | None = None
    """The call's cost from the provider's usage, in US dollars."""
    tokens: int = 0
    """Input plus output tokens the call used."""
    outcome: str = ""
    """How the answer was judged afterwards (filled by later feedback)."""


RouteContextProvider = Callable[[], RouteContext]
"""Returns the current request's ``RouteContext`` (injected; may read request context)."""


_DECISION: ContextVar[RouteDecision | None] = ContextVar("route_decision", default=None)
"""The current request's last route decision (set by gateway backends only)."""


def current_route_decision() -> RouteDecision | None:
    """Return the route decision of the last call a gateway served in this context, if any."""
    return _DECISION.get()


def set_route_decision(decision: RouteDecision) -> None:
    """Record ``decision`` as this context's route decision (gateway backends only)."""
    _DECISION.set(decision)


class ModelGateway(LLMProvider):
    """An ``LLMProvider`` that chooses, per request, which provider serves."""

    @abstractmethod
    async def resolve(self, ctx: RouteContext) -> LLMProvider:
        """Return the provider that would serve ``ctx`` (the first endpoint of its plan).

        Async because planning may consult a classifier, a ledger or a remote planner.
        """
