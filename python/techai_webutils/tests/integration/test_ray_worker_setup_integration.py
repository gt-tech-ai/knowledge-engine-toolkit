"""Integration test: a RealRayRuntime's worker setup hook runs in real Ray worker processes.

A Ray worker never runs the consumer's entrypoint, so the consumer configures logging there through
Ray's ``worker_process_setup_hook``. The unit test proves the hook reaches ``ray.init``; this one
proves Ray resolves the dotted path and runs it in a worker before the worker's first task. It
starts its own local cluster through the runtime (not the shared ``ray_cluster`` fixture, whose
``ray.init`` the runtime would reuse without passing the hook), and shuts it down afterwards.
"""

from __future__ import annotations

import logging
import os
import sys

import pytest

pytest.importorskip("ray")

# Opt-in gate, as in test_ray_executor_integration: a real local Ray cluster can hang on some
# machines, so the module is skipped unless RAY_INTEGRATION is set.
if not os.environ.get("RAY_INTEGRATION"):
    pytest.skip(
        "RAY_INTEGRATION not set — real-cluster Ray integration test skipped "
        "(set RAY_INTEGRATION=1 to run it)",
        allow_module_level=True,
    )

import ray

from techai_webutils.execution.executor.real_ray_runtime import RealRayRuntime


def _root_handler_count() -> int:
    """Return how many handlers the worker process's root logger has."""
    return len(logging.getLogger().handlers)


async def _handlers_in_a_worker(hook: str | None) -> int:
    """Start a local cluster through a runtime with ``hook``, and count a worker's root handlers."""
    await RealRayRuntime(worker_setup_hook=hook, log_encoding="JSON").warm_up()
    # A worker cannot import this test module by name, so its task function ships by value.
    module = sys.modules[__name__]
    ray.cloudpickle.register_pickle_by_value(module)
    try:
        task = ray.remote(_root_handler_count)
        return await task.remote()  # pyright: ignore[reportUnknownMemberType]
    finally:
        ray.cloudpickle.unregister_pickle_by_value(module)
        ray.shutdown()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_the_worker_setup_hook_runs_in_every_ray_worker() -> None:
    """A dotted-path setup hook configures logging in the worker process before its task runs.

    Why this test is important:
      - Without the hook a worker logs with library defaults (plain text that the log pipeline
        cannot parse as JSON); the hook is the only place a consumer can configure a worker.

    What it tests:
      - With ``logging.basicConfig`` as the hook, a worker's root logger has one more handler
        than a worker started without a hook.
    """
    without = await _handlers_in_a_worker(None)
    with_hook = await _handlers_in_a_worker("logging.basicConfig")

    assert with_hook == without + 1
