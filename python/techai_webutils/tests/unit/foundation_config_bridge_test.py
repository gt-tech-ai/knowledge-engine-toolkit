"""Tests for YAML-to-env bridge (initialize_config and apply_yaml_defaults).

Why these tests are important:
  - The bridge is the interface between YAML config files and Pydantic Settings
  - It must handle missing config directories gracefully (Docker/K8s deployments)
  - Environment variables must take precedence over YAML values (12-factor)
  - Idempotency is critical for test isolation and multi-call scenarios
"""

import os
from pathlib import Path
from tempfile import TemporaryDirectory

import pytest


def test_initialize_config_sets_env_vars_from_yaml(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Test that initialize_config() loads YAML and exports MYAPP_* env vars.

    **Why this test is important:**
      - This is the primary job of the bridge: it is the only path by which
        YAML config reaches Pydantic Settings, which read solely from env vars
      - If the dotted-path-to-env-var mapping broke, services would silently
        fall back to defaults instead of honoring base.yaml

    **What it tests:**
      - YAML keys are flattened to MYAPP_* env vars (every scalar leaf)
      - Dotted paths like database.host become MYAPP_DATABASE_HOST, with both
        host and port from the database and redis sections exported
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange: Create temp config directory with YAML
    with TemporaryDirectory() as tmpdir:
        config_file = Path(tmpdir) / "base.yaml"
        config_file.write_text("""
database:
  host: test-db-host
  port: 5555
redis:
  host: test-redis-host
  port: 6380
""")

        # Clear any existing env vars
        for key in list(os.environ.keys()):
            if key.startswith("MYAPP_"):
                monkeypatch.delenv(key, raising=False)

        # Act
        initialize_config(tmpdir, env_prefix="MYAPP")

        # Assert
        assert os.environ.get("MYAPP_DATABASE_HOST") == "test-db-host"
        assert os.environ.get("MYAPP_DATABASE_PORT") == "5555"
        assert os.environ.get("MYAPP_REDIS_HOST") == "test-redis-host"
        assert os.environ.get("MYAPP_REDIS_PORT") == "6380"

        # Cleanup
        reset_config()


def test_initialize_config_merges_environment_overlay(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Test that initialize_config() merges the {env}.yaml overlay and flattens every key.

    **Why this test is important:**
      - Deployments mount base.yaml + {env}.yaml and rely on the overlay to flip
        stub/localhost defaults to the real backend (e.g. a ``kind: bedrock``). If the overlay
        is not merged, or a deeply nested key is dropped, the service silently runs against
        stub/localhost defaults.

    **What it tests:**
      - With APP_ENV set, base.yaml + {env}.yaml deep-merge (overlay wins on conflict).
      - A base-only key survives; a deeply nested key is exported.
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    with TemporaryDirectory() as tmpdir:
        (Path(tmpdir) / "base.yaml").write_text("""
retrieval:
  llm:
    kind: stub
    model: base-model
  kb:
    kind: stub
""")
        (Path(tmpdir) / "staging.yaml").write_text("""
retrieval:
  llm:
    kind: bedrock
  kb:
    kind: bedrock
""")
        for key in [k for k in os.environ if k.startswith("MYAPP_")]:
            monkeypatch.delenv(key, raising=False)
        monkeypatch.setenv("APP_ENV", "staging")

        initialize_config(tmpdir, env_prefix="MYAPP")

        # Overlay wins on conflict...
        assert os.environ.get("MYAPP_RETRIEVAL_LLM_KIND") == "bedrock"
        assert os.environ.get("MYAPP_RETRIEVAL_KB_KIND") == "bedrock"
        # ...and a base-only nested key survives the merge.
        assert os.environ.get("MYAPP_RETRIEVAL_LLM_MODEL") == "base-model"

        reset_config()


def test_initialize_config_env_var_precedence(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that an env var set before initialize_config() is not overwritten by YAML.

    **Why this test is important:**
      - 12-factor precedence (env over file) is the contract operators rely on
        to override any YAML default in staging/prod without editing files
      - If YAML clobbered a pre-set env var, a deployed override would be
        silently ignored — a dangerous, hard-to-diagnose misconfiguration

    **What it tests:**
      - A pre-set MYAPP_DATABASE_HOST keeps its value after initialize_config
      - Keys absent from the environment (database.port) are still populated
        from YAML
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange
    with TemporaryDirectory() as tmpdir:
        config_file = Path(tmpdir) / "base.yaml"
        config_file.write_text("""
database:
  host: yaml-host
  port: 5432
""")

        # Set env var BEFORE initialize_config
        monkeypatch.setenv("MYAPP_DATABASE_HOST", "override-host")

        # Act
        initialize_config(tmpdir, env_prefix="MYAPP")

        # Assert: Pre-set env var is NOT overwritten
        assert os.environ.get("MYAPP_DATABASE_HOST") == "override-host"
        # Port from YAML is still set
        assert os.environ.get("MYAPP_DATABASE_PORT") == "5432"

        # Cleanup
        reset_config()


def test_initialize_config_missing_directory_graceful_fallback(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """Test that initialize_config() degrades to a warning when the config dir is missing.

    **Why this test is important:**
      - Docker/K8s deployments often mount no config files and configure purely
        via env vars; a hard failure here would prevent services from starting
        in their primary production deployment mode
      - The warning (rather than a swallowed silence) keeps a genuine
        misconfiguration visible in logs

    **What it tests:**
      - initialize_config does not raise for a nonexistent directory
      - A "config directory not found" warning is emitted
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange: Use a directory that doesn't exist
    nonexistent_dir = "/nonexistent/config/path"

    # Act: Should not raise
    initialize_config(nonexistent_dir, env_prefix="MYAPP")

    # Assert: Warning logged, no exception
    assert "Config directory not found" in caplog.text or "does not exist" in caplog.text

    # Cleanup
    reset_config()


def test_initialize_config_idempotency(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that a second initialize_config() call is a no-op via the sentinel flag.

    **Why this test is important:**
      - Multiple modules and test fixtures may defensively call initialize_config;
        without idempotency, a later call could re-apply YAML and stomp values
        that were intentionally changed after the first initialization
      - Confirms the sentinel actually short-circuits re-execution

    **What it tests:**
      - After the first call set the value, a value modified between calls
        survives the second call (the second call does not re-apply YAML)
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange
    with TemporaryDirectory() as tmpdir:
        config_file = Path(tmpdir) / "base.yaml"
        config_file.write_text("""
database:
  host: first-host
""")

        # Act: Call twice
        initialize_config(tmpdir, env_prefix="MYAPP")
        first_value = os.environ.get("MYAPP_DATABASE_HOST")

        # Modify env var between calls
        monkeypatch.setenv("MYAPP_DATABASE_HOST", "modified")

        initialize_config(tmpdir, env_prefix="MYAPP")  # Second call
        second_value = os.environ.get("MYAPP_DATABASE_HOST")

        # Assert: Second call is a no-op, modified value preserved
        assert first_value == "first-host"
        assert second_value == "modified"  # Not overwritten

        # Cleanup
        reset_config()


def test_reset_config_clears_sentinel(monkeypatch: pytest.MonkeyPatch) -> None:
    """Test that reset_config() clears the sentinel so initialize_config() runs again.

    **Why this test is important:**
      - The idempotency sentinel is process-global; without a reset hook, the
        first test to initialize would poison every later test's config state,
        making the suite order-dependent and unreliable
      - Verifies the escape hatch that re-enables re-initialization actually
        works (and re-reads the YAML afresh)

    **What it tests:**
      - After reset_config, a second initialize_config picks up newly-written
        YAML (second-run value), proving re-initialization occurred
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    # Arrange
    with TemporaryDirectory() as tmpdir:
        config_file = Path(tmpdir) / "base.yaml"
        config_file.write_text("""
database:
  host: first-run
""")

        # Act
        initialize_config(tmpdir, env_prefix="MYAPP")
        first_value = os.environ.get("MYAPP_DATABASE_HOST")

        # Clear sentinel
        reset_config()

        # Modify YAML
        config_file.write_text("""
database:
  host: second-run
""")

        # Clear env var to see if it gets set again
        monkeypatch.delenv("MYAPP_DATABASE_HOST", raising=False)

        # Re-initialize
        initialize_config(tmpdir, env_prefix="MYAPP")
        second_value = os.environ.get("MYAPP_DATABASE_HOST")

        # Assert: Second initialization worked
        assert first_value == "first-run"
        assert second_value == "second-run"

        # Cleanup
        reset_config()


def test_apply_yaml_defaults_stringifies_bool(monkeypatch: pytest.MonkeyPatch) -> None:
    """A bool YAML value is exported as "True"/"False" (Pydantic parses either casing).

    **Why this test is important:**
      - bool is an int subclass; without an explicit branch it would stringify via
        the int path inconsistently. Feature flags in YAML must reach settings as
        parseable booleans.

    **What it tests:**
      - apply_yaml_defaults({"flag": True}, "MYAPP") exports MYAPP_FLAG="True".
    """
    from techai_webutils.foundation.config.bridge import apply_yaml_defaults

    monkeypatch.delenv("MYAPP_FLAG", raising=False)
    apply_yaml_defaults({"flag": True}, "MYAPP")
    assert os.environ["MYAPP_FLAG"] == "True"


def test_initialize_config_missing_base_yaml_is_graceful() -> None:
    """A config dir without base.yaml initializes cleanly (env-vars-only deployments).

    **Why this test is important:**
      - Docker/K8s deployments often ship no base.yaml and rely purely on env vars;
        the bridge must not raise, or every such deployment would crash at startup.

    **What it tests:**
      - initialize_config against an empty dir returns without error and marks init.
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    reset_config()
    with TemporaryDirectory() as empty:
        initialize_config(
            empty, env_prefix="MYAPP"
        )  # no base.yaml present — must not raise
    reset_config()


def test_initialize_config_uses_consumer_prefix_and_env_selector(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """initialize_config exports under the consumer's prefix and picks the overlay from its env vars.

    **Why this test is important:**
      - The env-var prefix and the variable that selects the overlay are a consumer's conventions; a
        library that hard-codes its own names exports config the consumer's settings never read.

    **What it tests:**
      - With env_prefix="MYAPP" and env_selectors=("MYAPP_ENV",), base + the MYAPP_ENV overlay export
        as MYAPP_* (overlay wins) and nothing is exported unprefixed.
    """
    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    monkeypatch.delenv("APP_ENV", raising=False)
    monkeypatch.setenv("MYAPP_ENV", "staging")
    with TemporaryDirectory() as tmpdir:
        (Path(tmpdir) / "base.yaml").write_text("widget:\n  size: 3\n  color: red\n")
        (Path(tmpdir) / "staging.yaml").write_text("widget:\n  size: 7\n")
        try:
            initialize_config(tmpdir, env_prefix="MYAPP", env_selectors=("MYAPP_ENV",))

            assert os.environ.get("MYAPP_WIDGET_SIZE") == "7"
            assert os.environ.get("MYAPP_WIDGET_COLOR") == "red"
            assert "WIDGET_SIZE" not in os.environ
        finally:
            for key in ("MYAPP_WIDGET_SIZE", "MYAPP_WIDGET_COLOR"):
                os.environ.pop(key, None)
            reset_config()


def _write_widget_config(tmpdir: str, overlays: dict[str, str] | None = None) -> None:
    """Write a base.yaml (``kebridge.size: 3``, ``kebridge.color: red``) plus any named overlays."""
    (Path(tmpdir) / "base.yaml").write_text("kebridge:\n  size: 3\n  color: red\n")
    for env, body in (overlays or {}).items():
        (Path(tmpdir) / f"{env}.yaml").write_text(body)


@pytest.mark.parametrize("prefix", ["MYAPP", "MYAPP_"])
def test_initialize_config_prefix_round_trips_into_a_prefixed_settings_class(
    monkeypatch: pytest.MonkeyPatch, prefix: str
) -> None:
    """The YAML reaches a settings class whose env_prefix is ``MYAPP_``.

    It does so whether or not the bridge prefix carries the trailing underscore.

    Why this test is important:
      - A consumer naturally passes its settings' ``env_prefix`` ("MYAPP_") to initialize_config. Joining
        it with another "_" exported ``MYAPP__DATABASE_HOST``, which pydantic never reads, so the whole
        YAML was silently ignored and the service ran on built-in defaults.

    What it tests:
      - With env_prefix "MYAPP" or "MYAPP_", a DatabaseSettings subclass with env_prefix "MYAPP_" reads
        database.host/port from base.yaml, and no double-underscore variable is exported.
    """
    from pydantic_settings import SettingsConfigDict

    from techai_webutils.foundation.config.bridge import initialize_config
    from techai_webutils.foundation.config.settings import DatabaseSettings

    class _AppSettings(DatabaseSettings):
        model_config = SettingsConfigDict(
            env_prefix="MYAPP_", frozen=True, extra="ignore"
        )

    monkeypatch.delenv("APP_ENV", raising=False)
    monkeypatch.delenv("ENVIRONMENT", raising=False)
    with TemporaryDirectory() as tmpdir:
        (Path(tmpdir) / "base.yaml").write_text(
            "database:\n  host: yaml-db\n  port: 6543\n"
        )

        initialize_config(tmpdir, env_prefix=prefix)

        settings = _AppSettings()
        assert settings.database_host == "yaml-db"
        assert settings.database_port == 6543
        assert not [key for key in os.environ if key.startswith("MYAPP__")]


def test_initialize_config_rejects_a_second_call_with_different_arguments() -> None:
    """A second initialize_config with a different dir, prefix or selectors raises instead of no-opping.

    Why this test is important:
      - The first call wins process-wide. A later call with another prefix used to return silently, so
        an early default-prefix call left the app's own prefixed settings on built-in defaults with no
        error anywhere.

    What it tests:
      - After initialize_config(dir, env_prefix="MYAPP"), a call with another prefix, another directory
        or other selectors raises ValueError naming the conflict; repeating the same arguments (including
        the "MYAPP_" spelling of the same prefix) stays a no-op.
    """
    from techai_webutils.foundation.config.bridge import initialize_config

    with TemporaryDirectory() as tmpdir, TemporaryDirectory() as other_dir:
        _write_widget_config(tmpdir)
        initialize_config(tmpdir, env_prefix="MYAPP")

        initialize_config(tmpdir, env_prefix="MYAPP")  # same arguments: no-op
        initialize_config(
            tmpdir, env_prefix="MYAPP_"
        )  # same prefix, other spelling: no-op
        with pytest.raises(ValueError, match="env_prefix"):
            initialize_config(tmpdir, env_prefix="OTHER")
        with pytest.raises(ValueError, match="config_dir"):
            initialize_config(other_dir, env_prefix="MYAPP")
        with pytest.raises(ValueError, match="env_selectors"):
            initialize_config(tmpdir, env_prefix="MYAPP", env_selectors=("MYAPP_ENV",))


def test_initialize_config_defaults_export_unprefixed_names_from_the_app_env_overlay(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """With no arguments beyond the directory, the bridge exports bare names and APP_ENV picks the overlay.

    Why this test is important:
      - No prefix and the APP_ENV/ENVIRONMENT selectors are the documented defaults every consumer gets
        when it omits them; a regression there would load the wrong overlay or export names the
        unprefixed settings mixins never read.

    What it tests:
      - With APP_ENV=staging (and ENVIRONMENT=prod, which APP_ENV outranks), base + staging.yaml export
        as KEBRIDGE_SIZE=7 / KEBRIDGE_COLOR=red.
    """
    from techai_webutils.foundation.config.bridge import initialize_config

    monkeypatch.setenv("APP_ENV", "staging")
    monkeypatch.setenv("ENVIRONMENT", "prod")
    with TemporaryDirectory() as tmpdir:
        _write_widget_config(
            tmpdir,
            {"staging": "kebridge:\n  size: 7\n", "prod": "kebridge:\n  size: 9\n"},
        )
        try:
            initialize_config(tmpdir)

            assert os.environ.get("KEBRIDGE_SIZE") == "7"
            assert os.environ.get("KEBRIDGE_COLOR") == "red"
        finally:
            for key in ("KEBRIDGE_SIZE", "KEBRIDGE_COLOR"):
                os.environ.pop(key, None)


@pytest.mark.parametrize(
    ("environment", "expected_size"),
    [("prod", "9"), (None, "5")],
    ids=["environment-fallback", "dev-fallback"],
)
def test_initialize_config_falls_back_to_environment_then_dev(
    monkeypatch: pytest.MonkeyPatch, environment: str | None, expected_size: str
) -> None:
    """Without APP_ENV the overlay comes from ENVIRONMENT, and with neither set from dev.yaml.

    Why this test is important:
      - Deployments that set only ENVIRONMENT, and local runs that set nothing, rely on these fallbacks
        to load their overlay; losing either silently runs the service on base.yaml alone.

    What it tests:
      - APP_ENV unset + ENVIRONMENT=prod loads prod.yaml (size 9); neither set loads dev.yaml (size 5).
    """
    from techai_webutils.foundation.config.bridge import initialize_config

    monkeypatch.delenv("APP_ENV", raising=False)
    if environment is None:
        monkeypatch.delenv("ENVIRONMENT", raising=False)
    else:
        monkeypatch.setenv("ENVIRONMENT", environment)
    with TemporaryDirectory() as tmpdir:
        _write_widget_config(
            tmpdir, {"prod": "kebridge:\n  size: 9\n", "dev": "kebridge:\n  size: 5\n"}
        )
        try:
            initialize_config(tmpdir, env_prefix="MYAPP")

            assert os.environ.get("MYAPP_KEBRIDGE_SIZE") == expected_size
        finally:
            for key in ("MYAPP_KEBRIDGE_SIZE", "MYAPP_KEBRIDGE_COLOR"):
                os.environ.pop(key, None)


def test_initialize_config_logs_the_selected_overlay_and_warns_when_it_is_missing(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    """The bridge logs which overlay it selected and warns when a selected overlay file does not exist.

    Why this test is important:
      - A generic selector such as ENVIRONMENT may be set by other tooling to a name with no matching
        file (ENVIRONMENT=production beside prod.yaml). Skipping the overlay silently runs the service on
        base.yaml alone with nothing in the logs.

    What it tests:
      - With ENVIRONMENT=production and no production.yaml, a warning names the missing overlay file and
        the ENVIRONMENT selector; the base values are still exported.
    """
    import logging

    from techai_webutils.foundation.config.bridge import initialize_config

    monkeypatch.delenv("APP_ENV", raising=False)
    monkeypatch.setenv("ENVIRONMENT", "production")
    caplog.set_level(logging.INFO, logger="techai_webutils.foundation.config.bridge")
    with TemporaryDirectory() as tmpdir:
        _write_widget_config(tmpdir, {"prod": "kebridge:\n  size: 9\n"})
        try:
            initialize_config(tmpdir, env_prefix="MYAPP")

            warnings = [r for r in caplog.records if r.levelno == logging.WARNING]
            assert any(
                "production.yaml" in r.getMessage() and "ENVIRONMENT" in r.getMessage()
                for r in warnings
            )
            assert os.environ.get("MYAPP_KEBRIDGE_SIZE") == "3"
        finally:
            for key in ("MYAPP_KEBRIDGE_SIZE", "MYAPP_KEBRIDGE_COLOR"):
                os.environ.pop(key, None)


def test_initialize_config_strict_raises_on_malformed_yaml_instead_of_continuing() -> (
    None
):
    """strict=True turns an unreadable config into a startup error; the default still degrades to a warning.

    Why this test is important:
      - A YAML typo in a deployment otherwise starts the service on env vars and code defaults (stub
        kinds, localhost) instead of failing fast; strict mode lets a consumer that always ships YAML make
        that a hard error.

    What it tests:
      - initialize_config(strict=True) on a malformed base.yaml raises the YAML error, and on a missing
        directory raises FileNotFoundError; the default (strict=False) returns without raising.
    """
    import yaml

    from techai_webutils.foundation.config.bridge import initialize_config, reset_config

    with TemporaryDirectory() as tmpdir:
        (Path(tmpdir) / "base.yaml").write_text("kebridge: [unclosed\n")

        with pytest.raises(yaml.YAMLError):
            initialize_config(tmpdir, env_prefix="MYAPP", strict=True)
        reset_config()
        with pytest.raises(FileNotFoundError):
            initialize_config(Path(tmpdir) / "absent", env_prefix="MYAPP", strict=True)
        reset_config()

        initialize_config(tmpdir, env_prefix="MYAPP")  # default: warn and continue
