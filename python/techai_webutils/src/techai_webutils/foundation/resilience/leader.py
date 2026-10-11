"""Single-process leader elector.

The default ``LeaderElector`` backend: always the leader (there is only one process).
Mirrors Go's ``foundation/resilience/leader.AlwaysLeader``. The distributed sibling is a
Postgres advisory lock (``clients/lock``'s postgres backend), added when cross-pod
single-writer election is needed.
"""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.leader import LeaderElector


class AlwaysLeader(LeaderElector):
    """A ``LeaderElector`` that always holds leadership (single-process / dev)."""

    @override
    async def is_leader(self) -> bool:
        """Return True unconditionally: a single process is always its own leader."""
        return True
