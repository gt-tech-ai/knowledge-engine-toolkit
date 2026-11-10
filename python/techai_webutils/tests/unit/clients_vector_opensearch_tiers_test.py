"""Unit tests for ``render_policy``: the ISM states a ``TierPolicy`` becomes.

The round trip (ensure, attach, idempotency) is proven against OpenSearch in
``tests/integration/clients_vector_opensearch_tiers_integration_test.py``; these pin the
rendered states for the managed mode a test container cannot run, and the refused inputs.
"""

from __future__ import annotations

from typing import cast

import pytest

from techai_webutils.clients.vector.opensearch.tiers import (
    TierMode,
    TierPolicy,
    render_policy,
)
from techai_webutils.core.errors import AppValueError


def test_render_policy_chains_the_configured_tiers_and_refuses_unusable_ones() -> None:
    """A policy becomes hot followed by each configured tier, in order.

    With its mode's actions.

    Why this test is important: The rendered states are what moves a retired index to
        cheaper storage and deletes it at its retention. A skipped tier must not leave a
        dangling transition, the managed actions are UltraWarm and cold storage (which no
        test container offers), and a policy that would do nothing, or a managed cold tier
        without its timestamp field, must fail at boot.

    What it tests:
        - Managed warm, cold and delete: hot → warm (``warm_migration``) → cold
          (``cold_migration`` on the timestamp field) → delete, each entered at its age.
        - Self-managed with no cold tier: hot → warm (no replicas, read-only) → delete.
        - No tier, no id, or managed cold without a timestamp field raise
          ``AppValueError``.
    """
    managed = render_policy(
        TierPolicy(
            policy_id="p",
            warm_after="7d",
            cold_after="30d",
            delete_after="90d",
            timestamp_field="ingested_at",
        )
    )
    states = managed["states"]
    assert managed["default_state"] == "hot"
    assert states == [
        {
            "name": "hot",
            "actions": [],
            "transitions": [
                {"state_name": "warm", "conditions": {"min_index_age": "7d"}}
            ],
        },
        {
            "name": "warm",
            "actions": [{"warm_migration": {}}],
            "transitions": [
                {"state_name": "cold", "conditions": {"min_index_age": "30d"}}
            ],
        },
        {
            "name": "cold",
            "actions": [{"cold_migration": {"timestamp_field": "ingested_at"}}],
            "transitions": [
                {"state_name": "delete", "conditions": {"min_index_age": "90d"}}
            ],
        },
        {"name": "delete", "actions": [{"delete": {}}], "transitions": []},
    ]

    self_managed = render_policy(
        TierPolicy(
            policy_id="p", mode=TierMode.SELF_MANAGED, warm_after="1d", delete_after="9d"
        )
    )
    tiers = cast("list[dict[str, object]]", self_managed["states"])
    assert [s["name"] for s in tiers] == ["hot", "warm", "delete"]
    assert tiers[1]["actions"] == [
        {"replica_count": {"number_of_replicas": 0}},
        {"read_only": {}},
    ]
    assert tiers[1]["transitions"] == [
        {"state_name": "delete", "conditions": {"min_index_age": "9d"}}
    ]

    for bad in (
        TierPolicy(policy_id="p"),
        TierPolicy(warm_after="1d"),
        TierPolicy(policy_id="p", cold_after="1d"),
    ):
        with pytest.raises(AppValueError):
            _ = render_policy(bad)
