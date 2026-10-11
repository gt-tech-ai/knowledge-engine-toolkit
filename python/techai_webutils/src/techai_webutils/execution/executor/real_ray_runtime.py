"""RealRayRuntime: the production RayRuntime backed by an actual Ray cluster.

Imports ``ray`` (the optional ``techai-webutils[ray]`` extra) and is imported lazily by
``executor_from_config`` only when ``ExecutorConfig.kind`` is ``ray`` — so the default asyncio path
never loads Ray. Each ``submit`` schedules one Ray task that runs the async mapper to completion on
a worker via ``asyncio.run`` and awaits the resulting ObjectRef (Ray ObjectRefs are awaitable in
asyncio, giving driver-side concurrency bounded by ``fan_out``'s semaphore).
"""

from __future__ import annotations

import asyncio
import contextlib
from typing import TYPE_CHECKING, cast

import ray

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import StepResult

# Cold-connect resiliency defaults (overridable via the constructor). The head's per-connection Ray
# Client server (SpecificServer) can intermittently time out its gRPC channel on the FIRST connect from
# a pod (head-side ``proxier`` "Timeout waiting for channel"); retrying recovers it, and once connected
# the process-global Ray session is reused by every later submit. Warm the connection at the caller's
# startup (RealRayRuntime.warm_up) so this flaky first connect happens once, off the live-batch path.
_CONNECT_RETRIES = 5
"""Number of cold-connect attempts before giving up on the flaky Ray Client channel."""
_CONNECT_BACKOFF_SECONDS = 3.0
"""Seconds to wait between cold-connect retry attempts."""


def _reset_ray_client() -> None:
    """Best-effort teardown of a half-open Ray client between connect retries (never raises)."""
    with contextlib.suppress(Exception):
        if ray.is_initialized():
            cast("Callable[[], None]", ray.shutdown)()


@ray.remote
def _run_mapper(fn: Callable[..., Awaitable[StepResult]], item: object) -> StepResult:
    """Ray task body: run the async mapper to completion on a worker and return its StepResult."""

    async def _call() -> StepResult:
        return await fn(item)

    return asyncio.run(_call())


class RealRayRuntime:
    """RayRuntime backed by a real Ray cluster (connects on first ``submit``)."""

    def __init__(self, address: str | None = None, namespace: str | None = None) -> None:
        """Store the cluster address/namespace; connection is deferred to the first submit/warm_up."""
        self._address = address
        self._namespace = namespace
        self._started = False
        # The mapper is put into the object store ONCE and reused across a batch's items.
        # ``_cached_fn`` holds a STRONG reference so the identity check (``fn is _cached_fn``) can never
        # be fooled by ``id()`` reuse after a GC — a stale ref would run the wrong mapper.
        self._cached_fn: object | None = None
        self._mapper_ref: object | None = None

    async def warm_up(self) -> None:
        """Eagerly establish the process-global Ray connection so the first batch skips the cold connect.

        Call once at the caller's startup. Cold-connect resilience (the head's per-connection client
        server can time out its gRPC channel) is supplied by the ``ResilientRayRuntime`` DECORATOR wrapped
        around this runtime at ``executor_from_config`` — never inlined here (ARCHITECTURE.md#decorators).
        """
        await self._ensure_started()

    async def _ensure_started(self) -> None:
        """Connect to (or start) the Ray cluster once, idempotently.

        Respects a cluster already initialized out-of-band (a ``ray://`` driver connecting to a remote
        cluster, the startup warm-up, or a test fixture) so we never emit a redundant ``ray.init`` and
        concurrent batches reuse the one process-global session. ``ray.init`` is a blocking call, so it
        runs in a worker thread to avoid stalling the driver's event loop during connection;
        ``ignore_reinit_error`` makes a benign concurrent double-init a no-op. Cold-connect
        retry/backoff is the resilience decorator's job (see ``ResilientRayRuntime``), keeping this
        method a single, honest connect.
        """
        if self._started:
            return
        if ray.is_initialized():
            self._started = True
            return
        try:
            await asyncio.to_thread(
                cast("Callable[..., object]", ray.init),
                address=self._address,
                namespace=self._namespace,
                ignore_reinit_error=True,
            )
        except BaseException:
            # A failed cold connect can leave a half-open client; reset it so the resilience decorator's
            # retry gets a clean slate (otherwise ``ray.is_initialized()`` may report the dead session as
            # connected and short-circuit the next attempt). Retry/backoff itself is the decorator's job.
            await asyncio.to_thread(_reset_ray_client)
            raise
        self._started = True

    async def submit[T](
        self, fn: Callable[[T], Awaitable[StepResult]], item: T
    ) -> StepResult:
        """Schedule ``fn(item)`` as a Ray task and await its StepResult."""
        await self._ensure_started()
        # Put the mapper into the object store ONCE and pass the shared ObjectRef to every task, instead
        # of re-pickling the (identical) mapper per item. fan_out drives the whole batch with
        # one mapper, so an identity check puts it exactly once; Ray caches the object on each worker.
        if fn is not self._cached_fn:
            self._mapper_ref = cast("Callable[[object], object]", ray.put)(fn)
            self._cached_fn = fn
        # basedpyright ignores the untyped @ray.remote decorator, so `.remote` is invisible to it (the
        # dev LSP env, where ray is unresolved); the mapper arg is a ray ObjectRef held as `object`
        # (opaque), which the resolved `.remote` signature rejects — Ray auto-dereferences it so the
        # task body still receives the real mapper.
        ref = _run_mapper.remote(  # pyright: ignore[reportFunctionMemberAccess]
            self._mapper_ref,  # pyright: ignore[reportArgumentType]
            item,
        )
        return await ref
