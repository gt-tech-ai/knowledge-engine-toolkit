"""RealRayRuntime: the production RayRuntime backed by an actual Ray cluster.

Imports ``ray`` (the optional ``techai-webutils[ray]`` extra) and is imported lazily by
``executor_from_config`` only when ``ExecutorConfig.kind`` is ``ray`` — so the default
asyncio path never loads Ray. Each ``submit`` schedules one Ray task that runs the async
mapper to completion on a worker via ``asyncio.run`` and awaits the resulting ObjectRef
(Ray ObjectRefs are awaitable in asyncio, giving driver-side concurrency bounded by
``fan_out``'s semaphore).
"""

from __future__ import annotations

import asyncio
import contextlib
import importlib
from dataclasses import dataclass
from typing import TYPE_CHECKING, Protocol, cast, final

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
        """Return whether the default client context has a live connection.

        Returns:
            Whether the Ray Client session is connected.

        """
        ...

    def disconnect(self) -> None:
        """Disconnect every registered client context and empty the registry."""
        ...

    def get_context(self) -> object:
        """Return the calling thread's client context.

        Its ``client_worker`` is the live connection.

        Returns:
            The session's client context.

        """
        ...


class _ClientRegistry(Protocol):  # SDK seam — the untyped ray.util.client module
    """Ray's client-connection registry.

    The API stub plus the registered-context count.
    """

    ray: _ClientApi
    """The process-wide client API stub."""

    def num_connected_contexts(self) -> int:
        """Return how many client contexts are registered, live or not.

        Returns:
            How many client contexts are connected.

        """
        ...


# Cold-connect resiliency defaults (overridable via the constructor). The head's
# per-connection Ray Client server (SpecificServer) can intermittently time out its gRPC
# channel on the FIRST connect from a pod (head-side ``proxier`` "Timeout waiting for
# channel"); retrying recovers it, and once connected the process-global Ray session is
# reused by every later submit. Warm the connection at the caller's startup
# (RealRayRuntime.warm_up) so this flaky first connect happens once, off the live-batch
# path.
_CONNECT_RETRIES = 5
"""Number of cold-connect attempts before giving up on the flaky Ray Client channel."""
_CONNECT_BACKOFF_SECONDS = 3.0
"""Seconds to wait between cold-connect retry attempts."""


def _clear_stale_client() -> None:
    """Drop a Ray Client registration whose connection is gone, so a reconnect is allowed.

    (never raises).

    Ray keeps every client connection in a process-wide registry and removes one only on
    an explicit disconnect. When the head restarts, the connection dies but its entry
    stays:
        ``ray.is_initialized()``
    reports False, yet the next ``ray.init("ray://…")`` is refused ("already connected to
    the cluster with allow_multiple=True") — forever, until the process restarts.
    Disconnecting the stale default context empties the registry, so the next connect goes
    through.
    """
    with contextlib.suppress(Exception):
        client = cast(
            "_ClientRegistry", cast("object", importlib.import_module("ray.util.client"))
        )
        if client.num_connected_contexts() > 0 and not client.ray.is_connected():
            client.ray.disconnect()


def _reset_ray_client() -> None:
    """Tear down the Ray session, whatever state it reports.

    So the next connect starts fresh (never raises).

    Ray Client's ``is_connected()`` stays True after its data channel hits an
    unrecoverable error (a head replacement answers the old client id with NOT_FOUND):
    only the data channel marks itself shut down, not the client worker. So after the
    shutdown, every client context still registered is disconnected unconditionally,
    instead of only the ones that report a lost connection.
    """
    with contextlib.suppress(Exception):
        if ray.is_initialized():
            cast("Callable[[], None]", ray.shutdown)()
    with contextlib.suppress(Exception):
        client = cast(
            "_ClientRegistry", cast("object", importlib.import_module("ray.util.client"))
        )
        if client.num_connected_contexts() > 0:
            client.ray.disconnect()


def _session_token() -> object | None:
    """Return an object that identifies the current Ray Client connection.

    Or None outside client mode.

    Each Ray Client connect builds a new client worker, so its identity changes with every
    reconnect. An object reference is only valid in the session that created it, so a
    cached reference is reused only while this token is unchanged. Never raises.

    Returns:
        An object that identifies the current Ray Client connection.

    """
    with contextlib.suppress(Exception):
        client = cast(
            "_ClientRegistry", cast("object", importlib.import_module("ray.util.client"))
        )
        return cast(
            "object | None", getattr(client.ray.get_context(), "client_worker", None)
        )
    return None


@ray.remote
def _run_mapper(fn: Callable[..., Awaitable[StepResult]], item: object) -> StepResult:
    """Ray task body: run the async mapper to completion on a worker.

    And return its StepResult.

    Args:
        fn: The async mapper.
        item: The item.

    Returns:
        The step result for the item.

    """

    async def _call() -> StepResult:
        return await fn(item)

    return asyncio.run(_call())


class _RemoteTask(Protocol):
    """The typed view of ``_run_mapper`` as a Ray remote function."""

    def options(self, **options: object) -> _RemoteTask:
        """Return the task with Ray scheduling options.

        (``memory``, ``num_cpus``) applied.

        Args:
            **options: Ray's task options.

        Returns:
            The task with the options applied.

        """
        ...

    def remote(self, *args: object) -> Awaitable[StepResult]:
        """Schedule the task and return an awaitable of its result.

        Args:
            *args: The task's arguments.

        Returns:
            An awaitable of the task's result.

        """
        ...


def _ray_init_options(
    worker_setup_hook: str | None, log_encoding: str | None
) -> dict[str, object]:
    """Return the ``ray.init`` options for the hook and log encoding that are set.

    Args:
        worker_setup_hook: The worker setup hook's import path; ``None`` sets none.
        log_encoding: The workers' log encoding; ``None`` keeps Ray's.

    Returns:
        The ``ray.init`` options for the hook and log encoding that are set.

    """
    options: dict[str, object] = {}
    if worker_setup_hook:
        options["runtime_env"] = {"worker_process_setup_hook": worker_setup_hook}
    if log_encoding:
        options["logging_config"] = cast("Callable[..., object]", ray.LoggingConfig)(
            encoding=log_encoding
        )
    return options


def _ray_task_options(memory_bytes: int, num_cpus: float) -> dict[str, object]:
    """Return the Ray task options for the memory and CPUs that are declared.

    Args:
        memory_bytes: Each task's memory reservation; 0 reserves none.
        num_cpus: Each task's CPU reservation; 0 keeps Ray's default.

    Returns:
        The Ray task options for the memory and CPUs that are declared.

    """
    options: dict[str, object] = {}
    if memory_bytes > 0:
        options["memory"] = memory_bytes
    if num_cpus > 0:
        options["num_cpus"] = num_cpus
    return options


@dataclass(frozen=True, kw_only=True)
class RealRayRuntimeOptions:
    """What ``RealRayRuntime`` takes, grouped into one value."""

    worker_setup_hook: str | None = None
    """Dotted path of a no-argument callable Ray runs in every worker process before it
    runs a task or actor (``worker_process_setup_hook``) — where a consumer configures
    logging, since a worker never runs the consumer's entrypoint. ``None`` runs none.
    """

    log_encoding: str | None = None
    """Encoding of Ray's own task and actor logs (``ray.LoggingConfig``: ``TEXT`` or
    ``JSON``); ``None`` keeps Ray's default.
    """

    task_memory_bytes: int = 0
    """Memory Ray reserves for each task (its ``memory`` resource), so it places tasks by
    memory as well as CPU; ``0`` reserves none.
    """

    task_num_cpus: float = 0.0
    """CPUs each task declares (its ``num_cpus``); ``0`` keeps Ray's default of 1."""


@final
class RealRayRuntime:
    """RayRuntime backed by a real Ray cluster (connects on first ``submit``)."""

    def __init__(
        self,
        address: str | None = None,
        namespace: str | None = None,
        *,
        options: RealRayRuntimeOptions,
    ) -> None:
        """Store the connect settings; connection is deferred to the first submit/warm_up.

        Args:
            address: The cluster address; ``None`` starts or joins a local cluster.
            namespace: The Ray namespace; ``None`` uses Ray's default.
            options: The settings and collaborators, grouped.

        """
        self._address = address
        self._namespace = namespace
        self._init_options = _ray_init_options(
            options.worker_setup_hook, options.log_encoding
        )
        self._task_options = _ray_task_options(
            options.task_memory_bytes, options.task_num_cpus
        )
        """Ray ``.options`` for every task: the declared memory and CPUs that are set."""
        # The mapper is put into the object store ONCE per session and reused across a
        # batch's items. ``_cached_fn`` holds a STRONG reference so the identity check
        # (``fn is _cached_fn``) can never be fooled by ``id()`` reuse after a GC — a
        # stale ref would run the wrong mapper.
        self._cached_fn: object | None = None
        self._mapper_ref: object | None = None
        self._mapper_session: object | None = None
        """The session the cached ``_mapper_ref`` was put in; a reconnect invalidates the
        ref."""

    def _forget_session(self) -> None:
        """Drop everything that belongs to the current session: the cached mapper."""
        self._cached_fn = None
        self._mapper_ref = None
        self._mapper_session = None

    async def _reset(self) -> None:
        """Tear the Ray session down and forget this runtime's state in it.

        So the next call reconnects.
        """
        self._forget_session()
        await asyncio.to_thread(_reset_ray_client)

    async def warm_up(self) -> None:
        """Eagerly establish the process-global Ray connection.

        So the first batch skips the cold connect.

        Call once at the caller's startup. Cold-connect resilience (the head's
        per-connection client server can time out its gRPC channel) is supplied by the
        ``ResilientRayRuntime`` DECORATOR wrapped around this runtime at
        ``executor_from_config`` — never inlined here (ARCHITECTURE.md#decorators).

        The session is then proven live with one round trip to the cluster. After a head
        restart, Ray can keep reporting a Ray Client session as initialized and connected
        while its channel is gone (the data channel logs "Unrecoverable error"), so
        trusting ``ray.is_initialized()`` alone would pass a dead session to the batch. A
        failed probe resets the client, so the decorator's next attempt connects afresh.
        """
        await self._ensure_started()
        probed = False
        try:
            _ = await asyncio.to_thread(
                cast("Callable[[], object]", ray.cluster_resources)
            )
            probed = True
        finally:
            if not probed:
                await self._reset()

    async def cluster_cpus(self) -> float:
        """Return the CPUs Ray reports for the cluster (``0.0`` when it reports none).

        Returns:
            The CPUs Ray reports for the cluster (``0.0`` when it reports none).

        """
        await self._ensure_started()
        resources = await asyncio.to_thread(
            cast("Callable[[], dict[str, float]]", ray.cluster_resources)
        )
        return float(resources.get("CPU", 0.0))

    async def _ensure_started(self) -> None:
        """Connect to (or start) the Ray cluster once, idempotently.

        Respects a cluster already initialized out-of-band (a ``ray://`` driver connecting
        to a remote cluster, the startup warm-up, or a test fixture) so we never emit a
        redundant ``ray.init`` and concurrent batches reuse the one process-global
        session. ``ray.init`` is a blocking call, so it runs in a worker thread to avoid
        stalling the driver's event loop during connection; ``ignore_reinit_error`` makes
        a benign concurrent double-init a no-op. Cold-connect retry/backoff is the
        resilience decorator's job (see ``ResilientRayRuntime``), keeping this method a
        single, honest connect.

        A runtime that connected before is re-checked, not trusted: if the session dropped
        (the head restarted), it reconnects. A dead client registration left behind by
        that drop is cleared first, since Ray otherwise refuses every later ``ray.init``
        in the process.
        """
        if ray.is_initialized():
            return
        self._forget_session()
        await asyncio.to_thread(_clear_stale_client)
        connected = False
        try:
            _ = await asyncio.to_thread(
                cast("Callable[..., object]", ray.init),
                address=self._address,
                namespace=self._namespace,
                ignore_reinit_error=True,
                **self._init_options,
            )
            connected = True
        finally:
            if not connected:
                # A failed (or cancelled) cold connect can leave a half-open client;
                # reset it so the resilience decorator's retry gets a clean slate
                # (otherwise ``ray.is_initialized()`` may report the dead session as
                # connected and short-circuit the next attempt). Retry/backoff itself is
                # the decorator's job.
                await self._reset()

    async def submit[T](
        self, fn: Callable[[T], Awaitable[StepResult]], item: T
    ) -> StepResult:
        """Schedule ``fn(item)`` as a Ray task and await its StepResult.

        A failure of the task itself (``RayTaskError``, which Ray also makes an instance
        of the mapper's own exception type) is the item's. Losing the cluster connection
        (a gRPC error or a ``ConnectionError`` from the Ray Client) is the whole batch's:
        the client is reset and ``RayUnavailableError`` is raised, so the caller redrives
        the batch instead of failing each item.

        Args:
            fn: The step.
            item: The item.

        Returns:
            The step result for the item.

        Raises:
            RayTaskError: A task failure that is not recoverable as a result.
            RayUnavailableError: When the connection to the cluster is lost.

        """
        await self._ensure_started()
        try:
            return await self._dispatch(fn, item)
        except RayTaskError:
            raise
        except (grpc.RpcError, ConnectionError) as exc:
            await self._reset()
            msg = f"ray cluster connection lost: {exc}"
            raise RayUnavailableError(msg) from exc

    async def _dispatch[T](
        self, fn: Callable[[T], Awaitable[StepResult]], item: T
    ) -> StepResult:
        """Put the mapper (once per mapper).

        Schedule one task for ``item`` and await its result.

        Args:
            fn: The step.
            item: The item.

        Returns:
            The step result for the item.

        """
        # Put the mapper into the object store ONCE and pass the shared ObjectRef to every
        # task, instead of re-pickling the (identical) mapper per item. fan_out drives the
        # whole batch with one mapper, so an identity check puts it exactly once; Ray
        # caches the object on each worker. The ref is only valid in the session that
        # created it: a reconnect (by this runtime or any other in the process) changes
        # the session token, so the mapper is put again.
        session = _session_token()
        if fn is not self._cached_fn or session is not self._mapper_session:
            self._mapper_ref = cast("Callable[[object], object]", ray.put)(fn)
            self._cached_fn = fn
            self._mapper_session = session
        # The untyped @ray.remote decorator hides `.options`/`.remote` from the type
        # checker, so the task is viewed through the `_RemoteTask` protocol. The mapper
        # arg is a ray ObjectRef; Ray auto-dereferences it, so the task body still
        # receives the real mapper.
        remote = cast("_RemoteTask", cast("object", _run_mapper))
        task = remote.options(**self._task_options) if self._task_options else remote
        return await task.remote(self._mapper_ref, item)
