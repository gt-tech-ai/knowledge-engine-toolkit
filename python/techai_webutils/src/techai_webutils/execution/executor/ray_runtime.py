"""RayRuntime seam: dispatch one work item to a Ray cluster and await its StepResult.

The ``RayRuntime`` protocol carries no ``ray`` import, so ``techai_webutils`` type-checks and
imports without Ray installed; only ``RealRayRuntime`` (a separate module, imported lazily by
``executor_from_config``) touches ``ray``. This is what keeps Ray an optional dependency —
``RayExecutor`` depends on this protocol, never on ``ray`` itself.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import StepResult


@runtime_checkable
class RayRuntime(Protocol):
    """Runs a mapper on a Ray worker for a single item, returning its StepResult.

    The boundary between ``RayExecutor``'s driver-side orchestration and the actual cluster: one
    ``submit`` call is one Ray task, and ``RayExecutor`` bounds how many run at once via ``fan_out``.
    """

    async def submit[T](
        self, fn: Callable[[T], Awaitable[StepResult]], item: T
    ) -> StepResult:
        """Dispatch ``fn(item)`` to the Ray cluster and await its StepResult.

        ``fn`` and ``item`` are cloudpickled to the worker, so both must be picklable — a module-level
        mapper works; a lambda/local closure does not. (The asyncio executor has no such constraint, so
        this is the one behavioural difference a caller must respect when swapping to the Ray backend.)
        """
        ...

    async def warm_up(self) -> None:
        """Eagerly establish the cluster connection (idempotent).

        Called once at the caller's startup so the first live batch never pays the cold-connect cost;
        ``submit`` also connects lazily. The cold-connect can be flaky, so the resilience decorator
        (``ResilientRayRuntime``) retries this with backoff — the concrete runtime just connects once.
        """
        ...
