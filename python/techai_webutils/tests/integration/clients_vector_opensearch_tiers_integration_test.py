"""Integration tests for ``TierAdmin`` against real OpenSearch (testcontainers).

The container has no UltraWarm or cold storage, so the policy runs in ``self_managed`` mode; the
managed actions are pinned by the unit tests.
"""

from __future__ import annotations

import asyncio
import uuid
from typing import TYPE_CHECKING

import pytest
import pytest_asyncio
from opensearchpy import AsyncOpenSearch

from techai_webutils.clients.vector.opensearch.tiers import (
    TierAdmin,
    TierMode,
    TierPolicy,
)
from techai_webutils.core.errors import AppRuntimeError

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

pytestmark = pytest.mark.integration


@pytest_asyncio.fixture
async def client(opensearch_url: str) -> AsyncIterator[AsyncOpenSearch]:
    """Yield a client on the domain."""
    client = AsyncOpenSearch(hosts=[opensearch_url])
    try:
        yield client
    finally:
        await client.close()


@pytest.mark.asyncio
async def test_tier_policy_is_ensured_once_and_attached_only_where_asked(
    client: AsyncOpenSearch,
) -> None:
    """The ISM policy is stored once, rewritten only on change, and manages only attached indices.

    Why this test is important:
        Every ingestion pod ensures the policy at boot, so an unchanged policy must not be
        rewritten. The policy must reach only the retired index it is attached to: a warm
        index is read-only, so a policy that also caught the live index would break its writes.

    What it tests:
        - A second ``ensure_policy`` of the same policy leaves its seq_no; a changed retention
          raises it.
        - ``attach`` hands the retired index to the policy (``managed_by`` names it once ISM
          records it), a repeat ``attach`` succeeds, and the live index stays unmanaged.
        - ``attach`` to an index that does not exist raises ``AppRuntimeError``.
    """
    tag = uuid.uuid4().hex[:8]
    retired, live = f"retired-{tag}", f"live-{tag}"
    for index in (retired, live):
        await client.indices.create(index=index)
    admin = TierAdmin(client, timeout_seconds=30)
    policy = TierPolicy(
        policy_id=f"tiers-{tag}",
        mode=TierMode.SELF_MANAGED,
        warm_after="7d",
        delete_after="90d",
    )
    path = f"/_plugins/_ism/policies/{policy.policy_id}"

    await admin.ensure_policy(policy)
    first = await client.transport.perform_request("GET", path)
    await admin.ensure_policy(policy)
    second = await client.transport.perform_request("GET", path)
    assert second["_seq_no"] == first["_seq_no"]
    await admin.ensure_policy(
        TierPolicy(
            policy_id=policy.policy_id,
            mode=TierMode.SELF_MANAGED,
            warm_after="7d",
            delete_after="30d",
        )
    )
    third = await client.transport.perform_request("GET", path)
    assert third["_seq_no"] > second["_seq_no"]

    await admin.attach(retired, policy.policy_id)
    await admin.attach(retired, policy.policy_id)
    assert await _eventually_managed(admin, retired) == policy.policy_id
    assert await admin.managed_by(live) is None
    with pytest.raises(AppRuntimeError):
        await admin.attach(f"missing-{tag}", policy.policy_id)


async def _eventually_managed(admin: TierAdmin, index: str) -> str | None:
    """Return ``managed_by(index)`` once ISM records the attachment (it is asynchronous)."""
    for _ in range(40):
        if policy := await admin.managed_by(index):
            return policy
        await asyncio.sleep(0.5)
    return None
