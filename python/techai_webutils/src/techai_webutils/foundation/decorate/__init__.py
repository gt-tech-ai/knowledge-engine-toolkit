"""Type-agnostic decoration of named operations (mirrors Go ``foundation/decorate``).

Every request tier — controllers, clients, workflows, pipelines, services, repositories, jobs —
declares its ``Tier`` (its layers, outermost first) and runs each component's operations through
a ``Subject`` built from the component's ``Deps``. ``Decorations`` builds those Deps from the
``decoration`` config section (``foundation.config.decoration``), so every layer is switched and
tuned by config while its order stays the tier's.
"""

from techai_webutils.foundation.decorate.decorations import (
    Collaborators,
    Decorations,
    build,
)
from techai_webutils.foundation.decorate.middleware import (
    Authorizer,
    OpMiddleware,
    OpThunk,
    chain,
    exec_op,
    new_auth,
    new_bulkhead,
    new_circuit_breaker,
    new_logging,
    new_metrics,
    new_rate_limit,
    new_recovery,
    new_retry,
    new_timeout,
    new_tracing,
)
from techai_webutils.foundation.decorate.scope import Operation, Scope
from techai_webutils.foundation.decorate.tier import (
    Deps,
    Layer,
    Observability,
    Subject,
    Tier,
)

__all__ = [
    "Authorizer",
    "Collaborators",
    "Decorations",
    "Deps",
    "Layer",
    "Observability",
    "OpMiddleware",
    "OpThunk",
    "Operation",
    "Scope",
    "Subject",
    "Tier",
    "build",
    "chain",
    "exec_op",
    "new_auth",
    "new_bulkhead",
    "new_circuit_breaker",
    "new_logging",
    "new_metrics",
    "new_rate_limit",
    "new_recovery",
    "new_retry",
    "new_timeout",
    "new_tracing",
]
