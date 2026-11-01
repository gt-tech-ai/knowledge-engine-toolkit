"""Unit tests for the ``config`` routing policy store."""

from __future__ import annotations

import pytest

from techai_webutils.clients.model_gateway.routed.policy_store import (
    routing_policy_from_mapping,
    routing_policy_store_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.types.routing import RoutingPolicy


def test_workspace_override_beats_tenant_policy() -> None:
    """Test that a workspace policy wins over its tenant's, which wins over the default.

    **Why this test is important:**
      - A workspace can pin a stricter model than its tenant; and a tenant's rules must never
        leak to another tenant (each lookup is keyed only by the request's own ids).

    **What it tests:**
      - ``org-1/ws-a`` gets its workspace pin, ``org-1/ws-b`` the tenant pin
      - ``org-2`` gets the default ``RoutingPolicy()``
      - an unknown store kind or policy field, a non-decimal cost cap, or a workspace key
        without ``tenant/`` raises ``INVALID_INPUT``
    """
    store = routing_policy_store_from_config({
        "kind": "config",
        "policies": {"org-1": {"pins": {"generate": "anthropic/haiku"}, "mode": "user"}},
        "workspaces": {"org-1/ws-a": {"pins": {"generate": "anthropic/sonnet"}}},
    })

    assert store.policy("org-1", "ws-a").pin("generate") == "anthropic/sonnet"
    assert store.policy("org-1", "ws-b").pin("generate") == "anthropic/haiku"
    assert store.policy("org-2", "ws-a") == RoutingPolicy()
    for bad in (
        {"kind": "postgres"},
        {"kind": "config", "policies": {"o": {"colour": 1}}},
    ):
        with pytest.raises(AppError) as caught:
            routing_policy_store_from_config(bad)
        assert caught.value.code is ErrorCode.INVALID_INPUT


def test_routing_policy_from_mapping_coerces_lists_and_money() -> None:
    """Test that a YAML-shaped policy becomes a typed ``RoutingPolicy``.

    **Why this test is important:**
      - Config arrives as lists and strings; the filter compares tuples and ``Decimal`` caps.

    **What it tests:**
      - allow/deny lists become tuples, the cost cap a ``Decimal``, the rest pass through
    """
    policy = routing_policy_from_mapping({
        "allow": {"*": ["anthropic/haiku"]},
        "deny": {"generate": ["meta/small"]},
        "max_cost_per_request": "0.05",
        "region": "us-east-1",
    })

    assert policy.allow == {"*": ("anthropic/haiku",)}
    assert policy.denied("generate") == ("meta/small",)
    assert str(policy.max_cost_per_request) == "0.05"
    assert policy.region == "us-east-1"
