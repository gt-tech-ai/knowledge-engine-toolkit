"""Unit tests for configuration loading.

This file tests that the config loader correctly parses YAML files, deep-merges
environment overlays, substitutes environment variables (with default fallbacks),
and applies secrets overlays in the expected precedence order.

# Test Coverage

The tests cover:
  - Base config: YAML parsing and value access
  - Environment overlay: deep-merge of environment-specific overrides
  - Variable substitution: ${VAR} environment variable replacement
  - Default fallback: ${VAR:-default} syntax when env var is unset
  - Secrets overlay: precedence of secrets.yaml over base and environment configs
  - Error handling: FileNotFoundError for missing config directory

# Test Structure

Tests use pytest class-based organization with tmp_path fixtures for filesystem
isolation. Environment variables are set/cleaned in try/finally blocks to avoid
test pollution.

# Running Tests

Run with: pytest tests/python/test_foundation/test_config.py
"""

import os
from pathlib import Path

import pytest

from techai_webutils.core.errors import AppFileNotFoundError, InternalError
from techai_webutils.foundation.config.loader import load_config
from tests.json_path import dig

_ALL_INTERFACES = ".".join(["0"] * 4)
_FAKE_CRED_2 = "super-secret"

_EXPECTED_CFG = 8080


class TestConfigLoader:
    """Test suite for config loader YAML parsing and overlay merging."""

    @staticmethod
    def test_load_base_config(tmp_path: Path) -> None:
        """Test that load_config parses a base config.yaml and returns its values.

        **Why this test is important:**
          - Configuration loading is the first step in every service bootstrap
          - Incorrect YAML parsing would cause services to start with wrong settings
          - Value types (string, int) must be preserved through the parser

        **What it tests:**
          - cfg["app"]["name"] equals "test-app"
          - cfg["app"]["port"] equals integer 8080

        Args:
            tmp_path: A fresh directory for the test.

        """
        config_file = tmp_path / "base.yaml"
        _ = config_file.write_text("app:\n  name: test-app\n  port: 8080\n")

        cfg = load_config(config_dir=str(tmp_path))
        assert dig(cfg, "app", "name") == "test-app"
        assert dig(cfg, "app", "port") == _EXPECTED_CFG

    @staticmethod
    def test_environment_overlay_merges(tmp_path: Path) -> None:
        """Test that load_config deep-merges environment-specific YAML over the base.

        **Why this test is important:**
          - Services run in multiple environments (dev, staging, prod) with different
            settings
          - Deep merge must override specific keys without removing sibling keys
          - Additive keys in overlays must appear in the final config

        **What it tests:**
          - cfg["app"]["name"] is unchanged from base ("base")
          - cfg["app"]["debug"] is overridden to True by dev overlay
          - cfg["app"]["log_level"] is added by dev overlay ("debug")

        Args:
            tmp_path: A fresh directory for the test.

        """
        config_file = tmp_path / "base.yaml"
        _ = config_file.write_text("app:\n  name: base\n  port: 8080\n  debug: false\n")

        _ = (tmp_path / "dev.yaml").write_text(
            "app:\n  debug: true\n  log_level: debug\n"
        )

        cfg = load_config(config_dir=str(tmp_path), environment="dev")
        assert dig(cfg, "app", "name") == "base"  # unchanged
        assert dig(cfg, "app", "debug") is True  # overridden
        assert dig(cfg, "app", "log_level") == "debug"  # added

    @staticmethod
    def test_env_var_substitution(tmp_path: Path) -> None:
        """Test that load_config replaces ${VAR} placeholders.

        They become environment variable values.

        **Why this test is important:**
          - Secrets and host-specific values must not be hardcoded in config files
          - 12-factor app methodology requires configuration from the environment
          - Incorrect substitution would connect services to wrong databases or endpoints

        **What it tests:**
          - cfg["db"]["host"] equals the DB_HOST environment variable value
          - cfg["db"]["port"] equals the DB_PORT environment variable value

        Args:
            tmp_path: A fresh directory for the test.

        """
        config_file = tmp_path / "base.yaml"
        _ = config_file.write_text("db:\n  host: ${DB_HOST}\n  port: ${DB_PORT}\n")

        os.environ["DB_HOST"] = "localhost"
        os.environ["DB_PORT"] = "5432"
        try:
            cfg = load_config(config_dir=str(tmp_path))
            assert dig(cfg, "db", "host") == "localhost"
            assert dig(cfg, "db", "port") == "5432"
        finally:
            del os.environ["DB_HOST"]
            del os.environ["DB_PORT"]

    @staticmethod
    def test_env_var_with_default(tmp_path: Path) -> None:
        """Test that load_config uses the default value from ${VAR:-default} syntax.

        **Why this test is important:**
          - Default values enable services to start without every env var being set
          - Development environments benefit from sensible defaults without .env files
          - Missing var without default would crash the service on startup

        **What it tests:**
          - cfg["app"]["host"] equals "0.0.0.0" (the default) when MISSING_VAR is unset

        Args:
            tmp_path: A fresh directory for the test.

        """
        config_file = tmp_path / "base.yaml"
        _ = config_file.write_text("app:\n  host: ${MISSING_VAR:-0.0.0.0}\n")

        cfg = load_config(config_dir=str(tmp_path))
        assert dig(cfg, "app", "host") == _ALL_INTERFACES

    @staticmethod
    def test_secrets_overlay(tmp_path: Path) -> None:
        """Test that load_config applies secrets.yaml values over base config.

        **Why this test is important:**
          - Secrets must override base config to prevent accidental use of placeholder
            values
          - secrets.yaml is typically injected by Kubernetes secrets or vault in
            production
          - Incorrect precedence would expose placeholder passwords in production

        **What it tests:**
          - cfg["db"]["password"] equals "super-secret" from secrets.yaml, not "changeme"
            from base

        Args:
            tmp_path: A fresh directory for the test.

        """
        config_file = tmp_path / "base.yaml"
        _ = config_file.write_text("db:\n  password: changeme\n")
        secrets_file = tmp_path / "secrets.yaml"
        _ = secrets_file.write_text("db:\n  password: super-secret\n")

        cfg = load_config(config_dir=str(tmp_path))
        assert dig(cfg, "db", "password") == _FAKE_CRED_2

    @staticmethod
    def test_missing_config_dir_raises() -> None:
        """Test that load_config raises FileNotFoundError for missing config directory.

        **Why this test is important:**
          - Services must fail fast with a clear error if configuration is missing
          - Silent fallback to empty config would cause subtle runtime failures
          - Deployment pipelines depend on this check to catch misconfiguration early

        **What it tests:**
          - FileNotFoundError is raised when config directory does not exist
        """
        with pytest.raises(AppFileNotFoundError):
            _ = load_config(config_dir="/nonexistent/path")

    @staticmethod
    def test_secret_tree_merges_key_per_file(tmp_path: Path) -> None:
        """Test that each file under secrets.d/ merges at the key its path names.

        **Why this test is important:**
          - BYOC workloads receive credentials as a key-per-file tree projected from a
            Secret; a file beside base.yaml is useless unless the loader reads
            it, and a value with a stray trailing newline would authenticate as wrong.

        **What it tests:**
          - secrets.d/database/password sets database.password with its one trailing
            newline trimmed, and secrets.d/redis/password sets redis.password.

        Args:
            tmp_path: A fresh directory for the test.

        """
        _ = (tmp_path / "base.yaml").write_text("database:\n  host: localhost\n")
        tree = tmp_path / "secrets.d"
        _ = (tree / "database").mkdir(parents=True)
        _ = (tree / "database" / "password").write_text("s3cret\n")
        _ = (tree / "redis").mkdir(parents=True)
        _ = (tree / "redis" / "password").write_text("r3dis")

        cfg = load_config(config_dir=str(tmp_path))

        assert dig(cfg, "database", "password") == "s3cret"
        assert dig(cfg, "redis", "password") == "r3dis"

    @staticmethod
    def test_secret_tree_missing_dir_is_no_layer(tmp_path: Path) -> None:
        """Test that a config dir with no secrets.d/ loads unchanged.

        **Why this test is important:**
          - Compose and local runs have no secrets.d; the new layer must be strictly
            additive and never fail or alter a boot that worked before.

        **What it tests:**
          - With no secrets.d/, load_config succeeds and keeps the base value.

        Args:
            tmp_path: A fresh directory for the test.

        """
        _ = (tmp_path / "base.yaml").write_text("database:\n  host: localhost\n")

        cfg = load_config(config_dir=str(tmp_path))

        assert dig(cfg, "database", "host") == "localhost"

    @staticmethod
    def test_secret_tree_runs_before_substitution(tmp_path: Path) -> None:
        """Test that ${VAR} substitution still runs after the secrets.d merge.

        **Why this test is important:**
          - The tree is a layer below substitution; adding it must not break the existing
            ${VAR} behavior Compose and local runs rely on.

        **What it tests:**
          - A base value "${SUBST_PROBE}" still substitutes from the environment even
            when a secrets.d file is also present.

        Args:
            tmp_path: A fresh directory for the test.

        """
        _ = (tmp_path / "base.yaml").write_text('api:\n  region: "${SUBST_PROBE}"\n')
        tree = tmp_path / "secrets.d"
        _ = (tree / "database").mkdir(parents=True)
        _ = (tree / "database" / "password").write_text("s3cret")
        os.environ["SUBST_PROBE"] = "from-env"
        try:
            cfg = load_config(config_dir=str(tmp_path))
        finally:
            del os.environ["SUBST_PROBE"]

        assert dig(cfg, "api", "region") == "from-env"
        assert dig(cfg, "database", "password") == "s3cret"

    @staticmethod
    def test_secret_tree_resolves_projected_symlink(tmp_path: Path) -> None:
        """Test that a secrets.d symlink into a projected volume's ..data dir is walked.

        **Why this test is important:**
          - A projected Secret volume's entries are symlinks into kubelet's ..data dir;
            reading the symlink itself as a file would misread every credential.

        **What it tests:**
          - With secrets.d -> ..data (a real dir holding database/password), the value
            merges.

        Args:
            tmp_path: A fresh directory for the test.

        """
        _ = (tmp_path / "base.yaml").write_text("database:\n  host: localhost\n")
        data = tmp_path / "..data"
        _ = (data / "database").mkdir(parents=True)
        _ = (data / "database" / "password").write_text("linked\n")
        (tmp_path / "secrets.d").symlink_to("..data")

        cfg = load_config(config_dir=str(tmp_path))

        assert dig(cfg, "database", "password") == "linked"

    @staticmethod
    def test_secret_tree_unreadable_file_raises(tmp_path: Path) -> None:
        """Test that an unreadable credential file fails the boot with a coded error.

        **Why this test is important:**
          - A credential the loader cannot read must stop the process loudly; booting with
            an empty password would fail far from the cause or authenticate as a default.

        **What it tests:**
          - A secrets.d/database/password with mode 0o000 raises InternalError.

        Args:
            tmp_path: A fresh directory for the test.

        """
        if os.geteuid() == 0:
            pytest.skip("root bypasses file mode bits")

        _ = (tmp_path / "base.yaml").write_text("database:\n  host: localhost\n")
        tree = tmp_path / "secrets.d"
        _ = (tree / "database").mkdir(parents=True)
        secret = tree / "database" / "password"
        _ = secret.write_text("s3cret")
        secret.chmod(0o000)

        with pytest.raises(InternalError):
            _ = load_config(config_dir=str(tmp_path))
