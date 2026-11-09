"""Tests for RealRayRuntime's connect.

Recovery from a Ray Client session that died with the head.

``ray`` is an external SDK, so it is mocked where ``real_ray_runtime`` imports it (``ray``
and the ``importlib`` lookup of ``ray.util.client``). The mocks reproduce Ray's own
client-registry rule (``ray.client_builder.ClientBuilder.connect``): while a client
context is still registered but the default one is no longer connected,
``ray.init("ray://…")`` raises "already connected to the cluster with
allow_multiple=True".
"""

import asyncio
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import grpc
import pytest
import ray as real_ray

from techai_webutils.core.interfaces.execution import StepResult
from techai_webutils.execution.executor.pooled import PooledWorker
from techai_webutils.execution.executor.ray_runtime import RayUnavailableError
from techai_webutils.execution.executor.real_ray_pool import ray_worker_factory
from techai_webutils.execution.executor.real_ray_runtime import (
    RealRayRuntime,
    RealRayRuntimeOptions,
)
from techai_webutils.execution.executor.resilient_ray_runtime import (
    ResilientRayRuntime,
    ResilientRayRuntimeOptions,
)

_EXPECTED_CALL_COUNT = 2

_MODULE = "techai_webutils.execution.executor.real_ray_runtime"
_POOL = "techai_webutils.execution.executor.real_ray_pool"
_ADDRESS = "ray://head:10001"


def _importlib_returning(registry: SimpleNamespace) -> MagicMock:
    """Return an ``importlib`` stand-in.

    For ``real_ray_runtime`` whose ``import_module`` yields ``registry``.

    Replaces only the module's own ``importlib`` name, so imports elsewhere
    (``mock.patch`` resolving its targets) are unaffected.

    Returns:
        An ``importlib`` stand-in.

    """
    return MagicMock(import_module=MagicMock(return_value=registry))


async def _mapper(item: int) -> StepResult:
    """Map nothing: the (mocked) Ray task never runs it here.

    Returns:
        A passing step for the item.

    """
    await asyncio.sleep(0)
    return StepResult(name=str(item))


async def _build_worker() -> PooledWorker[int]:
    """Build nothing: the WorkerFactory shipped to the.

    (mocked) actor is never called here.
    """
    raise NotImplementedError


def _ray_client_after_head_restart() -> tuple[MagicMock, SimpleNamespace]:
    """Return a mocked ``ray`` and client registry.

    In the state a head restart leaves behind.

    One client context stays registered with no live connection. ``ray.init`` follows
    Ray's rule: it refuses while that stale registration exists and connects once it has
    been disconnected.

    Returns:
        A mocked ``ray`` and client registry.

    """
    state = {"registered": 1, "connected": False}

    def disconnect() -> None:
        state["registered"] = 0

    api = MagicMock()
    api.is_connected.side_effect = lambda: state["connected"]
    api.disconnect.side_effect = disconnect
    registry = SimpleNamespace(
        ray=api, num_connected_contexts=lambda: state["registered"]
    )

    def init(**_: object) -> None:
        if state["registered"] and not state["connected"]:
            msg = (
                "The client has already connected to the cluster with "
                "allow_multiple=True. Please set allow_multiple=True to proceed"
            )
            raise ValueError(msg)
        state["registered"], state["connected"] = 1, True

    ray = MagicMock()
    ray.is_initialized.side_effect = lambda: state["connected"]
    ray.init.side_effect = init
    return ray, registry


class TestRealRayRuntimeReconnect:
    """Tests for reconnecting after the Ray head restarted."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_connects_past_a_client_registration_left_by_a_dead_head() -> None:
        """Test that a connect clears a stale Ray Client registration instead.

        Of being refused.

        **Why this test is important:**
          - After the head restarted, every connect in a long-lived ingestion process was
            refused with "already connected … allow_multiple=True" until the pod
            restarted: bulk ingestion failed every item for hours.

        **What it tests:**
          - With one dead client context still registered, ``warm_up`` disconnects it and
            connects: ``ray.init`` is called once, with the runtime's address, and
            succeeds.
        """
        ray, registry = _ray_client_after_head_restart()
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}.importlib", _importlib_returning(registry)),
        ):
            await RealRayRuntime(
                address=_ADDRESS, options=RealRayRuntimeOptions()
            ).warm_up()

        registry.ray.disconnect.assert_called_once_with()
        ray.init.assert_called_once()
        assert ray.init.call_args.kwargs["address"] == _ADDRESS

    @staticmethod
    @pytest.mark.asyncio
    async def test_reconnects_when_an_established_session_dropped() -> None:
        """Test that a runtime that connected before reconnects once its session is gone.

        **Why this test is important:**
          - The runtime built at pod startup lives for the whole process; trusting its
            "connected" flag after the head restarted would skip the reconnect and fail
            every submit.

        **What it tests:**
          - A runtime that connected, then saw ``ray.is_initialized()`` turn False (head
            restarted), calls ``ray.init`` again on the next ``warm_up``.
        """
        ray, registry = _ray_client_after_head_restart()
        registry.ray.disconnect()  # start healthy: no stale registration
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}.importlib", _importlib_returning(registry)),
        ):
            runtime = RealRayRuntime(address=_ADDRESS, options=RealRayRuntimeOptions())
            await runtime.warm_up()
            ray.is_initialized.side_effect = lambda: False  # the head restarted
            ray.init.side_effect = None
            await runtime.warm_up()

        assert ray.init.call_count == _EXPECTED_CALL_COUNT

    @staticmethod
    @pytest.mark.asyncio
    async def test_actor_pool_connects_past_a_dead_head_registration() -> None:
        """Test that the actor-pool path recovers.

        From a dead head's client registration too.

        **Why this test is important:**
          - The actor pool had its own copy of the connect (no retry, no stale-client
            recovery), so the pooled bulk path would stay broken after a head restart even
            with the task path fixed.

        **What it tests:**
          - With a stale client registration left behind, building a pool worker clears
            it, connects to the factory's address, and spawns the actor.
        """
        ray, registry = _ray_client_after_head_restart()
        actor = MagicMock()
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}.importlib", _importlib_returning(registry)),
            patch(f"{_POOL}.ray", ray),
            patch(f"{_POOL}._WorkerActor", actor),
        ):
            build = ray_worker_factory(_build_worker, address=_ADDRESS)
            await build()

        assert ray.init.call_args.kwargs["address"] == _ADDRESS
        actor.remote.assert_called_once_with(_build_worker)


def _ray_client_with_a_dead_session() -> MagicMock:
    """Return a mocked ``ray`` whose client session reports connected while its channel.

    Is gone.

    The state a head restart can leave: ``ray.is_initialized()`` stays True, but every
        call to the
    cluster fails with a gRPC error until ``ray.shutdown`` drops the session and
    ``ray.init`` opens a new one.

    Returns:
        A mocked ``ray`` whose client session reports connected while its channel.

    """
    state = {"initialized": True, "alive": False}

    def cluster_resources() -> dict[str, float]:
        if not state["alive"]:
            raise grpc.RpcError
        return {"CPU": 2.0}

    def shutdown() -> None:
        state["initialized"] = False

    def init(**_: object) -> None:
        state["initialized"], state["alive"] = True, True

    ray = MagicMock()
    ray.is_initialized.side_effect = lambda: state["initialized"]
    ray.cluster_resources.side_effect = cluster_resources
    ray.shutdown.side_effect = shutdown
    ray.init.side_effect = init
    return ray


def _healthy_registry() -> SimpleNamespace:
    """Return a client registry with no stale registration.

    Returns:
        A client registry with no stale registration.

    """
    return SimpleNamespace(ray=MagicMock(), num_connected_contexts=lambda: 0)


class TestRealRayRuntimeDeadSession:
    """Tests for a session Ray still reports as connected after its head restarted."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_warm_up_replaces_a_session_that_reports_connected_but_is_dead() -> (
        None
    ):
        """Test that the batch-start connect proves the session live.

        And reconnects a dead one.

        **Why this test is important:**
          - After a head restart Ray kept reporting the old Ray Client session as
            initialized and connected; a connect that trusted that flag handed every batch
            a dead session.

        **What it tests:**
          - Through the resilience decorator, ``warm_up`` on a dead-but-initialized
            session shuts it down, connects once more with ``ray.init``, and returns with
            a working session.
        """
        ray = _ray_client_with_a_dead_session()
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}.importlib", _importlib_returning(_healthy_registry())),
        ):
            runtime = ResilientRayRuntime(
                RealRayRuntime(address=_ADDRESS, options=RealRayRuntimeOptions()),
                options=ResilientRayRuntimeOptions(base_delay=0.0, max_delay=0.0),
            )
            await runtime.warm_up()

            assert ray.cluster_resources() == {"CPU": 2.0}
        ray.init.assert_called_once()

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_connection_lost_mid_submit_fails_the_batch_not_the_item() -> None:
        """Test that a lost cluster connection during ``submit`` is batch-wide.

        And a task error is not.

        **Why this test is important:**
          - A dead session failed each item on its own, so the batch "completed" with
            every object failed and its message was acknowledged; the batch must be
            redriven instead.

        **What it tests:**
          - A gRPC error scheduling the task raises ``RayUnavailableError`` and shuts the
            session down.
          - A ``RayTaskError`` (the mapper raised, even a ``ConnectionError``) reaches the
            caller as is.
        """
        ray = _ray_client_with_a_dead_session()
        task_error = real_ray.exceptions.RayTaskError(
            function_name="mapper", traceback_str="", cause=ConnectionError("s3")
        ).as_instanceof_cause()

        async def raise_task_error() -> None:
            await asyncio.sleep(0)
            raise task_error

        mapper = MagicMock()
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}._run_mapper", mapper),
        ):
            runtime = RealRayRuntime(address=_ADDRESS, options=RealRayRuntimeOptions())
            mapper.remote.side_effect = grpc.RpcError
            with pytest.raises(RayUnavailableError):
                await runtime.submit(_mapper, 1)
            ray.shutdown.assert_called_once_with()

            ray.is_initialized.side_effect = lambda: True
            mapper.remote.side_effect = lambda *_: raise_task_error()
            with pytest.raises(real_ray.exceptions.RayTaskError):
                await runtime.submit(_mapper, 1)


async def _completed(*_: object) -> StepResult:
    """Stand in for an awaited Ray task that finished.

    Returns:
        A completed step.

    """
    await asyncio.sleep(0)
    return StepResult(name="done")


def _ray_session_across_a_head_replacement() -> tuple[
    MagicMock, SimpleNamespace, SimpleNamespace
]:
    """Return a mocked ``ray``.

    Client registry and client context that model one Ray Client session.

    Each ``ray.init`` builds a new client worker on the context (Ray builds one per
    connect), and ``ray.put`` hands out a new reference per call. The registry's API keeps
    reporting ``is_connected()`` True however the channel fares, as Ray does after a head
    replacement.

    Returns:
        A mocked ``ray``.

    """
    context = SimpleNamespace(client_worker=object())
    state = {"initialized": True}

    def shutdown() -> None:
        state["initialized"] = False

    def disconnect() -> None:
        context.client_worker = None

    def init(**_: object) -> None:
        state["initialized"] = True
        context.client_worker = object()

    api = MagicMock()
    api.is_connected.return_value = True
    api.get_context.return_value = context
    api.disconnect.side_effect = disconnect
    registry = SimpleNamespace(ray=api, num_connected_contexts=lambda: 1)

    ray = MagicMock()
    ray.is_initialized.side_effect = lambda: state["initialized"]
    ray.shutdown.side_effect = shutdown
    ray.init.side_effect = init
    ray.put.side_effect = [f"ref-{n}" for n in range(1, 10)]
    return ray, registry, context


class TestRealRayRuntimeHeadReplacement:
    """Tests for the mapper reference.

    And client teardown across a Ray head replacement.
    """

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_reconnect_puts_the_mapper_again() -> None:
        """Test that after a lost connection the next task carries a mapper put.

        In the new session.

        **Why this test is important:**
          - After a head replacement the runtime reconnected but kept sending the mapper
            reference from the dead session. The new head cannot resolve it, so every task
            failed as an item failure, no reset ever ran again, and ingestion failed every
            batch until the pod was deleted.

        **What it tests:**
          - A submit that loses the connection raises ``RayUnavailableError``; the next
            submit reconnects, puts the mapper again, and schedules the task with the new
            reference.
        """
        ray, registry, _ = _ray_session_across_a_head_replacement()
        mapper = MagicMock()
        mapper.remote.side_effect = _completed
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}.importlib", _importlib_returning(registry)),
            patch(f"{_MODULE}._run_mapper", mapper),
        ):
            runtime = RealRayRuntime(address=_ADDRESS, options=RealRayRuntimeOptions())
            await runtime.submit(_mapper, 1)
            mapper.remote.side_effect = ConnectionError("data channel closed")
            with pytest.raises(RayUnavailableError):
                await runtime.submit(_mapper, 2)
            mapper.remote.side_effect = _completed
            await runtime.submit(_mapper, 3)

        assert ray.put.call_count == _EXPECTED_CALL_COUNT
        assert mapper.remote.call_args.args[0] == "ref-2"

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_lost_connection_disconnects_a_stale_client() -> None:
        """Test that the reset.

        After a lost connection disconnects the client even though it claims to be live.

        **Why this test is important:**
          - Ray Client keeps ``is_connected()`` True after its data channel dies, so a
            teardown that disconnected only clients reporting a lost connection left the
            dead registration in place.

        **What it tests:**
          - A connection lost during ``submit`` disconnects the registered client context
            while its API still reports connected.
        """
        ray, registry, _ = _ray_session_across_a_head_replacement()
        mapper = MagicMock()
        mapper.remote.side_effect = ConnectionError("data channel closed")
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}.importlib", _importlib_returning(registry)),
            patch(f"{_MODULE}._run_mapper", mapper),
            pytest.raises(RayUnavailableError),
        ):
            await RealRayRuntime(
                address=_ADDRESS, options=RealRayRuntimeOptions()
            ).submit(_mapper, 1)

        registry.ray.disconnect.assert_called_once_with()

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_reconnect_by_another_runtime_invalidates_the_cached_mapper() -> None:
        """Test that a mapper reference is reused only within the session that created it.

        **Why this test is important:**
          - Several runtimes share the process's one Ray session; when another one
            reconnects, this runtime's cached reference belongs to the dead session even
            though it saw no failure.

        **What it tests:**
          - With the client connection replaced between two submits, the second submit
            puts the mapper again; with it unchanged, the mapper is put once.
        """
        ray, registry, context = _ray_session_across_a_head_replacement()
        mapper = MagicMock()
        mapper.remote.side_effect = _completed
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}.importlib", _importlib_returning(registry)),
            patch(f"{_MODULE}._run_mapper", mapper),
        ):
            runtime = RealRayRuntime(address=_ADDRESS, options=RealRayRuntimeOptions())
            await runtime.submit(_mapper, 1)
            await runtime.submit(_mapper, 2)
            assert ray.put.call_count == 1
            context.client_worker = object()  # another runtime reconnected
            await runtime.submit(_mapper, 3)

        assert ray.put.call_count == _EXPECTED_CALL_COUNT
        assert mapper.remote.call_args.args[0] == "ref-2"


class TestRealRayRuntimeWorkerLogging:
    """Tests for the logging setup a runtime asks Ray to apply on every worker process."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_connect_installs_the_worker_setup_hook_and_log_encoding() -> None:
        """Test that the configured setup hook and log encoding reach ``ray.init``.

        **Why this test is important:** A Ray worker process unpickles the mapper and runs
        it without ever running the consumer's entrypoint, so its logs keep the library
        defaults (plain text, not the consumer's JSON) unless Ray runs a setup hook in
        each worker first.

        **What it tests:**
        - ``ray.init`` receives ``runtime_env={"worker_process_setup_hook": <hook>}`` and
          a ``LoggingConfig`` with the configured encoding.
        - Without either setting, ``ray.init`` receives neither argument (Ray's defaults
          hold).
        """
        ray = MagicMock()
        ray.is_initialized.return_value = False
        registry = SimpleNamespace(ray=MagicMock(), num_connected_contexts=lambda: 0)
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}.importlib", _importlib_returning(registry)),
        ):
            await RealRayRuntime(
                address=_ADDRESS,
                options=RealRayRuntimeOptions(
                    worker_setup_hook="consumer.logging.configure", log_encoding="JSON"
                ),
            ).warm_up()
            await RealRayRuntime(
                address=_ADDRESS, options=RealRayRuntimeOptions()
            ).warm_up()

        configured, default = (c.kwargs for c in ray.init.call_args_list)
        assert configured["runtime_env"] == {
            "worker_process_setup_hook": "consumer.logging.configure"
        }
        ray.LoggingConfig.assert_called_once_with(encoding="JSON")
        assert configured["logging_config"] is ray.LoggingConfig.return_value
        assert "runtime_env" not in default
        assert "logging_config" not in default


class TestRealRayRuntimeMemory:
    """Tests for the memory a runtime reserves for each Ray task and actor."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_task_reserves_the_configured_memory() -> None:
        """Test that each task asks Ray to reserve the configured memory.

        And none when it is unset.

        **Why this test is important:**
          - With no memory reserved, Ray packs tasks onto a worker by CPU alone, so two
            memory-heavy tasks can share a worker and get it killed together.

        **What it tests:**
          - With ``task_memory_bytes`` set, the task is scheduled through
            ``.options(memory=…)``; with it unset, straight through ``.remote``.
        """
        ray = MagicMock()
        ray.is_initialized.return_value = True
        mapper = MagicMock()
        mapper.options.return_value.remote.side_effect = lambda *_: _mapper(1)
        mapper.remote.side_effect = lambda *_: _mapper(2)
        with (
            patch(f"{_MODULE}.ray", ray),
            patch(f"{_MODULE}._run_mapper", mapper),
        ):
            reserved = await RealRayRuntime(
                address=_ADDRESS, options=RealRayRuntimeOptions(task_memory_bytes=2 << 30)
            ).submit(_mapper, 1)
            unreserved = await RealRayRuntime(
                address=_ADDRESS, options=RealRayRuntimeOptions()
            ).submit(_mapper, 2)

        mapper.options.assert_called_once_with(memory=2 << 30)
        assert (reserved.name, unreserved.name) == ("1", "2")

    @staticmethod
    @pytest.mark.asyncio
    async def test_an_actor_reserves_the_configured_memory() -> None:
        """Test that a pool actor asks Ray to reserve the configured memory.

        **Why this test is important:**
          - A pooled actor runs the same memory-heavy work as a task, for its whole life.

        **What it tests:**
          - ``ray_worker_factory(..., memory_bytes=…)`` spawns the actor through
            ``.options(memory=…)``.
        """
        ray = MagicMock()
        ray.is_initialized.return_value = True
        actor = MagicMock()
        runtime = MagicMock()
        runtime.warm_up = MagicMock(side_effect=lambda: asyncio.sleep(0))
        with patch(f"{_POOL}.ray", ray), patch(f"{_POOL}._WorkerActor", actor):
            build = ray_worker_factory(
                _build_worker, runtime=runtime, memory_bytes=2 << 30
            )
            await build()

        actor.options.assert_called_once_with(memory=2 << 30)
        actor.options.return_value.remote.assert_called_once_with(_build_worker)
