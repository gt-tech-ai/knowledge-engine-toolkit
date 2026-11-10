"""Pydantic Settings mixins for configuration management.

This module provides mixin classes that can be composed to build service-specific Settings
classes. Each mixin provides fields for a specific component (database, redis, server,
etc.) and reads them from environment variables — unprefixed by default; a consumer's
settings class sets its own ``model_config`` ``env_prefix`` (e.g. ``"MYAPP_"``) and passes
the same prefix to ``initialize_config(env_prefix=...)`` (with or without the trailing
underscore; the bridge exports ``MYAPP_*`` either way).

Unprefixed names are shared with the whole process environment: a Kubernetes service link
such as ``REDIS_PORT=tcp://10.0.0.1:6379`` (injected for a Service named ``redis``) is
read as ``redis_port`` and fails validation at startup. Set a consumer-specific prefix in
deployments.

Why mixins:
  - Composable: Services can mix-and-match only the components they need
  - DRY: Each component's config is defined once
  - Type-safe: Pydantic validates all fields at runtime
  - Immutable: frozen=True prevents accidental mutations
  - MRO-safe: All mixins inherit from BaseAppSettings to avoid conflicts

Usage: from techai_webutils.foundation.config.settings import DatabaseSettings,
    RedisSettings

    class MyServiceSettings(DatabaseSettings, RedisSettings):
        # Service-specific fields can be added here pass

    settings = MyServiceSettings()  # Reads DATABASE_*, REDIS_* (or <prefix>DATABASE_*,
    ...)
"""

from ipaddress import IPv4Address
from typing import ClassVar

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

_ALL_INTERFACES = str(IPv4Address(0))
"""The bind address of a containerized service, reachable on every interface."""


class BaseAppSettings(BaseSettings):
    """Base class for all settings mixins.

    This defines the shared model_config that all mixins inherit. All mixins must inherit
    from this base class to avoid MRO conflicts when composed.

    Configuration:
      - env_prefix: "" — unprefixed; a consumer's settings class sets its own prefix
        (recommended: bare names such as ``REDIS_PORT`` collide with ambient variables,
        e.g. Kubernetes service links)
      - frozen: True — settings instances are immutable after creation
      - extra: "ignore" — unknown env vars are ignored (no validation errors)
    """

    model_config: ClassVar[SettingsConfigDict] = SettingsConfigDict(
        env_prefix="",
        frozen=True,
        extra="ignore",
        use_attribute_docstrings=True,
    )
    """Unprefixed, immutable, ignores unknown variables; each field's docstring is its
    schema description."""


class DatabaseSettings(BaseAppSettings):
    """Database configuration mixin.

    Fields are prefixed with 'database_' to avoid collision when multiple
    mixins are composed. All read from <prefix>DATABASE_* env vars.
    """

    database_host: str = "localhost"
    """PostgreSQL host."""
    database_port: int = 5432
    """PostgreSQL port."""
    database_user: str = "app"
    """PostgreSQL user."""
    database_password: str = ""
    """PostgreSQL password; empty until the environment or ``secrets.yaml`` sets it."""
    database_database: str = "knowledge_engine"
    """PostgreSQL database name."""
    database_sslmode: str = "disable"
    """libpq sslmode (disable/require/…)."""
    database_max_connections: int = 25
    """Max open pool connections."""
    database_max_idle_connections: int = 5
    """Max idle pool connections."""
    database_connection_max_lifetime: str = "5m"
    """Max connection lifetime (Go duration string)."""


class RedisSettings(BaseAppSettings):
    """Redis configuration mixin.

    Fields are prefixed with 'redis_' to avoid collision.
    All read from <prefix>REDIS_* env vars.
    """

    redis_host: str = "localhost"
    """Redis host."""
    redis_port: int = 6379
    """Redis port."""
    redis_password: str = ""
    """Redis password (empty = none)."""
    redis_db: int = 0
    """Redis logical database number."""


class ServerSettings(BaseAppSettings):
    """Server configuration mixin.

    Fields are prefixed with 'server_'. Port fields have no defaults because
    they are service-specific and must be set explicitly per service.
    """

    server_host: str = _ALL_INTERFACES
    """Bind address (0.0.0.0 in containers)."""
    server_read_timeout: str = "30s"
    """HTTP read timeout (duration string)."""
    server_write_timeout: str = "30s"
    """HTTP write timeout (duration string)."""
    server_idle_timeout: str = "120s"
    """HTTP idle timeout (duration string)."""


class LoggingSettings(BaseAppSettings):
    """Logging configuration mixin.

    Fields are prefixed with 'logging_'.
    """

    logging_level: str = "info"
    """Log level (debug/info/warn/error)."""
    logging_format: str = "json"
    """Log format (json/console)."""
    logging_redact_pii: bool = True
    """Redact PII fields from logs."""


class S3Settings(BaseAppSettings):
    """S3 storage configuration mixin.

    Fields are prefixed with 'storage_s3_' to match YAML structure.
    """

    storage_s3_endpoint: str = "http://localhost:9000"
    """S3/MinIO endpoint URL."""
    storage_s3_bucket: str = "documents"
    """Document bucket name."""
    storage_s3_region: str = "us-east-1"
    """S3 region."""
    storage_s3_access_key_id: str = ""
    """S3 access key id; empty uses the default AWS credential chain."""
    storage_s3_secret_access_key: str = ""
    """S3 secret access key; empty uses the default AWS credential chain."""


class SQSSettings(BaseAppSettings):
    """SQS messaging configuration mixin.

    Fields are prefixed with 'messaging_sqs_' to match YAML structure.
    """

    messaging_sqs_endpoint: str = "http://localhost:9324"
    """SQS/ElasticMQ endpoint URL."""
    messaging_sqs_region: str = "us-east-1"
    """SQS region."""
    messaging_sqs_access_key_id: str = ""
    """SQS access key id; empty uses the default AWS credential chain."""
    messaging_sqs_secret_access_key: str = ""
    """SQS secret access key; empty uses the default AWS credential chain."""


class ObservabilitySettings(BaseAppSettings):
    """Observability configuration mixin.

    Fields are prefixed with 'observability_'.
    """

    observability_otel_endpoint: str = "localhost:4317"
    """OTLP collector endpoint."""
    observability_service_name: str = "platform"
    """Service name for traces/metrics."""
    observability_traces_enabled: bool = True
    """Emit OpenTelemetry traces."""
    observability_metrics_enabled: bool = True
    """Emit Prometheus metrics."""


class TelemetrySettings(BaseSettings):
    """OpenTelemetry export settings, read under the standard OTel variable names.

    A service's settings class mixes it in; each field's ``validation_alias`` is the
    standard variable, so it is read unprefixed whatever the class's ``env_prefix``. It
    derives from ``BaseSettings`` rather than ``BaseAppSettings`` and sets no
    ``env_prefix``, so mixing it in never resets the service's own prefix.
    ``foundation.observability.start_observability`` reads it.
    """

    model_config: ClassVar[SettingsConfigDict] = SettingsConfigDict(
        frozen=True, extra="ignore", use_attribute_docstrings=True
    )
    """Immutable, ignores unknown variables; each field's docstring is its schema
    description."""

    otel_exporter_otlp_endpoint: str = Field(
        default="localhost:4317", validation_alias="OTEL_EXPORTER_OTLP_ENDPOINT"
    )
    """OTLP collector endpoint (OTEL_EXPORTER_OTLP_ENDPOINT)."""
    trace_sample_rate: str = Field(default="", validation_alias="TRACE_SAMPLE_RATE")
    """Fraction of traces sampled, 0.0-1.0 (TRACE_SAMPLE_RATE); empty or invalid
    samples every trace."""
