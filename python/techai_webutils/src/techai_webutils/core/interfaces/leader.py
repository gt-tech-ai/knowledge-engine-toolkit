"""Leader-election interface for single-instance execution.

Mirrors Go's ``interfaces.LeaderElector``. Only the elected leader runs the guarded work,
so a job scheduled across N replicas fires once.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable


@runtime_checkable
class LeaderElector(Protocol):
    """Coordinates single-instance execution.

    Implementations: a single-process elector that is always the leader (dev/single-replica),
    or a distributed elector (a Postgres advisory lock, the sibling of ``clients/lock``'s
    postgres backend). Job code depends on this Protocol, never a concrete elector.
    """

    async def is_leader(self) -> bool:
        """Report whether this instance currently holds leadership."""
        ...
