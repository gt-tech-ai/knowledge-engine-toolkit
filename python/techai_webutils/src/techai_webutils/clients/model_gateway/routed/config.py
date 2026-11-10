"""The ``routed`` gateway kind's configuration.

A leaf module: the family's ``builder`` nests ``RoutedConfig`` in ``GatewayConfig``
and the routed gateway builds itself from it, so the config lives where neither
imports the other.
"""

from collections.abc import Mapping
from dataclasses import dataclass, field


def _empty() -> Mapping[str, object]:
    """Return an empty mapping (a stage or store config left at its defaults).

    A plain ``dict``, not a ``MappingProxyType``: configs travel to Ray workers by pickle,
    and a mapping proxy cannot be pickled. The ``Mapping`` annotation keeps it read-only
    to callers.

    Returns:
        An empty mapping (a stage or store config left at its defaults).

    """
    return {}


@dataclass(frozen=True, slots=True)
class RoutedConfig:
    """The ``routed`` kind's configuration: catalog.

    Routes and each stage's raw config.
    """

    catalog: Mapping[str, object] = field(default_factory=_empty)
    """Model id → capabilities, priors and endpoints (validated at boot)."""
    routes: Mapping[str, object] = field(default_factory=_empty)
    """Route name → default model, preferred models and classifier exemplars."""
    default_route: str = ""
    """The route a step with no route of its own uses; empty makes that an error."""
    planner: Mapping[str, object] = field(default_factory=_empty)
    """The planner's config (``kind``: ``local``)."""
    classifier: Mapping[str, object] = field(default_factory=_empty)
    """The classifier's config (``kind``: ``stub``)."""
    ranker: Mapping[str, object] = field(default_factory=_empty)
    """The ranker's config (``kind``: ``static_order``)."""
    policy_store: Mapping[str, object] = field(default_factory=_empty)
    """The policy store's config (``kind``: ``config``; ``policies``, ``workspaces``)."""
    default_slo: str = "quality"
    """The ranking objective when neither the request nor the tenant policy names one."""
    max_attempts: int = 2
    """Most endpoints one call tries (open-breaker skips don't count)."""
    breaker_failures: int = 5
    """Consecutive transient failures that open an endpoint's breaker."""
    breaker_recovery_s: float = 30.0
    """Seconds an open breaker waits before letting a trial call through."""
    sink: Mapping[str, object] = field(default_factory=_empty)
    """The route-decision sink's config (``kind``: ``noop`` or ``analytics``)."""
    shadow: Mapping[str, object] | None = None
    """An alternative ``classifier`` / ``ranker`` / ``planner`` planned alongside the live
    one and never served; ``None`` (the default) runs no shadow."""
