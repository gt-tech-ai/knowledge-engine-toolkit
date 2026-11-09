"""Unit tests for ``SnapshotAdmin``'s requests and its reading of responses.

(the client is mocked).

The real snapshot, schedule and restore round trip is proven against OpenSearch in
``tests/integration/clients_vector_opensearch_snapshots_integration_test.py``; these pin
the
decisions made on the responses: when a policy is created, left alone or updated, which
    snapshot
is the newest restore point, and which inputs are refused before any request.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.vector.opensearch.snapshots import (
    RepositoryKind,
    SnapshotAdmin,
    SnapshotInfo,
    SnapshotRepository,
    SnapshotSchedule,
)
from techai_webutils.core.errors import AppTimeoutError, AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping

_SCHEDULE = SnapshotSchedule(policy="hourly", cron="0 * * * *", indices="vec-*", keep=48)


def _client(stored: Mapping[str, object] | None = None) -> MagicMock:
    """Return a mocked client whose SM lookup answers ``stored``.

    (a 404 body when ``None``).
    """
    client = MagicMock()
    lookup = stored if stored is not None else {"status": 404, "error": "not found"}
    client.transport.perform_request = AsyncMock(side_effect=[lookup, {}])
    client.snapshot.create_repository = AsyncMock()
    client.snapshot.restore = AsyncMock()
    return client


def _calls(client: MagicMock) -> list[tuple[str, str, dict[str, object]]]:
    """Return each SM request as ``(method, path, params)``."""
    perform = cast("AsyncMock", client.transport.perform_request)
    return [(c.args[0], c.args[1], c.kwargs["params"]) for c in perform.call_args_list]


@pytest.mark.asyncio
async def test_ensure_schedule_creates_leaves_or_updates_the_policy() -> None:
    """``ensure_schedule`` creates a missing policy, leaves a matching one.

    Updates a changed one.

    Why this test is important: Every ingestion pod calls it at boot. Re-posting an
        existing policy fails (a create is refused for a taken name) and blindly updating
        it would race across pods, so the policy must change only when its schedule or
        retention really did, with optimistic concurrency.

    What it tests:
        - No stored policy: one POST carrying the cron, the indices and ``max_count``
          retention.
        - A stored policy equal to the body: no write at all.
        - A stored policy with another cron: one PUT carrying the stored seq_no and
          primary term.
    """
    created = _client()
    await SnapshotAdmin(created).ensure_schedule(_SCHEDULE, "repo")
    method, path, _ = _calls(created)[1]
    body = cast(
        "dict[str, dict[str, object]]",
        created.transport.perform_request.call_args.kwargs["body"],
    )
    assert (method, path) == ("POST", "/_plugins/_sm/policies/hourly")
    assert body["creation"]["schedule"] == {
        "cron": {"expression": "0 * * * *", "timezone": "UTC"}
    }
    assert body["deletion"]["condition"] == {"max_count": 48, "min_count": 1}
    assert body["snapshot_config"]["indices"] == "vec-*"

    stored = {"_seq_no": 7, "_primary_term": 2, "sm_policy": body}
    unchanged = _client(stored)
    await SnapshotAdmin(unchanged).ensure_schedule(_SCHEDULE, "repo")
    assert [m for m, _, _ in _calls(unchanged)] == ["GET"]

    changed = _client(stored)
    await SnapshotAdmin(changed).ensure_schedule(
        SnapshotSchedule(policy="hourly", cron="*/30 * * * *", indices="vec-*", keep=48),
        "repo",
    )
    method, _, params = _calls(changed)[1]
    assert method == "PUT"
    assert (params["if_seq_no"], params["if_primary_term"]) == (7, 2)


@pytest.mark.asyncio
async def test_latest_is_the_newest_successful_snapshot_holding_the_index() -> None:
    """``latest`` returns the restore point.

    The newest successful snapshot that holds the index.

    Why this test is important: The drill's RPO and the recovery's restore point both come
        from it; a failed or partial snapshot, or one without the index, restores nothing
        useful.

    What it tests:
        - A newer failed snapshot and a newer one without the index are skipped.
        - No matching snapshot yields ``None``.
    """
    client = MagicMock()
    client.snapshot.get = AsyncMock(
        return_value={
            "snapshots": [
                {
                    "snapshot": "a",
                    "state": "SUCCESS",
                    "indices": ["i1"],
                    "end_time_in_millis": 1,
                },
                {
                    "snapshot": "b",
                    "state": "SUCCESS",
                    "indices": ["i1"],
                    "end_time_in_millis": 5,
                },
                {
                    "snapshot": "c",
                    "state": "FAILED",
                    "indices": ["i1"],
                    "end_time_in_millis": 9,
                },
                {
                    "snapshot": "d",
                    "state": "SUCCESS",
                    "indices": ["i2"],
                    "end_time_in_millis": 9,
                },
            ]
        }
    )
    admin = SnapshotAdmin(client)
    assert await admin.latest("repo", "i1") == SnapshotInfo("b", 5)
    assert await admin.latest("repo", "i3") is None


@pytest.mark.asyncio
async def test_refuses_unusable_inputs_before_any_request() -> None:
    """Repositories, schedules and restores that could not work are refused up front.

    Why this test is important: A restore onto its own name would overwrite the source
        index, and a half-configured repository registers but cannot hold a snapshot; both
        must fail before a request.

    What it tests:
        - ``restore`` refuses an empty or identical target name.
        - ``register`` refuses an s3 repository without its role and an fs one without a
          path.
        - ``ensure_schedule`` refuses a missing cron and a negative retention.
        - A restore that stays yellow past its timeout raises ``AppTimeoutError``.
    """
    client = _client()
    admin = SnapshotAdmin(client)
    with pytest.raises(AppValueError):
        await admin.restore("repo", "s", "i1", as_index="i1")
    with pytest.raises(AppValueError):
        await admin.register(SnapshotRepository(name="r", bucket="b", region="us-east-1"))
    with pytest.raises(AppValueError):
        await admin.register(SnapshotRepository(name="r", kind=RepositoryKind.FS))
    with pytest.raises(AppValueError):
        await admin.ensure_schedule(SnapshotSchedule(policy="p"), "repo")
    with pytest.raises(AppValueError):
        await admin.ensure_schedule(
            SnapshotSchedule(policy="p", cron="0 * * * *", keep=-1), "r"
        )
    client.snapshot.restore.assert_not_called()
    client.snapshot.create_repository.assert_not_called()
    client.transport.perform_request.assert_not_called()

    client.cluster.health = AsyncMock(
        return_value={"status": "yellow", "timed_out": True}
    )
    with pytest.raises(AppTimeoutError):
        await admin.wait_restored("restored", 1)
