"""Async batch fan-out execution substrate.

The Python analog of ``go/execution`` — a foundation-tier peer of
``techai_webutils.foundation`` that depends only on ``techai_webutils.core`` + stdlib +
``asyncio``. Models batch, multi-item fan-out (discover -> map -> fan-out -> aggregate),
distinct from the single-item ``pipelines``/``workflows`` stack.
"""

from techai_webutils.execution.engine.fan_out import fan_out
from techai_webutils.execution.engine.run import run_job_group
from techai_webutils.execution.executor import (
    AsyncioExecutor,
    ExecutorConfig,
    ExecutorKind,
    PooledExecutor,
    PooledWorker,
    RayExecutor,
    RayRuntime,
    WorkerFactory,
    executor_from_config,
)
from techai_webutils.execution.gate import (
    Gate,
    GateBuilder,
    GateFactory,
    Phase,
    new_gate,
    run_gate,
)

__all__ = [
    "AsyncioExecutor",
    "ExecutorConfig",
    "ExecutorKind",
    "Gate",
    "GateBuilder",
    "GateFactory",
    "Phase",
    "PooledExecutor",
    "PooledWorker",
    "RayExecutor",
    "RayRuntime",
    "WorkerFactory",
    "executor_from_config",
    "fan_out",
    "new_gate",
    "run_gate",
    "run_job_group",
]
