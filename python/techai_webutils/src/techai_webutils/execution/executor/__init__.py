"""Executor boundary: in-process (AsyncioExecutor) vs distributed (RayExecutor).

The ``Executor`` protocol lives in ``techai_webutils.core.interfaces.execution``; this package
holds its concrete implementations and the config-keyed ``executor_from_config`` factory.
``AsyncioExecutor`` is the in-process default; ``RayExecutor`` (backed by the ``RayRuntime`` seam)
is the distributed backend; ``PooledExecutor`` (backed by the ``PooledWorker`` seam) fans items over
a fixed pool of workers built once each (per-worker client reuse). ``RealRayRuntime`` and
``real_ray_pool`` are intentionally *not* re-exported here — they are the only modules that import the
optional ``ray`` dependency, so they are imported lazily and importing this package never pulls Ray.
"""

from techai_webutils.execution.executor.asyncio_executor import AsyncioExecutor
from techai_webutils.execution.executor.factory import (
    ExecutorConfig,
    ExecutorKind,
    executor_from_config,
)
from techai_webutils.execution.executor.pooled import (
    PooledExecutor,
    PooledWorker,
    WorkerFactory,
)
from techai_webutils.execution.executor.ray_executor import RayExecutor
from techai_webutils.execution.executor.ray_runtime import RayRuntime

__all__ = [
    "AsyncioExecutor",
    "ExecutorConfig",
    "ExecutorKind",
    "PooledExecutor",
    "PooledWorker",
    "RayExecutor",
    "RayRuntime",
    "WorkerFactory",
    "executor_from_config",
]
