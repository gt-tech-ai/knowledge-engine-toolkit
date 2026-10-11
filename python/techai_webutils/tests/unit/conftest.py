"""Shared fixtures for the flat unit suite.

Merged home for the per-layer unit fixtures that previously lived in the
now-collapsed ``tests/<layer>/conftest.py`` files:

- ``isolate_config_env`` / ``mock_redis`` — from the former ``foundation/conftest.py``.
- ``s3_config`` / ``sqs_config`` / ``mock_service`` — from the former ``clients/conftest.py``.
- ``metrics_mock`` — the spec'd ``MetricsProvider`` shared by the client-decorator metric tests.
- ``aws_client`` — the mocked aiobotocore client the S3/SQS clients open.

The seven fixture names are disjoint, so the merge introduces no collisions. The
integration suite keeps its own ``sqs_config`` / ``s3_config`` (real ElasticMQ/MinIO)
in ``tests/integration/conftest.py``, resolved independently for that directory.
"""

from __future__ import annotations

import os
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.storage.config import S3Config
from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricHistogram,
    MetricsProvider,
)
from techai_webutils.foundation.config.bridge import reset_config
from techai_webutils.foundation.config.settings import (
    DatabaseSettings,
    LoggingSettings,
    ObservabilitySettings,
    RedisSettings,
    S3Settings,
    ServerSettings,
    SQSSettings,
)

if TYPE_CHECKING:
    from collections.abc import Iterator

_TEST_PREFIX = "MYAPP_"
"""The env-var prefix the config tests export under."""

_MIXIN_ENV_NAMES = frozenset(
    name.upper()
    for mixin in (
        DatabaseSettings,
        LoggingSettings,
        ObservabilitySettings,
        RedisSettings,
        S3Settings,
        ServerSettings,
        SQSSettings,
    )
    for name in mixin.model_fields
)
"""The bare env names the unprefixed settings mixins read (DATABASE_HOST, REDIS_PORT, ...)."""

_OVERLAY_SELECTORS = ("APP_ENV", "ENVIRONMENT")
"""The default overlay selectors; an ambient value would pick an unexpected overlay."""


def _config_env_names() -> list[str]:
    """Return the set env vars a config test may read or write: MYAPP_* plus the bare mixin names."""
    return [
        key
        for key in os.environ
        if key.startswith(_TEST_PREFIX) or key in _MIXIN_ENV_NAMES
    ]


@pytest.fixture(autouse=True)
def isolate_config_env(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    """Isolate every test from ambient and leaked config env vars, and reset the bridge sentinel.

    Before the test, the MYAPP_* variables, the bare names the settings mixins read (DATABASE_*,
    REDIS_*, SERVER_*, LOGGING_*, STORAGE_S3_*, MESSAGING_SQS_*, OBSERVABILITY_*) and the default
    overlay selectors are removed through ``monkeypatch``, so an ambient value (a developer shell, CI
    service containers, Kubernetes service links) cannot change a default-value assertion and is
    restored afterwards. After the test, whatever the test wrote straight into ``os.environ`` (as
    ``initialize_config`` does) is popped directly — ``monkeypatch.delenv`` would record the leaked
    value and restore it on undo.
    """
    for key in [*_config_env_names(), *_OVERLAY_SELECTORS]:
        monkeypatch.delenv(key, raising=False)
    reset_config()

    yield

    for key in _config_env_names():
        os.environ.pop(key, None)
    reset_config()


@pytest.fixture
def mock_redis() -> AsyncMock:
    """Provide a mock async Redis client for cache tests."""
    return AsyncMock()


@pytest.fixture
def s3_config() -> S3Config:
    """Provide an S3 config pointed at the local MinIO defaults."""
    return S3Config(
        endpoint="http://localhost:9000",
        bucket="test-bucket",
        region="us-east-1",
        access_key="minioadmin",
        secret_key="minioadmin",
    )


@pytest.fixture
def sqs_config() -> SQSConfig:
    """Provide an SQS config pointed at the local ElasticMQ defaults."""
    return SQSConfig(
        endpoint="http://localhost:9324",
        region="us-east-1",
        queue_url="http://localhost:9324/queue/test",
        access_key="local",
        secret_key="local",
    )


@pytest.fixture
def aws_client() -> Iterator[AsyncMock]:
    """Patch ``aiobotocore.session.get_session`` for the test; yield the client it opens.

    Every ``session.create_client(...)`` entered while the test runs yields this one mock, so a
    test opens the real S3/SQS client through its async context and drives the AWS API mock.
    """
    client = AsyncMock()
    opened = MagicMock()
    opened.__aenter__ = AsyncMock(return_value=client)
    session = MagicMock()
    session.create_client = MagicMock(return_value=opened)
    with patch("aiobotocore.session.get_session", return_value=session):
        yield client


@pytest.fixture
def mock_service() -> MagicMock:
    """Return a MagicMock service reproducing the canned returns the proxies forward.

    ``get_item(id)`` -> ``{"id": id, "name": "test"}``; ``create_item(name)`` ->
    ``{"id": "new", "name": name}``; ``failing_method()`` raises ``RuntimeError``, so the
    proxy tests can assert transparent forwarding, chained composition, and error propagation.
    """
    svc = MagicMock()
    svc.get_item.side_effect = lambda item_id: {"id": item_id, "name": "test"}
    svc.create_item.side_effect = lambda name: {"id": "new", "name": name}
    svc.failing_method.side_effect = RuntimeError("intentional failure")
    return svc


@pytest.fixture
def metrics_mock() -> tuple[MagicMock, dict[str, MagicMock]]:
    """Return a ``MetricsProvider`` mock plus its instruments, recorded by metric name as they are declared.

    Each ``counter`` / ``histogram`` declaration returns a fresh ``MagicMock(spec=MetricCounter)`` /
    ``MagicMock(spec=MetricHistogram)`` stored under the metric name.
    """
    instruments: dict[str, MagicMock] = {}

    def _counter(name: str, _help: str, _labels: list[str] | None = None) -> MagicMock:
        instruments[name] = MagicMock(spec=MetricCounter)
        return instruments[name]

    def _histogram(
        name: str, _help: str, _labels: list[str] | None = None, _buckets: object = None
    ) -> MagicMock:
        instruments[name] = MagicMock(spec=MetricHistogram)
        return instruments[name]

    provider = MagicMock(spec=MetricsProvider)
    provider.counter.side_effect = _counter
    provider.histogram.side_effect = _histogram
    return provider, instruments
