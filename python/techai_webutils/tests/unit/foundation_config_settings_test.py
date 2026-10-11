"""Tests for Pydantic Settings mixins (DatabaseSettings, RedisSettings, etc.).

Why these tests are important:
  - Pydantic Settings is the runtime configuration system for all Python services
  - MRO conflicts can break mixin composition silently
  - Frozen settings prevent accidental mutations that cause race conditions
  - Field defaults must match config.yaml to work in all environments
"""

import pytest
from pydantic import ValidationError


def test_database_settings_defaults() -> None:
    """Test that DatabaseSettings exposes the documented local-dev defaults.

    **Why this test is important:**
      - These defaults are what a developer gets with no env vars set; they must
        match a local Docker Compose Postgres so a dev stack works out of the box
      - A drifted default (wrong port, user, or db name) would silently point
        local services at a nonexistent database

    **What it tests:**
      - database_host, database_port, database_user, database_password, and
        database_database resolve to their documented localhost/5432/app defaults
    """
    from techai_webutils.foundation.config.settings import DatabaseSettings

    # Act
    settings = DatabaseSettings()

    # Assert
    assert settings.database_host == "localhost"
    assert settings.database_port == 5432
    assert settings.database_user == "app"
    assert settings.database_password == "dev_password"
    assert settings.database_database == "knowledge_engine"


def test_database_settings_reads_from_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that DatabaseSettings reads and coerces DATABASE_* env vars.

    **Why this test is important:**
      - Env vars are how production/staging configure the database; this is the
        path the YAML bridge ultimately feeds, so it must override defaults
      - The port must be coerced from its string env value to int — a failure
        here would hand a str to a driver expecting a number

    **What it tests:**
      - DATABASE_HOST overrides the default host
      - DATABASE_PORT is parsed from its string env value into an int
    """
    from techai_webutils.foundation.config.settings import DatabaseSettings

    # Arrange
    monkeypatch.setenv("DATABASE_HOST", "prod-db.example.com")
    monkeypatch.setenv("DATABASE_PORT", "5433")

    # Act
    settings = DatabaseSettings()

    # Assert
    assert settings.database_host == "prod-db.example.com"
    assert settings.database_port == 5433


def test_database_settings_constructor_override() -> None:
    """Test that DatabaseSettings honors explicit constructor keyword overrides.

    **Why this test is important:**
      - Tests and call sites need to inject specific values programmatically
        without mutating process-global env vars (which would leak across tests)
      - Confirms constructor args win, giving a clean, isolated config path

    **What it tests:**
      - Passing database_host and database_port to the constructor sets those
        exact values on the instance
    """
    from techai_webutils.foundation.config.settings import DatabaseSettings

    # Act
    settings = DatabaseSettings(database_host="custom-host", database_port=9999)

    # Assert
    assert settings.database_host == "custom-host"
    assert settings.database_port == 9999


def test_database_settings_frozen() -> None:
    """Test that DatabaseSettings instances reject mutation after construction.

    **Why this test is important:**
      - Settings are shared across goroutine-equivalent async tasks; a mutable
        config invites races and action-at-a-distance bugs where one component
        rewrites another's view of the world
      - frozen=True is the structural guard that makes config effectively
        read-only; this proves it is enforced, not just declared

    **What it tests:**
      - Assigning to database_host post-construction raises ValidationError with
        the "Instance is frozen" message
    """
    from techai_webutils.foundation.config.settings import DatabaseSettings

    # Arrange
    settings = DatabaseSettings()

    # Act & Assert
    with pytest.raises(ValidationError, match="Instance is frozen"):
        settings.database_host = "mutated"


def test_redis_settings_defaults() -> None:
    """Test that RedisSettings exposes the documented local-dev defaults.

    **Why this test is important:**
      - With no env vars set, these defaults must point at the local Docker
        Compose Redis so caching works out of the box during development
      - A wrong host/port/db default would route cache traffic to nowhere or to
        the wrong logical database, corrupting or losing cached state

    **What it tests:**
      - redis_host defaults to "localhost", redis_port to 6379, redis_db to 0
    """
    from techai_webutils.foundation.config.settings import RedisSettings

    # Act
    settings = RedisSettings()

    # Assert
    assert settings.redis_host == "localhost"
    assert settings.redis_port == 6379
    assert settings.redis_db == 0


def test_server_settings_defaults() -> None:
    """Test that ServerSettings defaults the bind host to all interfaces.

    **Why this test is important:**
      - Containerized services must bind 0.0.0.0 to be reachable across the pod
        network; a default of 127.0.0.1 would make a service start cleanly yet
        be invisible to its callers — a silent, deploy-only outage
      - Port is intentionally service-specific (no default), so the host default
        is the meaningful invariant to pin here

    **What it tests:**
      - server_host defaults to "0.0.0.0"
    """
    from techai_webutils.foundation.config.settings import ServerSettings

    # Act
    settings = ServerSettings()

    # Assert
    assert settings.server_host == "0.0.0.0"
    # Port has no default (service-specific)


def test_logging_settings_defaults() -> None:
    """Test that LoggingSettings defaults to info-level JSON logs with PII redaction on.

    **Why this test is important:**
      - JSON format is what the log pipeline (Loki/Alloy) parses; defaulting to a
        non-structured format would break log ingestion in every environment
      - redact_pii defaulting to True is a privacy safeguard — a default of False
        would risk leaking PII into logs whenever the setting is left unspecified

    **What it tests:**
      - logging_level defaults to "info", logging_format to "json", and
        logging_redact_pii to True
    """
    from techai_webutils.foundation.config.settings import LoggingSettings

    # Act
    settings = LoggingSettings()

    # Assert
    assert settings.logging_level == "info"
    assert settings.logging_format == "json"
    assert settings.logging_redact_pii is True


def test_mixin_composition_no_mro_errors() -> None:
    """Test that composing two Settings mixins yields a usable class without MRO errors.

    **Why this test is important:**
      - Every real service builds its config by inheriting several of these
        mixins; if the shared BaseAppSettings ancestor didn't make them
        MRO-compatible, that composition would raise at class-definition time
        and the service could not start
      - This is the structural guarantee that the mixin design is actually
        composable, not just declared so

    **What it tests:**
      - A class inheriting DatabaseSettings and RedisSettings defines and
        instantiates successfully
      - Fields from both mixins (database_host, redis_host) are accessible on the
        composed instance
    """
    from techai_webutils.foundation.config.settings import DatabaseSettings, RedisSettings

    # Act: Compose two mixins
    class MySettings(DatabaseSettings, RedisSettings):
        pass

    settings = MySettings()

    # Assert: Both sets of fields are accessible
    assert settings.database_host == "localhost"
    assert settings.redis_host == "localhost"


def test_mixin_composition_with_env_vars(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that a composed mixin routes each component's env var to its own field.

    **Why this test is important:**
      - Both mixins share one env prefix, so the per-field name prefixing
        (database_/redis_) is the only thing preventing one component's env var
        from bleeding into another's field
      - A collision would cross-wire config — e.g. the Redis host silently
        overriding the database host — a subtle, security-relevant misroute

    **What it tests:**
      - DATABASE_HOST sets only database_host
      - REDIS_HOST sets only redis_host (no cross-contamination)
    """
    from techai_webutils.foundation.config.settings import DatabaseSettings, RedisSettings

    # Arrange
    monkeypatch.setenv("DATABASE_HOST", "db-override")
    monkeypatch.setenv("REDIS_HOST", "redis-override")

    # Act
    class MySettings(DatabaseSettings, RedisSettings):
        pass

    settings = MySettings()

    # Assert: No collision
    assert settings.database_host == "db-override"
    assert settings.redis_host == "redis-override"


def test_base_app_settings_has_correct_model_config() -> None:
    """Test that BaseAppSettings centralizes the env_prefix, frozen, and extra policy.

    **Why this test is important:**
      - This single shared model_config is what makes every mixin agree on the
        env prefix, immutability, and unknown-key handling; defining it once
        on the common ancestor is what keeps mixin composition MRO-safe and
        behaviorally consistent
      - Drift in any of these three settings would change config semantics for
        every service at once, so pinning them here is high-leverage

    **What it tests:**
      - model_config sets env_prefix to "" (unprefixed), frozen to True, and extra to
        "ignore"
    """
    from techai_webutils.foundation.config.settings import BaseAppSettings

    # Assert
    assert not BaseAppSettings.model_config.get("env_prefix")
    assert BaseAppSettings.model_config.get("frozen") is True
    assert BaseAppSettings.model_config.get("extra") == "ignore"


def test_settings_subclass_owns_its_env_prefix(monkeypatch: pytest.MonkeyPatch) -> None:
    """A consumer's settings class composes the mixins under its own env prefix.

    **Why this test is important:**
      - The mixins' default prefix is a library default, not the consumer's convention; a consumer
        must be able to read its own variables without renaming its deployment's env vars.

    **What it tests:**
      - A DatabaseSettings subclass with env_prefix="MYAPP_" reads MYAPP_DATABASE_HOST and ignores
        the unprefixed DATABASE_HOST.
    """
    from pydantic_settings import SettingsConfigDict

    from techai_webutils.foundation.config.settings import DatabaseSettings

    class MyAppSettings(DatabaseSettings):
        model_config = SettingsConfigDict(
            env_prefix="MYAPP_", frozen=True, extra="ignore"
        )

    monkeypatch.setenv("MYAPP_DATABASE_HOST", "db.example")
    monkeypatch.setenv("DATABASE_HOST", "wrong")

    assert MyAppSettings().database_host == "db.example"
