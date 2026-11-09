"""Shared fixtures for techai_webutils integration tests.

These tests exercise the thin external-dependency wrappers (Redis cache, SQS
publisher/subscriber, S3 storage client) against REAL services started on demand
via testcontainers — the Python parity to ``go/tests/integration/``. They are
the counterpart to the unit-gate carve-outs in ``pyproject.toml``: the unit suite
excludes these wrappers (their uncovered lines are real network calls); this suite
covers them end-to-end against live dependencies.

Containers use common upstream images (``redis:7-alpine``, ``softwaremill/elasticmq``
for SQS, Chainguard's MinIO for S3, ``postgres:16-alpine``).

All tests are marked ``integration`` and require a Docker daemon. When Docker is
unavailable the whole suite is SKIPPED (not failed), so a developer without Docker
still gets a green local run; the CI integration job provides a daemon (it runs with
``--cov-fail-under=0``; the combined coverage gate in ``.coveragerc.combined`` is an
optional local check).
"""

from __future__ import annotations

import asyncio
import importlib
import logging
import time
import uuid
from pathlib import PurePosixPath
from typing import TYPE_CHECKING
from urllib.parse import urlsplit, urlunsplit

import aiobotocore.session
import asyncpg
import httpx
import pytest
import pytest_asyncio
import ray
from redis.asyncio import Redis
from testcontainers.core.container import DockerContainer
from testcontainers.core.wait_strategies import LogMessageWaitStrategy
from testcontainers.redis import RedisContainer

from techai_webutils.clients.cache.redis import RedisCache
from techai_webutils.clients.cache.types import FailureMode
from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.storage.config import S3Config

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable, Iterator, Mapping

_FAKE_CRED_1 = "test"

_EXPECTED_STATUS = 200

# Container images. ElasticMQ is pinned to a release tag and MinIO by digest;
# redis:7-alpine (and postgres:16-alpine below) are floating major-version tags, so a new
# upstream 7.x/16.x image can change behaviour between runs.
_REDIS_IMAGE = "redis:7-alpine"
_ELASTICMQ_IMAGE = "softwaremill/elasticmq:1.6.6"
# MinIO no longer publishes images: Chainguard's free build, pinned by digest (the Go
# fixture go/tests/fixtures/dbtest/minio pins the same one).
_MINIO_IMAGE = (
    "cgr.dev/chainguard/minio:latest"
    "@sha256:bd014394a80898e68c149f2311fdf8d5a2c2f3bb2c33b9327ae6d02b4b065ae1"
)

# MinIO root credentials (MinIO's well-known local defaults). ElasticMQ ignores creds.
_MINIO_USER = "minioadmin"
_MINIO_ROOT_CRED = "minioadmin"


def _docker_available() -> bool:
    """Return True if a reachable Docker daemon is present.

    Checked lazily (only when an integration test actually runs) so the default
    unit run, which deselects this directory, never pays the probe cost.

    Returns:
        True if a reachable Docker daemon is present.

    """
    try:
        docker_client = importlib.import_module(
            "testcontainers.core.docker_client"
        ).DockerClient
        docker_client().client.ping()
    except Exception:
        return False
    return True


@pytest.fixture(scope="session", autouse=True)
def _require_docker() -> None:
    """Skip the entire integration suite when no Docker daemon is reachable."""
    if not _docker_available():
        pytest.skip("Docker daemon not available — integration tests skipped")


# --------------------------------------------------------------------------- #
# Session-scoped containers (started once, shared across the suite). Tests use   #
# per-test unique keys/queues/buckets to stay isolated despite the shared deps.  #
# --------------------------------------------------------------------------- #


@pytest.fixture(scope="session")
def redis_container() -> Iterator[object]:
    """Start a real Redis server for the session.

    Yields:
        A real Redis server for the session.

    """

    with RedisContainer(_REDIS_IMAGE) as container:
        yield container


@pytest.fixture(scope="session")
def sqs_endpoint() -> Iterator[str]:
    """Start ElasticMQ (SQS-compatible) and yield its mapped HTTP endpoint.

    Yields:
        The mapped endpoint.

    """

    container = (
        DockerContainer(_ELASTICMQ_IMAGE)
        .with_exposed_ports(9324)
        .waiting_for(
            LogMessageWaitStrategy("Started SQS rest server").with_startup_timeout(90)
        )
    )
    container.start()
    try:
        host = container.get_container_host_ip()
        port = int(container.get_exposed_port(9324))
        yield f"http://{host}:{port}"
    finally:
        container.stop()


@pytest.fixture(scope="session")
def minio_endpoint() -> Iterator[str]:
    """Start MinIO (S3-compatible) and yield its mapped HTTP endpoint.

    Yields:
        The mapped endpoint.

    """

    container = (
        DockerContainer(_MINIO_IMAGE)
        .with_env("MINIO_ROOT_USER", _MINIO_USER)
        .with_env("MINIO_ROOT_PASSWORD", _MINIO_ROOT_CRED)
        .with_exposed_ports(9000)
        .with_command("server /data")
        .waiting_for(LogMessageWaitStrategy("API:").with_startup_timeout(90))
    )
    container.start()
    try:
        host = container.get_container_host_ip()
        port = int(container.get_exposed_port(9000))
        yield f"http://{host}:{port}"
    finally:
        container.stop()


# --------------------------------------------------------------------------- #
# Per-test wrapper fixtures (fresh client + fresh queue/bucket each test).       #
# --------------------------------------------------------------------------- #


@pytest_asyncio.fixture
async def redis_cache(redis_container: object) -> AsyncIterator[RedisCache]:
    """Yield a RedisCache bound to the live container, in ERROR mode (no masking).

    Yields:
        A RedisCache bound to the live container, in ERROR mode (no masking).

    """

    host = redis_container.get_container_host_ip()  # type: ignore[attr-defined]
    port = int(redis_container.get_exposed_port(redis_container.port))  # type: ignore[attr-defined]
    client = Redis(host=host, port=port)
    try:
        # ERROR mode so a real connection fault surfaces instead of a silent miss.
        yield RedisCache(client, FailureMode.ERROR)
    finally:
        await client.aclose()


async def _create_sqs_queue(
    endpoint: str, name: str, attributes: Mapping[str, str] | None = None
) -> str:
    """Create a queue (with ``attributes``, e.g. a redrive policy) and return its URL.

    The URL's host is the mapped endpoint.

    ElasticMQ echoes its internal node-address host in the returned QueueUrl, which
    does not match the randomly-mapped host port; rewrite the host/port to the test
    endpoint while preserving the queue path so aiobotocore targets the container.

    Returns:
        A queue (with ``attributes``, e.g. a redrive policy) and return its URL.

    """
    session = aiobotocore.session.get_session()
    async with session.create_client(
        "sqs",
        endpoint_url=endpoint,
        region_name="us-east-1",
        aws_access_key_id="test",
        aws_secret_access_key=_FAKE_CRED_1,
    ) as client:
        resp = await client.create_queue(
            QueueName=name, Attributes=dict(attributes or {})
        )
    endpoint_parts = urlsplit(endpoint)
    queue_parts = urlsplit(resp["QueueUrl"])
    return urlunsplit((
        endpoint_parts.scheme,
        endpoint_parts.netloc,
        queue_parts.path,
        "",
        "",
    ))


@pytest_asyncio.fixture
async def sqs_config(sqs_endpoint: str) -> SQSConfig:
    """Return a fresh SQS queue (unique per test) wrapped in an SQSConfig.

    Returns:
        A fresh SQS queue (unique per test) wrapped in an SQSConfig.

    """
    queue_url = await _create_sqs_queue(sqs_endpoint, f"it-{uuid.uuid4().hex[:12]}")
    return SQSConfig(
        endpoint=sqs_endpoint,
        region="us-east-1",
        queue_url=queue_url,
        access_key="test",
        secret_key=_FAKE_CRED_1,
        max_messages=10,
        # Short long-poll so the subscriber loop returns promptly in tests.
        wait_time_seconds=1,
    )


@pytest.fixture
def make_sqs_queue(
    sqs_endpoint: str,
) -> Callable[[str, Mapping[str, str] | None], Awaitable[str]]:
    """Return a coroutine that creates queue ``name`` with ``attributes``.

    And returns its URL.

    Callers pass unique names; a test needing a dead-letter queue creates it first, then
    the source queue with a ``RedrivePolicy`` naming it.

    Returns:
        A coroutine that creates queue ``name`` with ``attributes``.

    """

    async def make(name: str, attributes: Mapping[str, str] | None = None) -> str:
        return await _create_sqs_queue(sqs_endpoint, name, attributes)

    return make


@pytest_asyncio.fixture
async def s3_config(minio_endpoint: str) -> S3Config:
    """Return a fresh MinIO bucket (unique per test) wrapped in an S3Config.

    Returns:
        A fresh MinIO bucket (unique per test) wrapped in an S3Config.

    """
    bucket = f"it-{uuid.uuid4().hex[:12]}"
    session = aiobotocore.session.get_session()
    async with session.create_client(
        "s3",
        endpoint_url=minio_endpoint,
        region_name="us-east-1",
        aws_access_key_id=_MINIO_USER,
        aws_secret_access_key=_MINIO_ROOT_CRED,
    ) as client:
        await client.create_bucket(Bucket=bucket)
    return S3Config(
        endpoint=minio_endpoint,
        bucket=bucket,
        region="us-east-1",
        access_key=_MINIO_USER,
        secret_key=_MINIO_ROOT_CRED,
    )


# --------------------------------------------------------------------------- #
# Postgres advisory-lock integration (real Postgres via testcontainers).        #
# ``postgres:16-alpine`` with throwaway test credentials.                        #
# --------------------------------------------------------------------------- #

_POSTGRES_IMAGE = "postgres:16-alpine"


async def _await_pg_ready(dsn: str) -> None:
    """Poll until a real TCP connection succeeds.

    (postgres logs 'ready' during init before TCP is up).

    Raises:
        RuntimeError: Postgres did not become ready in time.

    """
    for _ in range(60):
        try:
            conn = await asyncpg.connect(dsn)
            await conn.close()
            return
        except (OSError, asyncpg.PostgresError):
            await asyncio.sleep(0.5)
    msg = "postgres did not become ready in time"
    raise RuntimeError(msg)


@pytest.fixture(scope="module")
def postgres_dsn() -> Iterator[str]:
    """Start a real Postgres for the module and yield a libpq DSN for it.

    Yields:
        A real Postgres for the module and yield a libpq DSN for it.

    """

    container = (
        DockerContainer(_POSTGRES_IMAGE)
        .with_env("POSTGRES_USER", "app")
        .with_env("POSTGRES_PASSWORD", "dev_password")
        .with_env("POSTGRES_DB", "knowledge_engine")
        .with_exposed_ports(5432)
        .waiting_for(
            LogMessageWaitStrategy(
                "database system is ready to accept connections"
            ).with_startup_timeout(60)
        )
    )
    container.start()
    try:
        host = container.get_container_host_ip()
        port = int(container.get_exposed_port(5432))
        dsn = f"postgresql://app:dev_password@{host}:{port}/knowledge_engine?sslmode=disable"
        asyncio.run(_await_pg_ready(dsn))
        yield dsn
    finally:
        container.stop()


# --------------------------------------------------------------------------- #
# Real-Ray executor integration (opt-in; requires the ``ray`` extra + a live     #
# cluster). ``ray`` is imported lazily INSIDE the fixture so this shared         #
# conftest still imports for the non-Ray integration tests when the extra is     #
# absent; the Ray test module skips itself before the fixture is ever requested. #
# --------------------------------------------------------------------------- #


@pytest.fixture(scope="module")
def ray_cluster(request: pytest.FixtureRequest) -> object:
    """Start a small local Ray cluster ONCE for the module and pickle its mappers/workers.

    By value.

    Module-scoped so the two integration tests share a single
    ``ray.init``/``ray.shutdown`` (cluster spin-up dominates the runtime) rather than
    paying it twice. Ray workers are separate processes that cannot import a pytest-loaded
    test module by name, so the mapper/worker factory must be shipped *by value* via
    cloudpickle — the same requirement production mappers meet by living in an installed
    package. (The requesting test module skips itself unless ``RAY_INTEGRATION`` is set —
    ray itself is importable in CI, since the dev dependency group pins it — and also via
    ``importorskip`` where ray is not installed at all, so it adds nothing to the CI or
    default-local pipeline.)

    ``request.module`` is the requesting test module (where the picklable mappers/workers
    are defined), so registering it — rather than this conftest — reproduces the original
    per-module pickle-by-value behaviour now that the fixture lives centrally.

    Yields:
        A small local Ray cluster ONCE for the module and pickle its mappers/workers.

    """

    ray.init(
        num_cpus=2,
        ignore_reinit_error=True,
        include_dashboard=False,
        logging_level=logging.ERROR,
    )
    ray.cloudpickle.register_pickle_by_value(request.module)
    yield
    ray.cloudpickle.unregister_pickle_by_value(request.module)
    ray.shutdown()


# --------------------------------------------------------------------------- #
# OpenSearch (k-NN + BM25) via testcontainers, security plugin off. Two         #
# independent single-node clusters stand in for two domains; each allows fs    #
# snapshot repositories under _OPENSEARCH_SNAPSHOT_PATH.                        #
# --------------------------------------------------------------------------- #

_OPENSEARCH_IMAGE = "opensearchproject/opensearch:2.19.1"
_OPENSEARCH_SNAPSHOT_PATH = PurePosixPath("/", "tmp", "snapshots").as_posix()
"""The node's ``path.repo``: an ``fs`` snapshot repository must sit under it."""


def _await_opensearch(url: str) -> None:
    """Poll ``/_cluster/health`` until the node answers.

    (it logs readiness before HTTP is up).

    Raises:
        RuntimeError: Opensearch did not become ready in time.

    """

    for _ in range(120):
        try:
            if (
                httpx.get(f"{url}/_cluster/health", timeout=2).status_code
                == _EXPECTED_STATUS
            ):
                return
        except httpx.TransportError:
            time.sleep(1)
    msg = "opensearch did not become ready in time"
    raise RuntimeError(msg)


def _opensearch() -> Iterator[str]:
    """Start one single-node OpenSearch and yield its HTTP URL.

    Yields:
        One single-node OpenSearch and yield its HTTP URL.

    """

    container = (
        DockerContainer(_OPENSEARCH_IMAGE)
        .with_env("discovery.type", "single-node")
        .with_env("DISABLE_SECURITY_PLUGIN", "true")
        .with_env("DISABLE_INSTALL_DEMO_CONFIG", "true")
        .with_env("OPENSEARCH_JAVA_OPTS", "-Xms512m -Xmx512m")
        .with_env("path.repo", _OPENSEARCH_SNAPSHOT_PATH)
        .with_exposed_ports(9200)
    )
    container.start()
    try:
        url = f"http://{container.get_container_host_ip()}:{container.get_exposed_port(9200)}"
        _await_opensearch(url)
        yield url
    finally:
        container.stop()


@pytest.fixture(scope="session")
def opensearch_url() -> Iterator[str]:
    """Start the first OpenSearch domain for the session and yield its URL.

    Yields:
        The first OpenSearch domain for the session and yield its URL.

    """
    yield from _opensearch()


@pytest.fixture(scope="session")
def opensearch_url_b() -> Iterator[str]:
    """Start a second, independent OpenSearch domain for the session and yield its URL.

    Yields:
        A second, independent OpenSearch domain for the session and yield its URL.

    """
    yield from _opensearch()


@pytest.fixture(scope="session")
def opensearch_snapshot_path() -> str:
    """Return the path every OpenSearch container accepts ``fs`` snapshot repositories.

    Under.

    Returns:
        The path every OpenSearch container accepts ``fs`` snapshot repositories.

    """
    return _OPENSEARCH_SNAPSHOT_PATH
