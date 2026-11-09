"""``SnapshotAdmin``: register a snapshot repository, schedule snapshots.

And restore an index.

Amazon OpenSearch Service takes free hourly snapshots into its own ``cs-*`` repository,
but they restore only into the domain that took them and go with it when the domain is
deleted. A domain that must survive its own loss needs a manual repository (an S3 bucket
the service reaches through an IAM role) and a schedule writing to it: ``register`` and
``ensure_schedule`` (a Snapshot Management policy, OpenSearch 2.5+ on the managed service)
set both up idempotently, so a composition root can call them at every boot. ``latest``,
``restore``, ``wait_restored``, ``count`` and ``sample`` are the restore path a drill or a
recovery walks on another domain.

The ``AsyncOpenSearch`` client is injected; ``opensearchpy`` is never imported at run time
here. Every request carries the configured timeout.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast, final

from techai_webutils.core.errors import AppTimeoutError, AppValueError

if TYPE_CHECKING:
    from opensearchpy import AsyncOpenSearch

_NOT_FOUND = 404
"""The status the SM policy lookup tolerates (no policy yet)."""
_SUCCESS = "SUCCESS"
"""The state of a snapshot that completed with every shard."""
_GREEN = "green"
"""The health of an index whose every shard is assigned."""


class RepositoryKind(StrEnum):
    """Where a snapshot repository keeps its files."""

    S3 = "s3"
    """An S3 bucket the domain reaches through ``role_arn`` (Amazon OpenSearch
    Service)."""
    FS = "fs"
    """A shared file system path listed in the node's ``path.repo`` (a test container)."""


@dataclass(frozen=True, slots=True)
class SnapshotRepository:
    """A manual snapshot repository."""

    name: str = ""
    """The repository's name on the domain."""
    kind: RepositoryKind = RepositoryKind.S3
    """Where the repository keeps its files."""
    bucket: str = ""
    """The S3 bucket (``s3``)."""
    base_path: str = ""
    """The key prefix inside the bucket (``s3``; empty for the bucket root)."""
    region: str = ""
    """The bucket's region (``s3``)."""
    role_arn: str = ""
    """The role the service assumes to reach the bucket (``s3``; the registrar needs
    PassRole)."""
    location: str = ""
    """The file system path (``fs``)."""
    readonly: bool = False
    """Register the repository read-only (a domain that only restores from it)."""


@dataclass(frozen=True, slots=True)
class SnapshotSchedule:
    """A Snapshot Management policy writing to one repository."""

    policy: str = ""
    """The policy's name; its snapshots are named ``<policy>-<date>-<suffix>``."""
    cron: str = ""
    """When a snapshot is taken (a five-field cron expression, UTC); it bounds the RPO."""
    indices: str = "*"
    """The indices each snapshot holds (comma-separated, wildcards allowed)."""
    keep: int = 0
    """How many snapshots to keep (the newest); 0 keeps every snapshot."""
    time_limit: str = "1h"
    """How long one snapshot may take before the policy gives up on it."""


@dataclass(frozen=True, slots=True)
class SnapshotInfo:
    """One completed snapshot."""

    name: str
    """The snapshot's name."""
    end_millis: int
    """When the snapshot completed, in milliseconds since the epoch (the restore
    point)."""


@final
class SnapshotAdmin:
    """Snapshot repository, schedule and restore calls on one domain."""

    def __init__(self, client: AsyncOpenSearch, *, timeout_seconds: float = 10.0) -> None:
        """Bind the client; every request carries ``timeout_seconds``."""
        self._client = client
        self._params: dict[str, object] = {"request_timeout": timeout_seconds}

    async def register(self, repo: SnapshotRepository) -> None:
        """Register (or re-register, unchanged) ``repo`` on the domain.

        A callee raises ``AppValueError``: The repository names no name, or misses its
        kind's location.

        """
        await self._client.snapshot.create_repository(
            repository=repo.name, body=_repository_body(repo), params=self._params
        )

    async def ensure_schedule(self, schedule: SnapshotSchedule, repository: str) -> None:
        """Create the SM policy, or update it when its schedule.

        Indices or retention changed.

        A callee raises ``AppValueError``: The schedule names no policy or cron, or a
        negative ``keep``.

        """
        body = _policy_body(schedule, repository)
        path = f"/_plugins/_sm/policies/{schedule.policy}"
        current = cast(
            "dict[str, object]",
            await self._client.transport.perform_request(
                "GET", path, params={**self._params, "ignore": _NOT_FOUND}
            ),
        )
        existing = cast("dict[str, object] | None", current.get("sm_policy"))
        if existing is None:
            await self._client.transport.perform_request(
                "POST", path, params=self._params, body=body
            )
            return
        if _matches(existing, body):
            return
        await self._client.transport.perform_request(
            "PUT",
            path,
            params={
                **self._params,
                "if_seq_no": current["_seq_no"],
                "if_primary_term": current["_primary_term"],
            },
            body=body,
        )

    async def latest(self, repository: str, index: str) -> SnapshotInfo | None:
        """Return the newest successful snapshot in ``repository`` holding ``index``.

        If any.

        Returns:
            The newest successful snapshot in ``repository`` holding ``index``.

        """
        response = cast(
            "dict[str, list[dict[str, object]]]",
            await self._client.snapshot.get(
                repository=repository, snapshot="_all", params=self._params
            ),
        )
        held = [
            SnapshotInfo(str(s["snapshot"]), int(cast("int", s["end_time_in_millis"])))
            for s in response.get("snapshots", [])
            if s.get("state") == _SUCCESS
            and index in cast("list[str]", s.get("indices", []))
        ]
        return max(held, key=lambda s: s.end_millis, default=None)

    async def restore(
        self, repository: str, snapshot: str, index: str, *, as_index: str
    ) -> None:
        """Start restoring ``index`` from ``snapshot`` as ``as_index``.

        With no replicas or aliases.

        Raises:
            AppValueError: ``as_index`` is empty or equals ``index`` (a restore never
                overwrites).

        """
        if not as_index or as_index == index:
            msg = (
                f"snapshot restore needs a new index name, got {as_index!r} for {index!r}"
            )
            raise AppValueError(msg)
        await self._client.snapshot.restore(
            repository=repository,
            snapshot=snapshot,
            body={
                "indices": index,
                "include_global_state": False,
                "include_aliases": False,
                "rename_pattern": "^.+$",
                "rename_replacement": as_index,
                "index_settings": {"index.number_of_replicas": 0},
            },
            params=self._params,
        )

    async def wait_restored(self, index: str, timeout_seconds: float) -> None:
        """Wait until every shard of the restored ``index`` is assigned.

        Raises:
            AppTimeoutError: The index is not green within ``timeout_seconds``.

        """
        response = cast(
            "dict[str, object]",
            await self._client.cluster.health(
                index=index,
                params={
                    "wait_for_status": _GREEN,
                    "timeout": f"{int(timeout_seconds)}s",
                    "request_timeout": timeout_seconds + 10,
                },
            ),
        )
        if response.get("timed_out") or response.get("status") != _GREEN:
            msg = (
                f"{index} not restored within {timeout_seconds:g}s: "
                f"{response.get('status')}"
            )
            raise AppTimeoutError(msg)

    async def count(self, index: str, *, field: str, value: str) -> int:
        """Return how many documents of ``index`` hold ``value`` in ``field``.

        Returns:
            How many documents of ``index`` hold ``value`` in ``field``.

        """
        response = cast(
            "dict[str, int]",
            await self._client.count(
                index=index, body={"query": {"term": {field: value}}}, params=self._params
            ),
        )
        return response["count"]

    async def sample(self, index: str, *, field: str, value: str) -> str | None:
        """Return the id of one document of ``index`` holding ``value`` in ``field``.

        If any.

        Returns:
            The id of one document of ``index`` holding ``value`` in ``field``.

        """
        response = cast(
            "dict[str, dict[str, list[dict[str, object]]]]",
            await self._client.search(
                index=index,
                body={"query": {"term": {field: value}}, "size": 1, "_source": False},
                params=self._params,
            ),
        )
        hits = response["hits"]["hits"]
        return str(hits[0]["_id"]) if hits else None

    async def delete_index(self, index: str) -> None:
        """Delete ``index`` (a restored copy), tolerating one already gone."""
        await self._client.indices.delete(
            index=index, params={**self._params, "ignore_unavailable": "true"}
        )


def _repository_body(repo: SnapshotRepository) -> dict[str, object]:
    """Return the ``PUT _snapshot/<name>`` body for ``repo``.

    Returns:
        The ``PUT _snapshot/<name>`` body for ``repo``.

    Raises:
        AppValueError: As in ``SnapshotAdmin.register``.

    """
    if not repo.name:
        msg = "snapshot repository needs a name"
        raise AppValueError(msg)
    if repo.kind is RepositoryKind.FS:
        if not repo.location:
            msg = f"fs snapshot repository {repo.name} needs a location"
            raise AppValueError(msg)
        settings: dict[str, object] = {"location": repo.location}
    else:
        if not (repo.bucket and repo.region and repo.role_arn):
            msg = (
                f"s3 snapshot repository {repo.name} needs a bucket, region and role_arn"
            )
            raise AppValueError(msg)
        settings = {
            "bucket": repo.bucket,
            "region": repo.region,
            "role_arn": repo.role_arn,
        }
        if repo.base_path:
            settings["base_path"] = repo.base_path
    if repo.readonly:
        settings["readonly"] = True
    return {"type": str(repo.kind), "settings": settings}


def _policy_body(schedule: SnapshotSchedule, repository: str) -> dict[str, object]:
    """Return the SM policy body for ``schedule`` writing to ``repository``.

    Returns:
        The SM policy body for ``schedule`` writing to ``repository``.

    Raises:
        AppValueError: As in ``SnapshotAdmin.ensure_schedule``.

    """
    if not (schedule.policy and schedule.cron and repository):
        msg = "snapshot schedule needs a policy name, a cron expression and a repository"
        raise AppValueError(msg)
    if schedule.keep < 0:
        msg = f"snapshot schedule keep must be >= 0, got {schedule.keep}"
        raise AppValueError(msg)
    cron = {"cron": {"expression": schedule.cron, "timezone": "UTC"}}
    body: dict[str, object] = {
        "description": f"snapshots of {schedule.indices} into {repository}",
        "creation": {"schedule": cron, "time_limit": schedule.time_limit},
        "snapshot_config": {
            "indices": schedule.indices,
            "repository": repository,
            "ignore_unavailable": "true",
            "include_global_state": "false",
            "partial": "false",
        },
    }
    if schedule.keep:
        body["deletion"] = {
            "schedule": cron,
            "condition": {"max_count": schedule.keep, "min_count": 1},
            "time_limit": schedule.time_limit,
        }
    return body


def _matches(existing: dict[str, object], body: dict[str, object]) -> bool:
    """Report whether the stored policy already has ``body``'s schedule.

    Indices and retention.

    Returns:
        Whether the stored policy already matches.

    """
    want_config = cast("dict[str, object]", body["snapshot_config"])
    have_config = cast("dict[str, object]", existing.get("snapshot_config", {}))
    keys = ("indices", "repository", "partial", "include_global_state")
    return (
        existing.get("creation") == body["creation"]
        and existing.get("deletion") == body.get("deletion")
        and all(str(have_config.get(k)) == str(want_config[k]) for k in keys)
    )
