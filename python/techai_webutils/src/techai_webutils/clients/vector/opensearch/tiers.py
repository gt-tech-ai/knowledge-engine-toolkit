"""``TierAdmin``: age retired indices through hot, warm and cold tiers, then delete them.

A versioned collection keeps its previous index after an alias swap as the rollback
target. Left alone, that index holds hot-node memory and disk forever. A ``TierPolicy`` is
an Index State Management (ISM) policy rendered from config: ``warm_after``,
``cold_after`` and ``delete_after`` move an index on as it ages, and an empty age skips
the tier.

The policy is attached to one index at a time (``attach``), never through an
``ism_template`` that would match by name, because a warm index is read-only: tiering the
live index a collection writes to would fail every write. A caller attaches the policy to
an index when it retires it. ISM measures each age from the index's creation, not its
retirement, so an index retired after its warm age moves on the next ISM run.

``TierMode`` picks the actions. On Amazon OpenSearch Service (``managed``) warm is
UltraWarm (``warm_migration``) and cold is cold storage (``cold_migration``, which needs a
timestamp field). On a self-managed domain or a test container (``self_managed``) warm
drops replicas and blocks writes, and cold closes the index.

The ``AsyncOpenSearch`` client is injected; ``opensearchpy`` is never imported at run time
here. Every request carries the configured timeout.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.core.errors import AppRuntimeError, AppValueError

if TYPE_CHECKING:
    from opensearchpy import AsyncOpenSearch

_NOT_FOUND = 404
"""The status the policy lookup tolerates (no policy yet)."""
_POLICY_KEY = "index.plugins.index_state_management.policy_id"
"""The explain field naming the ISM policy that manages an index."""
_ALREADY_MANAGED = "already has a policy"
"""The ``add`` refusal for an index some policy already manages."""


class TierMode(StrEnum):
    """Which actions move an index into the warm and cold tiers."""

    MANAGED = "managed"
    """Amazon OpenSearch Service: UltraWarm (``warm_migration``) and cold storage."""
    SELF_MANAGED = "self_managed"
    """Any domain: warm drops replicas and blocks writes, cold closes the index."""


@dataclass(frozen=True, slots=True)
class TierPolicy:
    """An ISM policy that ages an index through its tiers.

    An empty age skips that tier.
    """

    policy_id: str = ""
    """The policy's id on the domain."""
    mode: TierMode = TierMode.MANAGED
    """Which actions the warm and cold tiers run."""
    warm_after: str = ""
    """The index age that moves it to warm (an ISM duration such as ``7d``); empty skips
    warm."""
    cold_after: str = ""
    """The index age that moves it to cold; empty skips cold."""
    delete_after: str = ""
    """The index age that deletes it (the retention); empty keeps it forever."""
    timestamp_field: str = ""
    """The date field ``cold_migration`` reads (``managed`` mode with a cold tier
    only)."""


class TierAdmin:
    """ISM policy calls on one domain."""

    def __init__(self, client: AsyncOpenSearch, *, timeout_seconds: float = 10.0) -> None:
        """Bind the client; every request carries ``timeout_seconds``."""
        self._client = client
        self._params: dict[str, object] = {"request_timeout": timeout_seconds}

    async def ensure_policy(self, policy: TierPolicy) -> None:
        """Create the ISM policy, or update it when its states changed.

        Unchanged is a no-op.

        A callee raises ``AppValueError``: The policy names no id or no tier, or managed
        cold has no timestamp.

        """
        body = render_policy(policy)
        path = f"/_plugins/_ism/policies/{policy.policy_id}"
        current = cast(
            "dict[str, object]",
            await self._client.transport.perform_request(
                "GET", path, params={**self._params, "ignore": _NOT_FOUND}
            ),
        )
        existing = cast("dict[str, object] | None", current.get("policy"))
        params = dict(self._params)
        if existing is not None:
            if _matches(existing, body):
                return
            params |= {
                "if_seq_no": current["_seq_no"],
                "if_primary_term": current["_primary_term"],
            }
        await self._client.transport.perform_request(
            "PUT", path, params=params, body={"policy": body}
        )

    async def attach(self, index: str, policy_id: str) -> None:
        """Hand ``index`` to the policy, so a repeat call, or a retry after a crash.

        Is harmless.

        An index some policy already manages (this one, or an earlier version's) is
        switched to ``policy_id`` through ``change_policy``, which ISM applies once the
        current state's actions finish.

        Raises:
            AppRuntimeError: The domain updated no index: it refused for another reason,
                or ``index`` does not exist (which ISM reports as zero updated, not a
                failure).

        """
        response = await self._ism("add", index, policy_id)
        failed = cast("list[dict[str, object]]", response.get("failed_indices") or [])
        if failed and all(_ALREADY_MANAGED in str(f.get("reason")) for f in failed):
            response = await self._ism("change_policy", index, policy_id)
        if not cast("int", response.get("updated_indices", 0)):
            msg = f"ISM policy {policy_id} not attached to {index}: {response}"
            raise AppRuntimeError(msg)

    async def _ism(self, verb: str, index: str, policy_id: str) -> dict[str, object]:
        """Run the ISM ``verb`` (``add`` or ``change_policy``) on ``index``.

        Return the response.

        Returns:
            The ISM response.

        """
        return cast(
            "dict[str, object]",
            await self._client.transport.perform_request(
                "POST",
                f"/_plugins/_ism/{verb}/{index}",
                params=self._params,
                body={"policy_id": policy_id},
            ),
        )

    async def managed_by(self, index: str) -> str | None:
        """Return the id of the ISM policy managing ``index``, or ``None``.

        ISM records a new attachment asynchronously (about a second on a test container),
        so this reads ``None`` right after an ``attach``; it is a check for operators and
        tests, never a guard before ``attach``.

        Returns:
            The id of the ISM policy managing ``index``, or ``None``.

        """
        response = cast(
            "dict[str, dict[str, object]]",
            await self._client.transport.perform_request(
                "GET", f"/_plugins/_ism/explain/{index}", params=self._params
            ),
        )
        policy = response.get(index, {}).get(_POLICY_KEY)
        return str(policy) if policy else None


def render_policy(policy: TierPolicy) -> dict[str, object]:
    """Return the ISM policy body (the value under ``policy``) for ``policy``.

    Returns:
        The ISM policy body (the value under ``policy``) for ``policy``.

    Raises:
        AppValueError: As in ``TierAdmin.ensure_policy``.

    """
    if not policy.policy_id:
        msg = "ISM tier policy needs a policy id"
        raise AppValueError(msg)
    tiers = [
        (name, age, _actions(policy, name))
        for name, age in (
            ("warm", policy.warm_after),
            ("cold", policy.cold_after),
            ("delete", policy.delete_after),
        )
        if age
    ]
    if not tiers:
        msg = (
            f"ISM tier policy {policy.policy_id} needs at least one of warm, cold or "
            f"delete"
        )
        raise AppValueError(msg)
    states: list[dict[str, object]] = [{"name": "hot", "actions": [], "transitions": []}]
    for name, age, actions in tiers:
        cast("list[object]", states[-1]["transitions"]).append({
            "state_name": name,
            "conditions": {"min_index_age": age},
        })
        states.append({"name": name, "actions": actions, "transitions": []})
    return {
        "description": f"tiers for retired indices ({policy.mode})",
        "default_state": "hot",
        "states": states,
    }


def _actions(policy: TierPolicy, tier: str) -> list[dict[str, object]]:
    """Return the ISM actions entering ``tier`` runs under ``policy.mode``.

    Returns:
        The ISM actions entering ``tier`` runs under ``policy.mode``.

    Raises:
        AppValueError: A managed cold tier has no ``timestamp_field``.

    """
    if tier == "delete":
        return [{"delete": {}}]
    if policy.mode is TierMode.SELF_MANAGED:
        if tier == "warm":
            return [{"replica_count": {"number_of_replicas": 0}}, {"read_only": {}}]
        return [{"close": {}}]
    if tier == "warm":
        return [{"warm_migration": {}}]
    if not policy.timestamp_field:
        msg = f"ISM tier policy {policy.policy_id}: managed cold needs a timestamp_field"
        raise AppValueError(msg)
    return [{"cold_migration": {"timestamp_field": policy.timestamp_field}}]


def _matches(existing: dict[str, object], body: dict[str, object]) -> bool:
    """Report whether the stored policy already has ``body``'s default state and states.

    The domain adds retry settings to each stored action, so actions compare by their type
    and its own settings only.

    Returns:
        Whether the stored policy already matches.

    """
    if existing.get("default_state") != body["default_state"]:
        return False
    have = cast("list[dict[str, object]]", existing.get("states", []))
    want = cast("list[dict[str, object]]", body["states"])
    return len(have) == len(want) and all(
        _state_key(h) == _state_key(w) for h, w in zip(have, want, strict=True)
    )


def _state_key(state: dict[str, object]) -> tuple[object, ...]:
    """Return a state's name, its actions without the domain's defaults.

    And its transitions.

    Returns:
        A state's name, its actions without the domain's defaults.

    """
    actions = cast("list[dict[str, object]]", state.get("actions", []))
    stripped = [
        {k: v for k, v in action.items() if k not in {"retry", "timeout"}}
        for action in actions
    ]
    return (
        state.get("name"),
        repr(stripped),
        repr(state.get("transitions", [])),
    )
