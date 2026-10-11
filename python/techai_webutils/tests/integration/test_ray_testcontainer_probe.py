"""De-risk probe: a Ray computation runs over a Ray testcontainer via the Ray Jobs API.

It runs reliably, with no ``ray://`` client hang across the macOS/Docker boundary.

Ray deprecated the ``ray://`` client in favor of Ray Jobs; ``ray.init(address="ray://…")`` from a
macOS host hangs indefinitely (see the plan's ## Deviations). This probe validates the chosen
alternative: submit a job to ``rayproject/ray:2.56.1-py312`` via ``JobSubmissionClient`` (HTTP, the
dashboard/jobs port 8265), poll to a terminal ``JobStatus`` with a BOUNDED loop, and assert
``SUCCEEDED``. No ``ray://`` anywhere — the job's ``ray.init()`` runs on the cluster head INSIDE the
Linux container (where local ``ray.init`` is reliable). Skipped (never a hang, never a hard failure)
when Docker or the Ray image is unavailable.
"""

from __future__ import annotations

import time
from typing import TYPE_CHECKING

import pytest

pytest.importorskip("ray")

from ray.job_submission import JobStatus, JobSubmissionClient
from testcontainers.core.container import DockerContainer

if TYPE_CHECKING:
    from collections.abc import Iterator, Mapping

_RAY_IMAGE = "rayproject/ray:2.56.1-py312"
_DASHBOARD_PORT = 8265
# NOTE: do NOT pass --node-ip-address=0.0.0.0 — the head then registers its job agent at an
# unroutable address and the dashboard reports "No available agent to submit job". Letting Ray
# auto-detect the container IP registers the agent correctly. --dashboard-host=0.0.0.0 still makes the
# jobs API reachable from the host via the mapped port.
_RAY_HEAD_CMD = (
    "ray start --head --port=6379 "
    f"--dashboard-host=0.0.0.0 --dashboard-port={_DASHBOARD_PORT} --num-cpus=2 --block"
)
_TERMINAL_STATUSES = {JobStatus.SUCCEEDED, JobStatus.FAILED, JobStatus.STOPPED}


def _ray_image_present() -> bool:
    """Return True if Docker is reachable AND the Ray image is already pulled.

    Skip otherwise: never hang on a cold pull or a missing daemon.
    """
    try:
        from testcontainers.core.docker_client import DockerClient

        DockerClient().client.images.get(_RAY_IMAGE)
    except Exception:
        return False
    return True


pytestmark = pytest.mark.skipif(
    not _ray_image_present(),
    reason=f"Docker or the Ray image {_RAY_IMAGE} is unavailable (pull it to run this probe)",
)


def submit_ray_job(
    client: JobSubmissionClient,
    entrypoint: str,
    *,
    runtime_env: Mapping[str, object] | None = None,
    timeout_s: int = 120,
) -> str:
    """Submit a Ray job, retrying the transient ``No available agent`` 500.

    The head's job agent starts a few seconds after the dashboard API. Bounded — never an
    unbounded hang.
    """
    deadline = time.monotonic() + timeout_s
    last_exc: Exception | None = None
    while time.monotonic() < deadline:
        try:
            return client.submit_job(
                entrypoint=entrypoint, runtime_env=dict(runtime_env or {})
            )
        except RuntimeError as exc:
            if "No available agent" in str(exc):
                last_exc = exc
                time.sleep(2)
                continue
            raise
    msg = f"Ray job submission never succeeded within {timeout_s}s"
    raise RuntimeError(msg) from last_exc


def await_terminal(
    client: JobSubmissionClient, job_id: str, timeout_s: int = 180
) -> JobStatus:
    """Poll a submitted job to a terminal status, bounded — never an unbounded hang."""
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        status = client.get_job_status(job_id)
        if status in _TERMINAL_STATUSES:
            return status
        time.sleep(2)
    msg = f"Ray job {job_id} did not reach a terminal status within {timeout_s}s"
    raise TimeoutError(msg)


@pytest.fixture(scope="module")
def ray_jobs_client() -> Iterator[JobSubmissionClient]:
    """Start a Ray head container and yield a ``JobSubmissionClient`` bound to its jobs API (HTTP 8265)."""
    container = (
        DockerContainer(_RAY_IMAGE)
        .with_command(_RAY_HEAD_CMD)
        .with_exposed_ports(_DASHBOARD_PORT)
        .with_kwargs(shm_size="2gb")
    )
    container.start()
    try:
        host = container.get_container_host_ip()
        port = container.get_exposed_port(_DASHBOARD_PORT)
        address = f"http://{host}:{port}"
        # Bounded retry until the jobs API answers a real HTTP round-trip (never a hang).
        client: JobSubmissionClient | None = None
        last_exc: Exception | None = None
        for _ in range(45):
            try:
                candidate = JobSubmissionClient(address)
                candidate.list_jobs()
                client = candidate
                break
            except Exception as exc:
                last_exc = exc
                time.sleep(2)
        if client is None:
            msg = f"Ray jobs API at {address} never became ready"
            raise RuntimeError(msg) from last_exc
        yield client
    finally:
        container.stop()


@pytest.mark.integration
def test_ray_job_submit_and_poll_over_a_testcontainer(
    ray_jobs_client: JobSubmissionClient,
) -> None:
    """Prove a Ray job submitted to a testcontainer runs to SUCCEEDED via the Jobs API (no ray:// hang).

    Why this test is important:
      - It de-risks the whole Ray-testcontainer approach after ``ray://`` was found to hang on
        macOS/Docker (## Deviations): the Jobs API is HTTP (reliable across the host/container boundary)
        and the job driver's ``ray.init`` runs inside the Linux container. Bounded polling means no hang.

    What it tests:
      - A trivial job (``ray.init()`` on the cluster head + a remote task) reaches
        ``JobStatus.SUCCEEDED`` within the bounded poll window.
    """
    entrypoint = "python -c 'import ray; ray.init(); print(\"ray job ok\", ray.get(ray.remote(lambda: 42).remote()))'"
    job_id = submit_ray_job(ray_jobs_client, entrypoint)

    status = await_terminal(ray_jobs_client, job_id)

    assert status == JobStatus.SUCCEEDED, ray_jobs_client.get_job_logs(job_id)
