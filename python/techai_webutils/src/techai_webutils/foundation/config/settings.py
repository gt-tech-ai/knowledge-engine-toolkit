"""Pydantic Settings mixins for configuration management.

This module provides mixin classes that can be composed to build service-specific
Settings classes. Each mixin provides fields for a specific component (database,
redis, server, etc.) and reads them from environment variables — unprefixed by default; a
consumer's settings class sets its own ``model_config`` ``env_prefix`` (e.g. ``"MYAPP_"``) and
passes the same prefix to ``initialize_config(env_prefix=...)`` (with or without the trailing
underscore; the bridge exports ``MYAPP_*`` either way).

Unprefixed names are shared with the whole process environment: a Kubernetes service link such as
``REDIS_PORT=tcp://10.0.0.1:6379`` (injected for a Service named ``redis``) is read as
``redis_port`` and fails validation at startup. Set a consumer-specific prefix in deployments.

Why mixins:
  - Composable: Services can mix-and-match only the components they need
  - DRY: Each component's config is defined once
  - Type-safe: Pydantic validates all fields at runtime
  - Immutable: frozen=True prevents accidental mutations
  - MRO-safe: All mixins inherit from BaseAppSettings to avoid conflicts

Usage:
    from techai_webutils.foundation.config.settings import DatabaseSettings, RedisSettings

    class MyServiceSettings(DatabaseSettings, RedisSettings):
        # Service-specific fields can be added here
        pass

    settings = MyServiceSettings()  # Reads DATABASE_*, REDIS_* (or <prefix>DATABASE_*, ...)
"""

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class BaseAppSettings(BaseSettings):
    """Base class for all settings mixins.

    This defines the shared model_config that all mixins inherit. All mixins
    must inherit from this base class to avoid MRO conflicts when composed.

    Configuration:
      - env_prefix: "" — unprefixed; a consumer's settings class sets its own prefix (recommended:
        bare names such as ``REDIS_PORT`` collide with ambient variables, e.g. Kubernetes service links)
      - frozen: True — settings instances are immutable after creation
      - extra: "ignore" — unknown env vars are ignored (no validation errors)
    """

    model_config = SettingsConfigDict(
        env_prefix="",
        frozen=True,
        extra="ignore",
    )


class DatabaseSettings(BaseAppSettings):
    """Database configuration mixin.

    Fields are prefixed with 'database_' to avoid collision when multiple
    mixins are composed. All read from <prefix>DATABASE_* env vars.
    """

    database_host: str = Field(default="localhost", description="PostgreSQL host.")
    database_port: int = Field(default=5432, description="PostgreSQL port.")
    database_user: str = Field(default="app", description="PostgreSQL user.")
    database_password: str = Field(
        default="dev_password", description="PostgreSQL password."
    )
    database_database: str = Field(
        default="knowledge_engine", description="PostgreSQL database name."
    )
    database_sslmode: str = Field(
        default="disable", description="libpq sslmode (disable/require/…)."
    )
    database_max_connections: int = Field(
        default=25, description="Max open pool connections."
    )
    database_max_idle_connections: int = Field(
        default=5, description="Max idle pool connections."
    )
    database_connection_max_lifetime: str = Field(
        default="5m", description="Max connection lifetime (Go duration string)."
    )


class RedisSettings(BaseAppSettings):
    """Redis configuration mixin.

    Fields are prefixed with 'redis_' to avoid collision.
    All read from <prefix>REDIS_* env vars.
    """

    redis_host: str = Field(default="localhost", description="Redis host.")
    redis_port: int = Field(default=6379, description="Redis port.")
    redis_password: str = Field(default="", description="Redis password (empty = none).")
    redis_db: int = Field(default=0, description="Redis logical database number.")


class ServerSettings(BaseAppSettings):
    """Server configuration mixin.

    Fields are prefixed with 'server_'. Port fields have no defaults because
    they are service-specific and must be set explicitly per service.
    """

    server_host: str = Field(
        default="0.0.0.0",  # noqa: S104  # nosec B104 - containerized services must bind all interfaces
        description="Bind address (0.0.0.0 in containers).",
    )
    server_read_timeout: str = Field(
        default="30s", description="HTTP read timeout (duration string)."
    )
    server_write_timeout: str = Field(
        default="30s", description="HTTP write timeout (duration string)."
    )
    server_idle_timeout: str = Field(
        default="120s", description="HTTP idle timeout (duration string)."
    )


class LoggingSettings(BaseAppSettings):
    """Logging configuration mixin.

    Fields are prefixed with 'logging_'.
    """

    logging_level: str = Field(
        default="info", description="Log level (debug/info/warn/error)."
    )
    logging_format: str = Field(default="json", description="Log format (json/console).")
    logging_redact_pii: bool = Field(
        default=True, description="Redact PII fields from logs."
    )


class S3Settings(BaseAppSettings):
    """S3 storage configuration mixin.

    Fields are prefixed with 'storage_s3_' to match YAML structure.
    """

    storage_s3_endpoint: str = Field(
        default="http://localhost:9000", description="S3/MinIO endpoint URL."
    )
    storage_s3_bucket: str = Field(
        default="documents", description="Document bucket name."
    )
    storage_s3_region: str = Field(default="us-east-1", description="S3 region.")
    storage_s3_access_key_id: str = Field(
        default="minioadmin", description="S3 access key id."
    )
    storage_s3_secret_access_key: str = Field(
        default="minioadmin", description="S3 secret access key."
    )


class SQSSettings(BaseAppSettings):
    """SQS messaging configuration mixin.

    Fields are prefixed with 'messaging_sqs_' to match YAML structure.
    """

    messaging_sqs_endpoint: str = Field(
        default="http://localhost:9324", description="SQS/ElasticMQ endpoint URL."
    )
    messaging_sqs_region: str = Field(default="us-east-1", description="SQS region.")
    messaging_sqs_access_key_id: str = Field(
        default="local", description="SQS access key id."
    )
    messaging_sqs_secret_access_key: str = Field(
        default="local", description="SQS secret access key."
    )


class ObservabilitySettings(BaseAppSettings):
    """Observability configuration mixin.

    Fields are prefixed with 'observability_'.
    """

    observability_otel_endpoint: str = Field(
        default="localhost:4317", description="OTLP collector endpoint."
    )
    observability_service_name: str = Field(
        default="platform", description="Service name for traces/metrics."
    )
    observability_traces_enabled: bool = Field(
        default=True, description="Emit OpenTelemetry traces."
    )
    observability_metrics_enabled: bool = Field(
        default=True, description="Emit Prometheus metrics."
    )
