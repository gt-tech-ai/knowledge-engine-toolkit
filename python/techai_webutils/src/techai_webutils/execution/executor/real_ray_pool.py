"""Ray actor-pool backing for PooledExecutor: each PooledWorker slot is a Ray async actor.

Wraps a ray-free ``WorkerFactory`` so ``PooledExecutor``'s pool slots become Ray actors
distributed across the cluster, each building its (expensive) worker context ONCE on its
own persistent event loop and reusing it across every item routed to it — replacing the
per-object client rebuild of the stateless-task path (``RealRayRuntime`` +
``_run_mapper``). Imports ``ray`` (the optional ``techai-webutils[ray]`` extra), so a
caller imports it lazily only when the actor pool is configured; the default asyncio path
never loads it. This mirrors the ``RayExecutor``/``RayRuntime``/``RealRayRuntime`` split:
``PooledExecutor`` + ``PooledWorker`` are ray-free; only this module touches ``ray``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, cast, final

import ray

from techai_webutils.execution.executor.factory import (
    ExecutorConfig,
    ExecutorKind,
    ray_runtime_from_config,
)
from techai_webutils.execution.executor.pooled import LazyWorker

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import StepResult
    from techai_webutils.execution.executor.pooled import PooledWorker, WorkerFactory
    from techai_webutils.execution.executor.ray_runtime import RayRuntime


class _ActorOptions(Protocol):
    """The ``.options`` the ``@ray.remote`` decorator adds to an actor class."""

    remote: Callable[..., object]
    """The actor constructor."""

    def options(self, *, memory: int) -> _ActorOptions:
        """Return the actor class with these resource options.

        Args:
            memory: The actor's memory reservation in bytes.

        Returns:
            The actor class with the options applied.

        """
        ...


class _RemoteMethod[R](Protocol):
    """A Ray actor method: ``.remote(...)`` schedules the call.

    It returns an awaitable ref.
    """

    def remote(self, *args: object) -> Awaitable[R]:
        """Schedule the call on the actor.

        Await the result to get the method's return value.

        Args:
            *args: The method's arguments.

        Returns:
            The call's result, once it finishes.

        """
        ...


class _WorkerActorHandle(Protocol):
    """Typed view of a ``_WorkerActor`` handle.

    Ray's ``@ray.remote`` erases the method types.
    """

    @property
    def process(self) -> _RemoteMethod[StepResult]:
        """The remote ``_WorkerActor.process``."""
        ...

    @property
    def aclose(self) -> _RemoteMethod[None]:
        """The remote ``_WorkerActor.aclose``."""
        ...


_ACTOR_MAX_RESTARTS = 3
"""Times Ray restarts a pool actor whose process died (node preempted or reclaimed, worker
OOM-killed)."""
_ACTOR_MAX_TASK_RETRIES = 3
"""Times Ray re-runs a ``process`` call that was in flight on (or queued for) a pool actor
that died."""


@final
@ray.remote(max_restarts=_ACTOR_MAX_RESTARTS, max_task_retries=_ACTOR_MAX_TASK_RETRIES)
class _WorkerActor:
    """Ray async actor that builds its PooledWorker once and reuses it.

    The worker is built on the first call and reused per item.

    All of an async actor's methods run on one persistent event loop for the actor's
    lifetime, so the worker's loop-bound async clients (aiobotocore S3 sessions, gRPC
    channels) built in ``process`` stay valid across every item — which the stateless-task
    path (``asyncio.run`` per task, a fresh loop each time) cannot do.

    The actor is restartable: if its process dies, Ray recreates it (``__init__`` runs
    again, so the worker is rebuilt on first use) and re-runs the ``process`` calls that
    were lost. Without this, a dead actor stayed dead and every remaining item routed to
    its pool slot failed. ``process`` must therefore tolerate a re-run of the same item —
    the same at-least-once contract a queue consumer already meets.
    """

    def __init__(self, factory: WorkerFactory[object]) -> None:
        """Hold a build-once lazy worker.

        The inner worker is built on the first process call.

        The build-once guard lives in ``LazyWorker`` and is load-bearing here: an async
        Ray actor runs its methods concurrently on one event loop, so without it two
        concurrent first-item calls would each ``await factory()`` (the build yields on
        the S3-session open) and build a SECOND worker, orphaning the first's un-closed
        clients and defeating the reuse.

        Args:
            factory: Builds the worker on first use.

        """
        self._lazy = LazyWorker(factory)

    async def process(self, item: object) -> StepResult:
        """Build the worker once, then process the item on it.

        The worker lives on the actor's persistent loop.

        Args:
            item: The work item.

        Returns:
            The worker once (on the actor's persistent loop).

        """
        worker = await self._lazy.get()
        return await worker.process(item)

    async def aclose(self) -> None:
        """Close the built worker's resources, if any were built.

        Clients and subprocess pools.
        """
        worker = self._lazy.built
        if worker is not None:
            await worker.aclose()


@final
class _ActorProxy:
    """PooledWorker facade over a Ray actor handle: process/aclose become remote calls."""

    def __init__(self, actor: _WorkerActorHandle) -> None:
        """Wrap the Ray actor handle this proxy dispatches to.

        Args:
            actor: The actor's handle.

        """
        self._actor = actor

    async def process(self, item: object) -> StepResult:
        """Dispatch one item to the actor and await its StepResult.

        Ray ObjectRefs are awaitable.

        Args:
            item: The work item.

        Returns:
            The step result for the item.

        """
        return await self._actor.process.remote(item)

    async def aclose(self) -> None:
        """Close the actor's worker, then terminate the actor to free the cluster slot.

        ``ray.kill`` runs in a ``finally`` so a failed graceful close (worker ``aclose``
        raised, or the actor is already unreachable) still frees the actor slot rather
        than leaking it.
        """
        try:
            await self._actor.aclose.remote()
        finally:
            # ``ray.kill``'s own signature carries an unparameterized ActorHandle; the
            # cast is the typed boundary.
            cast("Callable[[_WorkerActorHandle], None]", ray.kill)(self._actor)


def ray_worker_factory[T](
    inner: WorkerFactory[T],
    *,
    address: str | None = None,
    namespace: str | None = None,
    runtime: RayRuntime | None = None,
    memory_bytes: int = 0,
) -> WorkerFactory[T]:
    """Wrap ``inner`` so each ``PooledExecutor`` slot is a Ray actor.

    The returned factory connects through a ``RayRuntime`` before spawning each actor — by
    default the same resilience-decorated runtime ``executor_from_config`` builds for
    ``address``/``namespace`` (``ray_runtime_from_config``), so the actor pool shares the
    stateless path's single-flight retried connect, its stale-client recovery after a head
    restart, and its batch-wide ``RayUnavailableError`` (pass
    ``abort_on=(RayUnavailableError,)`` to ``PooledExecutor`` to abort the batch on it).
    It then spawns one ``_WorkerActor`` per call and returns an ``_ActorProxy`` over it —
    so ``PooledExecutor``'s build-once-per-slot semantics create exactly one actor per
    pool slot, each of which lazily builds ``inner``'s worker on its own event loop.
    ``memory_bytes`` is the memory Ray reserves for each actor (its ``memory`` resource);
    ``0`` reserves none.

    Args:
        inner: Builds the worker each actor holds.
        address: The Ray cluster; ``None`` uses the environment's.
        namespace: The Ray namespace; ``None`` uses the default.
        runtime: Connects to the cluster; ``None`` builds one from ``address``.
        memory_bytes: Each actor's memory reservation; 0 reserves none.

    Returns:
        The factory whose slots are Ray actors.

    """
    connector = runtime or ray_runtime_from_config(
        ExecutorConfig(
            kind=ExecutorKind.RAY,
            ray_address=address or "",
            ray_namespace=namespace or "",
        )
    )

    async def build() -> PooledWorker[T]:
        """Connect, then start one worker actor for a pool slot.

        Returns:
            The worker backed by the new actor.

        """
        await connector.warm_up()
        # `.remote` is the actor constructor injected by the @ray.remote class decorator;
        # its typed signature is erased, so the handle is cast to the typed view — one
        # actor per pool slot.
        actor = cast("_ActorOptions", cast("object", _WorkerActor))
        actor_class = actor.options(memory=memory_bytes) if memory_bytes > 0 else actor
        spawn = cast(
            "Callable[[WorkerFactory[T]], _WorkerActorHandle]", actor_class.remote
        )
        return _ActorProxy(spawn(inner))

    return build
