"""The ``config`` routing policy store: tenant and workspace policies from configuration.

A lookup is keyed only by the request's own tenant and workspace, so one tenant's pins or
allow-list can never resolve into another's route. A workspace policy replaces its
tenant's; a tenant with no policy gets the default ``RoutingPolicy()`` (people decide, no
pins).
"""

from __future__ import annotations

from dataclasses import dataclass, fields
from decimal import Decimal, InvalidOperation
from enum import StrEnum
from types import MappingProxyType
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.model_gateway.routed.catalog import (
    as_mapping,
    as_strings,
    invalid,
)
from techai_webutils.core.types.routing import RoutingPolicy

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

    from techai_webutils.core.interfaces.routing import RoutingPolicyStore

_POLICY_FIELDS = frozenset(f.name for f in fields(RoutingPolicy))
"""The keys a policy mapping may carry."""
_DEFAULT = RoutingPolicy()
"""The policy of a tenant with none configured."""


class RoutingPolicyStoreKind(StrEnum):
    """The routing policy store backends."""

    CONFIG = "config"
    """Policies from configuration (ESO-projected in staging)."""


def _rules(raw: object, what: str) -> Mapping[str, tuple[str, ...]]:
    """Return a step → ids mapping with tuple values.

    Returns:
        A step → ids mapping with tuple values.

    """
    return MappingProxyType({
        step: as_strings(ids, f"{what} {step}")
        for step, ids in as_mapping(raw, what).items()
    })


def routing_policy_from_mapping(raw: Mapping[str, object]) -> RoutingPolicy:
    """Return the ``RoutingPolicy`` a configuration mapping describes.

    Returns:
        The ``RoutingPolicy`` a configuration mapping describes.

    A callee raises ``AppError``: ``INVALID_INPUT`` for an unknown field or a malformed
    value.

    Raises:
        invalid: Routing policy has unknown fields ``sorted(unknown)``; routing policy
            max_cost_per_request must be a decimal.

    """
    unknown = set(raw) - _POLICY_FIELDS
    if unknown:
        msg = f"routing policy has unknown fields {sorted(unknown)}"
        raise invalid(msg)
    values: dict[str, object] = dict(raw)
    for name in ("allow", "deny"):
        if name in values:
            values[name] = _rules(values[name], f"routing policy {name}")
    if "pins" in values:
        values["pins"] = MappingProxyType({
            str(k): str(v)
            for k, v in as_mapping(values["pins"], "routing policy pins").items()
        })
    if values.get("max_cost_per_request") is not None:
        try:
            values["max_cost_per_request"] = Decimal(str(values["max_cost_per_request"]))
        except InvalidOperation:
            msg = "routing policy max_cost_per_request must be a decimal"
            raise invalid(msg) from None
    return cast("Callable[..., RoutingPolicy]", RoutingPolicy)(**values)


@dataclass(frozen=True, slots=True)
class ConfigPolicyStore:
    """Policies held in memory, loaded from configuration at boot."""

    policies: Mapping[str, RoutingPolicy]
    """Tenant id → its policy."""
    workspace_overrides: Mapping[tuple[str, str], RoutingPolicy]
    """(tenant id, workspace id) → a policy replacing the tenant's."""

    def policy(self, tenant_id: str, workspace_id: str) -> RoutingPolicy:
        """Return the workspace's policy, else the tenant's, else the default.

        Returns:
            The workspace's policy, else the tenant's, else the default.

        """
        override = self.workspace_overrides.get((tenant_id, workspace_id))
        if override is not None:
            return override
        return self.policies.get(tenant_id, _DEFAULT)


def routing_policy_store_from_config(raw: Mapping[str, object]) -> RoutingPolicyStore:
    """Build the policy store ``raw["kind"]`` selects (``config`` by default).

    ``raw`` carries ``policies`` (tenant → policy) and ``workspaces``
    (``tenant/workspace`` → policy).

    Returns:
        The policy store ``raw["kind"]`` selects (``config`` by default).

    A callee raises ``AppError``: ``INVALID_INPUT`` for an unknown kind or a malformed
    policy.

    Raises:
        invalid: Unknown routing policy store kind: ``kind``; workspace policy key ``key``
            must be tenant/workspace.

    """
    kind = str(raw.get("kind", RoutingPolicyStoreKind.CONFIG))
    if kind != RoutingPolicyStoreKind.CONFIG:
        msg = f"unknown routing policy store kind: {kind!r}"
        raise invalid(msg)
    policies = {
        tenant: routing_policy_from_mapping(as_mapping(spec, f"policy {tenant}"))
        for tenant, spec in as_mapping(raw.get("policies", {}), "policies").items()
    }
    overrides: dict[tuple[str, str], RoutingPolicy] = {}
    for key, spec in as_mapping(raw.get("workspaces", {}), "workspaces").items():
        tenant, sep, workspace = key.partition("/")
        if not sep or not tenant or not workspace:
            msg = f"workspace policy key {key!r} must be tenant/workspace"
            raise invalid(msg)
        overrides[tenant, workspace] = routing_policy_from_mapping(
            as_mapping(spec, f"policy {key}")
        )
    return ConfigPolicyStore(
        policies=MappingProxyType(policies),
        workspace_overrides=MappingProxyType(overrides),
    )
