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
import importlib
from typing import TYPE_CHECKING, Protocol, cast

import grpc
import ray
from ray.exceptions import RayTaskError

from techai_webutils.execution.executor.ray_runtime import RayUnavailableError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import StepResult


class _ClientApi(
    Protocol
):  # SDK seam — ray.util.client.ray (Ray's untyped client API stub)
    """The two calls made on Ray's process-wide client API stub."""

    def is_connected(self) -> bool:
        """Return whether the default client context has a live connection."""
        ...

    def disconnect(self) -> None:
        """Disconnect every registered client context and empty the registry."""
        ...


class _ClientRegistry(Protocol):  # SDK seam — the untyped ray.util.client module
    """Ray's client-connection registry: the API stub plus the registered-context count."""

    ray: _ClientApi
    """The process-wide client API stub."""

    def num_connected_contexts(self) -> int:
        """Return how many client contexts are registered, live or not."""
        ...


# Cold-connect resiliency defaults (overridable via the constructor). The head's per-connection Ray
# Client server (SpecificServer) can intermittently time out its gRPC channel on the FIRST connect from
# a pod (head-side ``proxier`` "Timeout waiting for channel"); retrying recovers it, and once connected
# the process-global Ray session is reused by every later submit. Warm the connection at the caller's
# startup (RealRayRuntime.warm_up) so this flaky first connect happens once, off the live-batch path.
_CONNECT_RETRIES = 5
"""Number of cold-connect attempts before giving up on the flaky Ray Client channel."""
_CONNECT_BACKOFF_SECONDS = 3.0
"""Seconds to wait between cold-connect retry attempts."""


def _clear_stale_client() -> None:
    """Drop a Ray Client registration whose connection is gone, so a reconnect is allowed (never raises).

    Ray keeps every client connection in a process-wide registry and removes one only on an explicit
    disconnect. When the head restarts, the connection dies but its entry stays: ``ray.is_initialized()``
    reports False, yet the next ``ray.init("ray://…")`` is refused ("already connected to the cluster
    with allow_multiple=True") — forever, until the process restarts. Disconnecting the stale default
    context empties the registry, so the next connect goes through.
    """
    with contextlib.suppress(Exception):
        client = cast("_ClientRegistry", importlib.import_module("ray.util.client"))
        if client.num_connected_contexts() > 0 and not client.ray.is_connected():
            client.ray.disconnect()


def _reset_ray_client() -> None:
    """Best-effort teardown of a half-open or stale Ray client between connect retries (never raises)."""
    with contextlib.suppress(Exception):
        if ray.is_initialized():
            cast("Callable[[], None]", ray.shutdown)()
    _clear_stale_client()


@ray.remote
def _run_mapper(fn: Callable[..., Awaitable[StepResult]], item: object) -> StepResult:
    """Ray task body: run the async mapper to completion on a worker and return its StepResult."""

    async def _call() -> StepResult:
        return await fn(item)

    return asyncio.run(_call())


class RealRayRuntime:
    """RayRuntime backed by a real Ray cluster (connects on first ``submit``)."""

    def __init__(
        self,
        address: str | None = None,
        namespace: str | None = None,
        *,
        worker_setup_hook: str | None = None,
        log_encoding: str | None = None,
    ) -> None:
        """Store the connect settings; connection is deferred to the first submit/warm_up.

        Args:
            address: The cluster address; ``None`` starts or joins a local cluster.
            namespace: The Ray namespace; ``None`` uses Ray's default.
            worker_setup_hook: Dotted path of a no-argument callable Ray runs in every worker process
                before it runs a task or actor (``worker_process_setup_hook``) — where a consumer
                configures logging, since a worker never runs the consumer's entrypoint. ``None`` runs none.
            log_encoding: Encoding of Ray's own task and actor logs (``ray.LoggingConfig``: ``TEXT`` or
                ``JSON``); ``None`` keeps Ray's default.

        """
        self._address = address
        self._namespace = namespace
        self._init_options: dict[str, object] = {}
        if worker_setup_hook:
            self._init_options["runtime_env"] = {
                "worker_process_setup_hook": worker_setup_hook
            }
        if log_encoding:
            self._init_options["logging_config"] = cast(
                "Callable[..., object]", ray.LoggingConfig
            )(encoding=log_encoding)
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

        The session is then proven live with one round trip to the cluster. After a head restart, Ray
        can keep reporting a Ray Client session as initialized and connected while its channel is
        gone (the data channel logs "Unrecoverable error"), so trusting ``ray.is_initialized()``
        alone would pass a dead session to the batch. A failed probe resets the client, so the
        decorator's next attempt connects afresh.
        """
        await self._ensure_started()
        try:
            await asyncio.to_thread(cast("Callable[[], object]", ray.cluster_resources))
        except Exception:
            await asyncio.to_thread(_reset_ray_client)
            raise

    async def _ensure_started(self) -> None:
        """Connect to (or start) the Ray cluster once, idempotently.

        Respects a cluster already initialized out-of-band (a ``ray://`` driver connecting to a remote
        cluster, the startup warm-up, or a test fixture) so we never emit a redundant ``ray.init`` and
        concurrent batches reuse the one process-global session. ``ray.init`` is a blocking call, so it
        runs in a worker thread to avoid stalling the driver's event loop during connection;
        ``ignore_reinit_error`` makes a benign concurrent double-init a no-op. Cold-connect
        retry/backoff is the resilience decorator's job (see ``ResilientRayRuntime``), keeping this
        method a single, honest connect.

        A runtime that connected before is re-checked, not trusted: if the session dropped (the head
        restarted), it reconnects. A dead client registration left behind by that drop is cleared first,
        since Ray otherwise refuses every later ``ray.init`` in the process.
        """
        if ray.is_initialized():
            self._started = True
            return
        self._started = False
        await asyncio.to_thread(_clear_stale_client)
        try:
            await asyncio.to_thread(
                cast("Callable[..., object]", ray.init),
                address=self._address,
                namespace=self._namespace,
                ignore_reinit_error=True,
                **self._init_options,
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
        """Schedule ``fn(item)`` as a Ray task and await its StepResult.

        A failure of the task itself (``RayTaskError``, which Ray also makes an instance of the
        mapper's own exception type) is the item's. Losing the cluster connection (a gRPC error or a
        ``ConnectionError`` from the Ray Client) is the whole batch's: the client is reset and
        ``RayUnavailableError`` is raised, so the caller redrives the batch instead of failing each item.

        Raises:
            RayUnavailableError: When the connection to the cluster is lost.

        """
        await self._ensure_started()
        try:
            return await self._dispatch(fn, item)
        except RayTaskError:
            raise
        except (grpc.RpcError, ConnectionError) as exc:
            await asyncio.to_thread(_reset_ray_client)
            msg = f"ray cluster connection lost: {exc}"
            raise RayUnavailableError(msg) from exc

    async def _dispatch[T](
        self, fn: Callable[[T], Awaitable[StepResult]], item: T
    ) -> StepResult:
        """Put the mapper (once per mapper), schedule one task for ``item`` and await its result."""
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
