"""Config-keyed Executor factory: in-process (asyncio) vs distributed (ray) fan-out.

Mirrors the ``foundation/logger`` NewFromConfig pattern — the environment's
``ExecutorConfig`` (from the consumer's config) selects the executor, so distribution is
top-level configuration, not code. The ``ray`` branch imports ``RealRayRuntime`` lazily so
the ``ray`` extra stays optional and the asyncio path never loads it. Unknown kinds fail
loudly (mirroring ``logger.NewFromConfig``).
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.execution.executor.asyncio_executor import AsyncioExecutor
from techai_webutils.execution.executor.ray_executor import RayExecutor

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.execution import ExecutionObserver, Executor
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.execution.executor.resilient_ray_runtime import (
        ResilientRayRuntime,
    )


class ExecutorKind(StrEnum):
    """Which Executor implementation to build."""

    ASYNCIO = "asyncio"
    """In-process fan-out (``AsyncioExecutor``) — the default."""
    RAY = "ray"
    """Distributed fan-out (``RayExecutor`` over a Ray cluster) — for large batches."""


@dataclass(frozen=True, slots=True)
class ExecutorConfig:
    """Executor configuration (the consumer maps its own config section onto it).

    ``kind`` may arrive as a raw YAML string; ``executor_from_config`` coerces it to
    ``ExecutorKind`` and fails loudly on an unrecognized value.
    """

    kind: ExecutorKind | str = ExecutorKind.ASYNCIO
    """Selects the executor (``asyncio`` in-process, ``ray`` distributed)."""
    ray_address: str = ""
    """Ray cluster address; blank ⇒ a local cluster, ``ray://<head-host>:10001`` ⇒ a
    remote cluster."""
    ray_namespace: str = ""
    """Optional Ray namespace to isolate this workload's actors/tasks (blank ⇒ Ray's
    default)."""
    ray_worker_setup_hook: str = ""
    """Ray-only: dotted path of a no-argument callable run in every Ray worker process
    before its tasks (Ray's ``worker_process_setup_hook``) — where the consumer configures
    logging; blank ⇒ none."""
    ray_log_encoding: str = ""
    """Ray-only: encoding of Ray's task and actor logs (``TEXT`` or ``JSON``); blank ⇒
    Ray's default."""
    ray_task_memory_bytes: int = 0
    """Ray-only: memory Ray reserves for each task or pool actor (its ``memory``
    resource), so it places work by memory as well as CPU and a worker never runs more
    than its memory holds; ``0`` ⇒ none."""
    ray_task_num_cpus: float = 0.0
    """Ray-only: CPUs each task declares (its ``num_cpus``, e.g. a parser's
    ``ParseCost.cpu``). Above ``0`` a batch also keeps at most ``floor(cluster CPUs /
    ray_task_num_cpus)`` tasks in flight; ``0`` ⇒ Ray's default of 1 CPU and the caller's
    concurrency unchanged."""
    ray_head_probe_address: str = ""
    """Ray-only: ``host:port`` of the Ray head that ``ResilientRayRuntime.head_reachable``
    probes (e.g. the head service's dashboard port); blank ⇒ the probe refuses, since a
    guessed host would report the wrong head."""
    ray_head_probe_timeout_seconds: float = 2.0
    """Ray-only: ceiling in seconds on one head probe."""
    actor_pool_size: int = 0
    """Ray-only: number of pooled worker actors that reuse clients per worker.

    ``0`` (the default) keeps the stateless-task path (the mapper rebuilds clients per
    object); ``> 0`` selects the actor pool (``PooledExecutor`` over that many actors,
    each built once). Ignored by the ``asyncio`` kind and by ``executor_from_config`` —
    the caller that builds a ``PooledExecutor`` consumes it directly. """
    name: str = "executor"
    """Batch/label name forwarded to ``fan_out`` for the executor's observer + metrics."""


def executor_from_config(
    config: ExecutorConfig,
    observer: ExecutionObserver | None = None,
    *,
    metrics: MetricsProvider | None = None,
) -> Executor:
    """Build the Executor selected by ``config.kind``.

    (asyncio in-process or ray distributed).

    ``metrics`` reaches the ray kind's ``ResilientRayRuntime``
    (``ray_connect_failures_total``).

    Returns:
        The Executor selected by ``config.kind``.

    Raises:
        AppValueError: Unknown executor kind: ``config.kind``.

    """
    try:
        kind = ExecutorKind(config.kind)
    except ValueError as exc:
        msg = f"unknown executor kind: {config.kind!r}"
        raise AppValueError(msg) from exc
    if kind is ExecutorKind.ASYNCIO:
        return AsyncioExecutor(observer, name=config.name)
    # kind is ExecutorKind.RAY — build the resilience-decorated Ray runtime (lazy ray
    # import).
    return RayExecutor(
        ray_runtime_from_config(config, metrics=metrics),
        observer,
        name=config.name,
        task_num_cpus=config.ray_task_num_cpus,
    )


def ray_runtime_from_config(
    config: ExecutorConfig, *, metrics: MetricsProvider | None = None
) -> ResilientRayRuntime:
    """Build the resilience-decorated ``RealRayRuntime`` for the ray kind.

    (lazy ``ray`` import).

    ``ResilientRayRuntime`` wraps ``RealRayRuntime`` so a flaky cold connect is retried
    with backoff (resilience is a decorator, not inlined — ARCHITECTURE.md#decorators).
    Shared by ``executor_from_config`` (per-batch dispatch) and a caller's startup
    warm-up, so both connect through the one resilient path. The concrete
    ``RealRayRuntime`` is imported here so the asyncio path never loads ``ray``.

    Returns:
        The resilience-decorated ``RealRayRuntime`` for the ray kind.

    """
    from techai_webutils.execution.executor.real_ray_runtime import RealRayRuntime  # noqa: PLC0415
    from techai_webutils.execution.executor.resilient_ray_runtime import (  # noqa: PLC0415
        ResilientRayRuntime,
    )

    inner = RealRayRuntime(
        address=config.ray_address or None,
        namespace=config.ray_namespace or None,
        worker_setup_hook=config.ray_worker_setup_hook or None,
        log_encoding=config.ray_log_encoding or None,
        task_memory_bytes=config.ray_task_memory_bytes,
        task_num_cpus=config.ray_task_num_cpus,
    )
    return ResilientRayRuntime(
        inner,
        metrics=metrics,
        head_probe_address=config.ray_head_probe_address or None,
        head_probe_timeout_seconds=config.ray_head_probe_timeout_seconds,
    )
