"""Ray actor-pool backing for PooledExecutor: each PooledWorker slot is a Ray async actor.

Wraps a ray-free ``WorkerFactory`` so ``PooledExecutor``'s pool slots become Ray actors distributed
across the cluster, each building its (expensive) worker context ONCE on its own persistent event
loop and reusing it across every item routed to it — replacing the per-object client
rebuild of the stateless-task path (``RealRayRuntime`` + ``_run_mapper``). Imports ``ray`` (the
optional ``techai-webutils[ray]`` extra), so a caller imports it lazily only when the actor pool is
configured; the default asyncio path never loads it. This mirrors the
``RayExecutor``/``RayRuntime``/``RealRayRuntime`` split: ``PooledExecutor`` + ``PooledWorker`` are
ray-free; only this module touches ``ray``.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Any, Protocol, cast

import ray

from techai_webutils.execution.executor.pooled import LazyWorker

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import StepResult
    from techai_webutils.execution.executor.pooled import PooledWorker, WorkerFactory


class _RemoteMethod[R](Protocol):
    """A Ray actor method: ``.remote(...)`` schedules the call and returns an awaitable ref."""

    def remote(self, *args: object) -> Awaitable[R]:
        """Schedule the call on the actor; await the result to get the method's return value."""
        ...


class _WorkerActorHandle(Protocol):
    """Typed view of a ``_WorkerActor`` handle — Ray's ``@ray.remote`` erases the method types."""

    @property
    def process(self) -> _RemoteMethod[StepResult]:
        """The remote ``_WorkerActor.process``."""
        ...

    @property
    def aclose(self) -> _RemoteMethod[None]:
        """The remote ``_WorkerActor.aclose``."""
        ...


@ray.remote
class _WorkerActor:
    """Ray async actor that builds its PooledWorker once (first call) and reuses it per item.

    All of an async actor's methods run on one persistent event loop for the actor's lifetime, so the
    worker's loop-bound async clients (aiobotocore S3 sessions, gRPC channels) built in ``process``
    stay valid across every item — which the stateless-task path (``asyncio.run`` per task, a fresh
    loop each time) cannot do.
    """

    def __init__(self, factory: WorkerFactory[Any]) -> None:
        """Hold a build-once lazy worker; the inner worker is built on the first process call.

        The build-once guard lives in ``LazyWorker`` and is load-bearing here: an async Ray actor runs
        its methods concurrently on one event loop, so without it two concurrent first-item calls would
        each ``await factory()`` (the build yields on the S3-session open) and build a SECOND worker,
        orphaning the first's un-closed clients and defeating the reuse.
        """
        self._lazy = LazyWorker(factory)

    async def process(self, item: object) -> StepResult:
        """Build the worker once (on the actor's persistent loop), then process the item on it."""
        worker = await self._lazy.get()
        return await worker.process(item)

    async def aclose(self) -> None:
        """Close the built worker's resources (clients, subprocess pools), if any were built."""
        worker = self._lazy.built
        if worker is not None:
            await worker.aclose()


class _ActorProxy:
    """PooledWorker facade over a Ray actor handle: process/aclose become remote calls."""

    def __init__(self, actor: _WorkerActorHandle) -> None:
        """Wrap the Ray actor handle this proxy dispatches to."""
        self._actor = actor

    async def process(self, item: object) -> StepResult:
        """Dispatch one item to the actor and await its StepResult (Ray ObjectRefs are awaitable)."""
        return await self._actor.process.remote(item)

    async def aclose(self) -> None:
        """Close the actor's worker, then terminate the actor to free the cluster slot.

        ``ray.kill`` runs in a ``finally`` so a failed graceful close (worker ``aclose`` raised, or the
        actor is already unreachable) still frees the actor slot rather than leaking it.
        """
        try:
            await self._actor.aclose.remote()
        finally:
            # ``ray.kill``'s own signature carries an unparameterized ActorHandle; the cast is the
            # typed boundary.
            cast("Callable[[_WorkerActorHandle], None]", ray.kill)(self._actor)


def ray_worker_factory[T](
    inner: WorkerFactory[T],
    *,
    address: str | None = None,
    namespace: str | None = None,
) -> WorkerFactory[T]:
    """Wrap ``inner`` so each ``PooledExecutor`` slot is a Ray actor (built once, reused per item).

    The returned factory ensures the Ray cluster is connected (idempotently, off the event loop so it
    never stalls the driver) on first use, then spawns one ``_WorkerActor`` per call and returns an
    ``_ActorProxy`` over it — so ``PooledExecutor``'s build-once-per-slot semantics create exactly one
    actor per pool slot, each of which lazily builds ``inner``'s worker on its own event loop.
    """
    started = {"value": False}

    async def build() -> PooledWorker[T]:
        if not started["value"]:
            if not ray.is_initialized():
                await asyncio.to_thread(
                    cast("Callable[..., object]", ray.init),
                    address=address,
                    namespace=namespace,
                    ignore_reinit_error=True,
                )
            started["value"] = True
        # `.remote` is the actor constructor injected by the @ray.remote class decorator; its typed
        # signature is erased, so the handle is cast to the typed view — one actor per pool slot.
        spawn = cast(
            "Callable[[WorkerFactory[T]], _WorkerActorHandle]", _WorkerActor.remote
        )
        return _ActorProxy(spawn(inner))

    return build
