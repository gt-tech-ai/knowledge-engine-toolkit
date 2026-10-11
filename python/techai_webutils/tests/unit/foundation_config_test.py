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

from techai_webutils.foundation.config.loader import load_config


class TestConfigLoader:
    """Test suite for config loader YAML parsing and overlay merging."""

    def test_load_base_config(self, tmp_path: Path) -> None:
        """Test that load_config parses a base config.yaml and returns its values.

        **Why this test is important:**
          - Configuration loading is the first step in every service bootstrap
          - Incorrect YAML parsing would cause services to start with wrong settings
          - Value types (string, int) must be preserved through the parser

        **What it tests:**
          - cfg["app"]["name"] equals "test-app"
          - cfg["app"]["port"] equals integer 8080
        """
        config_file = tmp_path / "base.yaml"
        config_file.write_text("app:\n  name: test-app\n  port: 8080\n")

        cfg = load_config(config_dir=str(tmp_path))
        assert cfg["app"]["name"] == "test-app"
        assert cfg["app"]["port"] == 8080

    def test_environment_overlay_merges(self, tmp_path: Path) -> None:
        """Test that load_config deep-merges environment-specific YAML over the base.

        **Why this test is important:**
          - Services run in multiple environments (dev, staging, prod) with different settings
          - Deep merge must override specific keys without removing sibling keys
          - Additive keys in overlays must appear in the final config

        **What it tests:**
          - cfg["app"]["name"] is unchanged from base ("base")
          - cfg["app"]["debug"] is overridden to True by dev overlay
          - cfg["app"]["log_level"] is added by dev overlay ("debug")
        """
        config_file = tmp_path / "base.yaml"
        config_file.write_text("app:\n  name: base\n  port: 8080\n  debug: false\n")

        (tmp_path / "dev.yaml").write_text("app:\n  debug: true\n  log_level: debug\n")

        cfg = load_config(config_dir=str(tmp_path), environment="dev")
        assert cfg["app"]["name"] == "base"  # unchanged
        assert cfg["app"]["debug"] is True  # overridden
        assert cfg["app"]["log_level"] == "debug"  # added

    def test_env_var_substitution(self, tmp_path: Path) -> None:
        """Test that load_config replaces ${VAR} placeholders with environment variable values.

        **Why this test is important:**
          - Secrets and host-specific values must not be hardcoded in config files
          - 12-factor app methodology requires configuration from the environment
          - Incorrect substitution would connect services to wrong databases or endpoints

        **What it tests:**
          - cfg["db"]["host"] equals the DB_HOST environment variable value
          - cfg["db"]["port"] equals the DB_PORT environment variable value
        """
        config_file = tmp_path / "base.yaml"
        config_file.write_text("db:\n  host: ${DB_HOST}\n  port: ${DB_PORT}\n")

        os.environ["DB_HOST"] = "localhost"
        os.environ["DB_PORT"] = "5432"
        try:
            cfg = load_config(config_dir=str(tmp_path))
            assert cfg["db"]["host"] == "localhost"
            assert cfg["db"]["port"] == "5432"
        finally:
            del os.environ["DB_HOST"]
            del os.environ["DB_PORT"]

    def test_env_var_with_default(self, tmp_path: Path) -> None:
        """Test that load_config uses the default value from ${VAR:-default} syntax.

        **Why this test is important:**
          - Default values enable services to start without every env var being set
          - Development environments benefit from sensible defaults without .env files
          - Missing var without default would crash the service on startup

        **What it tests:**
          - cfg["app"]["host"] equals "0.0.0.0" (the default) when MISSING_VAR is unset
        """
        config_file = tmp_path / "base.yaml"
        config_file.write_text("app:\n  host: ${MISSING_VAR:-0.0.0.0}\n")

        cfg = load_config(config_dir=str(tmp_path))
        assert cfg["app"]["host"] == "0.0.0.0"

    def test_secrets_overlay(self, tmp_path: Path) -> None:
        """Test that load_config applies secrets.yaml values over base config.

        **Why this test is important:**
          - Secrets must override base config to prevent accidental use of placeholder values
          - secrets.yaml is typically injected by Kubernetes secrets or vault in production
          - Incorrect precedence would expose placeholder passwords in production

        **What it tests:**
          - cfg["db"]["password"] equals "super-secret" from secrets.yaml, not "changeme" from base
        """
        config_file = tmp_path / "base.yaml"
        config_file.write_text("db:\n  password: changeme\n")
        secrets_file = tmp_path / "secrets.yaml"
        secrets_file.write_text("db:\n  password: super-secret\n")

        cfg = load_config(config_dir=str(tmp_path))
        assert cfg["db"]["password"] == "super-secret"

    def test_missing_config_dir_raises(self) -> None:
        """Test that load_config raises FileNotFoundError for missing config directory.

        **Why this test is important:**
          - Services must fail fast with a clear error if configuration is missing
          - Silent fallback to empty config would cause subtle runtime failures
          - Deployment pipelines depend on this check to catch misconfiguration early

        **What it tests:**
          - FileNotFoundError is raised when config directory does not exist
        """
        with pytest.raises(FileNotFoundError):
            load_config(config_dir="/nonexistent/path")
