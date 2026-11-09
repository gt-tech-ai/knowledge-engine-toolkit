"""RayRuntime seam: dispatch one work item to a Ray cluster and await its StepResult.

The ``RayRuntime`` protocol carries no ``ray`` import, so ``techai_webutils`` type-checks
and imports without Ray installed; only ``RealRayRuntime`` (a separate module, imported
lazily by ``executor_from_config``) touches ``ray``. This is what keeps Ray an optional
dependency — ``RayExecutor`` depends on this protocol, never on ``ray`` itself.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, runtime_checkable

from techai_webutils.core.errors.errors import UnavailableError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import StepResult


class RayUnavailableError(UnavailableError):
    """The Ray cluster connection itself is down.

    So every item of the batch would fail alike.

    Raised by ``warm_up`` (and the lazy connect inside ``submit``) once the connect
    retries are spent. It is batch-wide, not per-item: ``RayExecutor`` aborts the whole
    batch on it (so a queue consumer redrives the batch) instead of recording one FAIL per
    item. A distinct type, not a plain ``UnavailableError``, because a mapper's own
    ``UnavailableError`` raised on a worker (an S3 or gRPC dependency outage) reaches the
    driver as an instance of that class too, and must stay a per-item failure.
    """


@runtime_checkable
class RayRuntime(Protocol):
    """Runs a mapper on a Ray worker for a single item, returning its StepResult.

    The boundary between ``RayExecutor``'s driver-side orchestration and the actual
    cluster: one ``submit`` call is one Ray task, and ``RayExecutor`` bounds how many run
    at once via ``fan_out``.
    """

    async def submit[T](
        self, fn: Callable[[T], Awaitable[StepResult]], item: T
    ) -> StepResult:
        """Dispatch ``fn(item)`` to the Ray cluster and await its StepResult.

        ``fn`` and ``item`` are cloudpickled to the worker, so both must be picklable — a
        module-level mapper works; a lambda/local closure does not. (The asyncio executor
        has no such constraint, so this is the one behavioural difference a caller must
        respect when swapping to the Ray backend.)
        """
        ...

    async def warm_up(self) -> None:
        """Establish the cluster connection, or confirm it is still up (idempotent).

        Called at the caller's startup so the first live batch never pays the cold-connect
        cost, and by ``RayExecutor`` before each batch; ``submit`` also connects lazily. A
        connection that dropped (the head restarted) is re-established. The cold-connect
        can be flaky, so the resilience decorator (``ResilientRayRuntime``) retries this
        with backoff and raises ``RayUnavailableError`` once the retries are spent — the
        concrete runtime just connects once.
        """
        ...

    async def cluster_cpus(self) -> float:
        """Return the CPUs the cluster reports right now (``0.0`` when it reports none).

        ``RayExecutor.width`` divides it by a task's declared CPUs to bound a batch's
        in-flight tasks by the cluster's schedulable slots.
        """
        ...
