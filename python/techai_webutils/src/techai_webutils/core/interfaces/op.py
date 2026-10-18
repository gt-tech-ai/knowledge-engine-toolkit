"""One operation of a component (mirrors Go ``core/interfaces.Op``).

A component whose verbs are not CRUD exposes them as ``Op`` values beside its role interfaces, so
each verb is decorated, configured and mocked on its own. The tier bases build them:
``RepositoryBase.query`` / ``command``, ``ServiceBase.op``, ``WorkflowBase.read`` / ``command`` /
``saga`` and ``ClientBase.call`` / ``call_once``.
"""

from __future__ import annotations

from typing import Protocol


class Op[In, Out](Protocol):
    """One named operation: a query, command, saga or outbound call."""

    async def execute(self, value: In, /) -> Out:
        """Run the operation on ``value``."""
        ...
