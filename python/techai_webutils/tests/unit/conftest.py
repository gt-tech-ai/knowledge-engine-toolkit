"""Shared fixtures for the flat unit suite.

Merged home for the per-layer unit fixtures that previously lived in the
now-collapsed ``tests/<layer>/conftest.py`` files:

- ``isolate_config_env`` / ``mock_redis`` — from the former ``foundation/conftest.py``.
- ``s3_config`` / ``sqs_config`` — from the former ``clients/conftest.py``.
- ``metrics_mock`` — the spec'd ``MetricsProvider`` shared by the metric tests.
- ``observed`` — mocked logger, metrics and tracer behind one ``Observability``, recording the
  spans, series and log lines a decorated tier emits.
- ``aws_client`` — the mocked aiobotocore client the S3/SQS clients open.

The fixture names are disjoint, so the merge introduces no collisions. The
integration suite keeps its own ``sqs_config`` / ``s3_config`` (real ElasticMQ/MinIO)
in ``tests/integration/conftest.py``, resolved independently for that directory.
"""

from __future__ import annotations

import os
from contextlib import contextmanager
from dataclasses import dataclass, field
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.storage.config import S3Config
from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricHistogram,
    MetricsProvider,
)
from techai_webutils.core.interfaces.tracer import SpanKind, TracerProvider, TracerSpan
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
from techai_webutils.foundation.decorate import Observability

if TYPE_CHECKING:
    from collections.abc import Generator, Iterator

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
def metrics_mock() -> tuple[MagicMock, dict[str, MagicMock]]:
    """Return a ``MetricsProvider`` mock plus its instruments, recorded by metric name as they are declared.

    Like a real provider (create or retrieve), the first ``counter`` / ``histogram`` declaration of
    a name returns a new ``MagicMock(spec=MetricCounter)`` / ``MagicMock(spec=MetricHistogram)``
    stored under it, and every later declaration of that name returns the same one.
    """
    instruments: dict[str, MagicMock] = {}

    def _counter(name: str, _help: str, _labels: list[str] | None = None) -> MagicMock:
        return instruments.setdefault(name, MagicMock(spec=MetricCounter))

    def _histogram(
        name: str, _help: str, _labels: list[str] | None = None, _buckets: object = None
    ) -> MagicMock:
        return instruments.setdefault(name, MagicMock(spec=MetricHistogram))

    provider = MagicMock(spec=MetricsProvider)
    provider.counter.side_effect = _counter
    provider.histogram.side_effect = _histogram
    return provider, instruments


@dataclass
class Observed:
    """Mocked observability collaborators and what a decorated tier emitted through them."""

    observability: Observability
    """The logger, metrics and tracer mocks, as a tier's Deps take them."""

    logger: MagicMock
    """The ``Logger`` mock."""

    instruments: dict[str, MagicMock]
    """The metric instruments, by series name."""

    spans: list[tuple[str, SpanKind, dict[str, object]]] = field(default_factory=list)
    """Each span opened: its name, kind and attributes."""

    span_errors: list[BaseException] = field(default_factory=list)
    """Each error recorded on a span."""


@pytest.fixture
def observed(metrics_mock: tuple[MagicMock, dict[str, MagicMock]]) -> Observed:
    """Return mocked observability collaborators that record the spans, series and log lines."""
    provider, instruments = metrics_mock
    tracer = MagicMock(spec=TracerProvider)
    logger = MagicMock(spec=Logger)
    result = Observed(Observability(logger, provider, tracer), logger, instruments)

    @contextmanager
    def span(
        name: str, *, kind: SpanKind = SpanKind.INTERNAL, **attributes: object
    ) -> Generator[TracerSpan]:
        result.spans.append((name, kind, attributes))
        handle = MagicMock(spec=TracerSpan)
        handle.record_error.side_effect = result.span_errors.append
        yield handle

    tracer.span.side_effect = span
    return result
