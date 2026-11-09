"""Shared fixtures for the flat unit suite.

Merged home for the per-layer unit fixtures that previously lived in the now-collapsed
``tests/<layer>/conftest.py`` files:

- ``isolate_config_env`` / ``mock_redis`` — from the former ``foundation/conftest.py``.
- ``s3_config`` / ``sqs_config`` — from the former ``clients/conftest.py``.
- ``metrics_mock`` — the spec'd ``MetricsProvider`` shared by the metric tests.
- ``observed`` — mocked logger, metrics and tracer behind one ``Observability``, recording
  the spans, series and log lines a decorated tier emits.
- ``aws_client`` — the mocked aiobotocore client the S3/SQS clients open.

The fixture names are disjoint, so the merge introduces no collisions. The integration
suite keeps its own ``sqs_config`` / ``s3_config`` (real ElasticMQ/MinIO) in
``tests/integration/conftest.py``, resolved independently for that directory.
"""

from __future__ import annotations

import os
from contextlib import contextmanager
from dataclasses import dataclass, field
from pathlib import Path
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

_FAKE_CRED_1 = "minioadmin"
_FAKE_CRED_2 = "local"

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
"""The bare env names the unprefixed settings mixins read (DATABASE_HOST, REDIS_PORT,
...)."""

_OVERLAY_SELECTORS = ("APP_ENV", "ENVIRONMENT")
"""The default overlay selectors; an ambient value would pick an unexpected overlay."""


def _config_env_names() -> list[str]:
    """Return the set env vars a config test may read or write.

    MYAPP_* plus the bare mixin names.

    Returns:
        The set env vars a config test may read or write.

    """
    return [
        key
        for key in os.environ
        if key.startswith(_TEST_PREFIX) or key in _MIXIN_ENV_NAMES
    ]


def pytest_collection_modifyitems(items: list[pytest.Item]) -> None:
    """Run ``isolate_config_env`` around every test in this directory."""
    here = Path(__file__).parent
    for item in items:
        if here in item.path.parents and isinstance(item, pytest.Function):
            item.fixturenames.insert(0, "isolate_config_env")


@pytest.fixture
def isolate_config_env(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    """Isolate every test from ambient and leaked config env vars.

    And reset the bridge sentinel.

    Before the test, the MYAPP_* variables, the bare names the settings mixins read
    (DATABASE_*, REDIS_*, SERVER_*, LOGGING_*, STORAGE_S3_*, MESSAGING_SQS_*,
    OBSERVABILITY_*) and the default overlay selectors are removed through
    ``monkeypatch``, so an ambient value (a developer shell, CI service containers,
    Kubernetes service links) cannot change a default-value assertion and is restored
    afterwards. After the test, whatever the test wrote straight into ``os.environ`` (as
    ``initialize_config`` does) is popped directly — ``monkeypatch.delenv`` would record
    the leaked value and restore it on undo.
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
    """Provide a mock async Redis client for cache tests.

    Returns:
        The mock client.

    """
    return AsyncMock()


@pytest.fixture
def s3_config() -> S3Config:
    """Provide an S3 config pointed at the local MinIO defaults.

    Returns:
        The config.

    """
    return S3Config(
        endpoint="http://localhost:9000",
        bucket="test-bucket",
        region="us-east-1",
        access_key="minioadmin",
        secret_key=_FAKE_CRED_1,
    )


@pytest.fixture
def sqs_config() -> SQSConfig:
    """Provide an SQS config pointed at the local ElasticMQ defaults.

    Returns:
        The config.

    """
    return SQSConfig(
        endpoint="http://localhost:9324",
        region="us-east-1",
        queue_url="http://localhost:9324/queue/test",
        access_key="local",
        secret_key=_FAKE_CRED_2,
    )


@pytest.fixture
def aws_client() -> Iterator[AsyncMock]:
    """Patch ``aiobotocore.session.get_session`` for the test; yield the client it opens.

    Every ``session.create_client(...)`` entered while the test runs yields this one mock,
    so a test opens the real S3/SQS client through its async context and drives the AWS
    API mock.

    Yields:
        The mock client.

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
    """Return a ``MetricsProvider`` mock plus its instruments.

    Recorded by metric name as they are declared.

    Like a real provider (create or retrieve), the first ``counter`` / ``histogram``
    declaration of a name returns a new ``MagicMock(spec=MetricCounter)`` /
    ``MagicMock(spec=MetricHistogram)`` stored under it, and every later declaration of
    that name returns the same one.

    Returns:
        A ``MetricsProvider`` mock plus its instruments.

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
    """Mocked observability collaborators.

    And what a decorated tier emitted through them.
    """

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
    """Return mocked observability collaborators that record the spans.

    Series and log lines.

    Returns:
        Mocked observability collaborators that record the spans.

    """
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


# --------------------------------------------------------------------------- #
# Routed model gateway: a three-model catalog, its routes and a policy store.  #
# --------------------------------------------------------------------------- #


def _stub_llm(model: str) -> dict[str, str]:
    """Return a ``stub`` LLM config fragment naming ``model``.

    Returns:
        A ``stub`` LLM config fragment naming ``model``.

    """
    return {"kind": "stub", "model": model}


@pytest.fixture
def catalog_raw() -> dict[str, object]:
    """Return a catalog: Sonnet (two regions), Haiku.

    And a small off-cloud 8k-window model.

    Returns:
        A catalog: Sonnet (two regions), Haiku.

    """
    return {
        "anthropic/sonnet": {
            "capabilities": {
                "context_tokens": 200_000,
                "max_output": 8192,
                "tools": True,
            },
            "quality": 0.9,
            "ttft_ms": 900,
            "endpoints": [
                {
                    "name": "use1",
                    "llm": _stub_llm("sonnet-use1"),
                    "region": "us-east-1",
                    "zero_retention": True,
                },
                {
                    "name": "usw2",
                    "llm": _stub_llm("sonnet-usw2"),
                    "region": "us-west-2",
                    "zero_retention": True,
                },
            ],
        },
        "anthropic/haiku": {
            "capabilities": {"context_tokens": 200_000, "max_output": 4096},
            "quality": 0.7,
            "ttft_ms": 400,
            "endpoints": [
                {
                    "name": "use1",
                    "llm": _stub_llm("haiku-use1"),
                    "region": "us-east-1",
                    "zero_retention": True,
                },
            ],
        },
        "meta/small": {
            "capabilities": {"context_tokens": 8000, "max_output": 2048},
            "quality": 0.5,
            "ttft_ms": 200,
            "endpoints": [
                {
                    "name": "local",
                    "llm": _stub_llm("small-local"),
                    "region": "us-east-1",
                    "zero_retention": False,
                    "leaves_cloud_boundary": True,
                },
            ],
        },
    }


@pytest.fixture
def routes_raw() -> dict[str, object]:
    """Return routes named for the steps: generate → Sonnet (then Haiku).

    Rewrite → Haiku.

    Returns:
        Routes named for the steps: generate → Sonnet (then Haiku).

    """
    return {
        "generate": {
            "default_model": "anthropic/sonnet",
            "preferred": ["anthropic/haiku"],
        },
        "rewrite": {"default_model": "anthropic/haiku"},
    }
