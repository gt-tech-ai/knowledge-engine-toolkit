"""Tests for YamlConfigLoader."""

import tempfile
from dataclasses import dataclass
from pathlib import Path

from techai_webutils.foundation.config.config_loader import YamlConfigLoader


class TestYamlConfigLoader:
    """Test suite for YamlConfigLoader YAML parsing and typed access."""

    def test_get_string(self) -> None:
        """Test that get_string retrieves a nested string value using dot notation.

        **Why this test is important:**
          - Dotted key access is the primary API for reading configuration values
          - Services use get_string for host names, connection strings, and feature flags
          - Incorrect key resolution would cause services to start with wrong settings

        **What it tests:**
          - get_string("app.name") returns "test-app" from nested YAML
        """
        loader = self._make_loader({"app": {"name": "test-app"}})
        assert loader.get_string("app.name") == "test-app"

    def test_get_string_missing(self) -> None:
        """Test that get_string returns an empty string for missing keys.

        **Why this test is important:**
          - Missing keys are common during incremental config rollouts
          - Returning empty string rather than raising enables graceful defaults
          - Callers can use truthiness checks on the return value

        **What it tests:**
          - get_string("missing") returns "" when key does not exist
        """
        loader = self._make_loader({})
        assert not loader.get_string("missing")

    def test_get_int(self) -> None:
        """Test that get_int retrieves an integer value by key.

        **Why this test is important:**
          - Ports, timeouts, and buffer sizes are configured as integers
          - Type coercion must produce correct int values from YAML scalar nodes
          - Incorrect parsing would cause bind failures or invalid timeouts

        **What it tests:**
          - get_int("port") returns 8080 from YAML
        """
        loader = self._make_loader({"port": 8080})
        assert loader.get_int("port") == 8080

    def test_get_int_missing(self) -> None:
        """Test that get_int returns zero for missing keys.

        **Why this test is important:**
          - Zero is the natural default for numeric config values
          - Callers can detect missing config by checking for zero
          - Raising on missing keys would break services during config migration

        **What it tests:**
          - get_int("missing") returns 0 when key does not exist
        """
        loader = self._make_loader({})
        assert loader.get_int("missing") == 0

    def test_get_bool(self) -> None:
        """Test that get_bool retrieves a boolean value by key.

        **Why this test is important:**
          - Feature flags and debug toggles depend on correct boolean parsing
          - YAML booleans (true/false) must map to Python True/False
          - Incorrect parsing could enable debug mode in production

        **What it tests:**
          - get_bool("debug") returns True from YAML
        """
        loader = self._make_loader({"debug": True})
        assert loader.get_bool("debug") is True

    def test_get_bool_missing(self) -> None:
        """Test that get_bool returns False for missing keys.

        **Why this test is important:**
          - False is the safe default for feature flags (disabled by default)
          - Missing boolean keys must not enable features accidentally
          - Consistent zero-value semantics across get_string, get_int, get_bool

        **What it tests:**
          - get_bool("missing") returns False when key does not exist
        """
        loader = self._make_loader({})
        assert loader.get_bool("missing") is False

    def test_get_bool_string_true(self) -> None:
        """Test that get_bool coerces the string "true" to boolean True.

        **Why this test is important:**
          - Environment variable overrides often inject string "true" instead of YAML boolean
          - Docker Compose and K8s ConfigMaps pass all values as strings
          - Without string coercion, env-var-based config would always evaluate to False

        **What it tests:**
          - get_bool("enabled") returns True when YAML value is the string "true"
        """
        loader = self._make_loader({"enabled": "true"})
        assert loader.get_bool("enabled") is True

    def test_get_raw(self) -> None:
        """Test that get retrieves the raw value without type coercion.

        **Why this test is important:**
          - Complex config values (lists, dicts) need raw access for custom parsing
          - The generic get() method is the escape hatch when typed accessors are insufficient
          - YAML lists are used for allowed origins, feature flag variants, etc.

        **What it tests:**
          - get("items") returns the list [1, 2, 3] from YAML
        """
        loader = self._make_loader({"items": [1, 2, 3]})
        assert loader.get("items") == [1, 2, 3]

    def test_get_missing(self) -> None:
        """Test that get returns None for a non-existent key.

        **Why this test is important:**
          - Raw get() must return None (not raise) for missing keys
          - Callers use None checks to provide defaults or skip optional config
          - Consistent with dict.get() semantics that Python developers expect

        **What it tests:**
          - get("nonexistent") returns None
        """
        loader = self._make_loader({})
        assert loader.get("nonexistent") is None

    def test_nested_dotted_key(self) -> None:
        """Test that dotted key notation traverses nested YAML structures.

        **Why this test is important:**
          - Configuration is deeply nested (db.host, db.port, auth.jwt.secret)
          - Dotted key traversal must resolve through multiple nesting levels
          - This is the primary access pattern used by all service config readers

        **What it tests:**
          - get_string("db.host") returns "localhost"
          - get_int("db.port") returns 5432
        """
        loader = self._make_loader({"db": {"host": "localhost", "port": 5432}})
        assert loader.get_string("db.host") == "localhost"
        assert loader.get_int("db.port") == 5432

    def test_unmarshal(self) -> None:
        """Test that unmarshal maps YAML data into a typed Python object.

        **Why this test is important:**
          - Typed config objects enable IDE autocompletion and static type checking
          - Unmarshaling catches type mismatches at startup instead of at runtime
          - Services use dataclass/pydantic models for validated configuration

        **What it tests:**
          - cfg.host equals "localhost" from YAML
          - cfg.port equals 5432 from YAML
        """
        loader = self._make_loader({"host": "localhost", "port": 5432})

        @dataclass
        class Config:
            host: str
            port: int

        cfg = loader.unmarshal(Config)
        assert cfg.host == "localhost"
        assert cfg.port == 5432

    @staticmethod
    def _make_loader(data: dict) -> YamlConfigLoader:  # type: ignore[type-arg]
        """Create a YamlConfigLoader from a dict by writing a temp YAML file."""
        import yaml

        with tempfile.TemporaryDirectory() as tmpdir:
            config_file = Path(tmpdir) / "base.yaml"
            config_file.write_text(yaml.dump(data))
            return YamlConfigLoader(tmpdir)
