"""Workflow interfaces for business process orchestration.

Defines the abstract contracts for workflow implementations (sync and async).
Workflows coordinate multi-step operations across services, pipelines, and caches.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


class Workflow[In, Out](ABC):
    """Abstract workflow for synchronous business process orchestration.

    Workflows coordinate multiple operations across services, pipelines, repositories,
    and caches. Unlike pipelines (stateless transforms), workflows orchestrate stateful
    business processes with conditional logic, error recovery, and cross-cutting concerns.

    Examples: Cache-aside pattern (check cache → compute → store), order fulfillment
    (verify → charge → ship → notify).
    """

    @abstractmethod
    def execute(self, input_data: In) -> Out:
        """Orchestrate a multi-step business process.

        Implementations may be stateful and typically depend on services, repos, and caches.
        """
        ...


class AsyncWorkflow[In, Out](ABC):
    """Abstract workflow for asynchronous business process orchestration.

    Async variant of Workflow for orchestrating async operations. This is the primary
    variant for Python services which are typically async.

    Examples: Search workflow (check cache → run pipeline → cache result), ingestion
    workflow (parse → validate → transform → index), notification dispatch.
    """

    @abstractmethod
    async def execute(self, input_data: In) -> Out:
        """Orchestrate a multi-step business process asynchronously.

        Implementations may be stateful and typically depend on async services, repos, and caches.
        """
        ...
