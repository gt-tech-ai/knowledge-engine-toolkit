"""YAML-based ConfigLoader implementation.

Wraps the existing ``load_config`` function into the ``ConfigLoader`` ABC
contract from core/interfaces.
"""

from __future__ import annotations

from typing import TypeVar, cast

from techai_webutils.core.interfaces.config_loader import ConfigLoader
from techai_webutils.foundation.config.loader import load_config

T = TypeVar("T")


class YamlConfigLoader(ConfigLoader):
    """ConfigLoader backed by hierarchical YAML files.

    Args:
        config_dir: Path to directory containing base.yaml.
        environment: Optional environment name for overlay loading.

    """

    def __init__(self, config_dir: str, environment: str | None = None) -> None:
        """Eagerly load and merge the YAML hierarchy into an in-memory dict.

        Args:
            config_dir: Path to the directory holding ``base.yaml``.
            environment: Optional environment name selecting an overlay
                (``{environment}.yaml``) merged over the base.

        """
        self._data = load_config(config_dir, environment)

    def get(self, key: str) -> object | None:
        """Return the raw value for a dotted key, or None if not set."""
        return self._resolve(key)

    def get_string(self, key: str) -> str:
        """Return the string value for a key."""
        val = self._resolve(key)
        return str(val) if val is not None else ""

    def get_int(self, key: str) -> int:
        """Return the int value for a key."""
        val = self._resolve(key)
        if val is None:
            return 0
        if isinstance(val, int):
            return val
        return int(str(val))

    def get_bool(self, key: str) -> bool:
        """Return the bool value for a key."""
        val = self._resolve(key)
        if val is None:
            return False
        if isinstance(val, bool):
            return val
        if isinstance(val, str):
            return val.lower() in {"true", "1", "yes"}
        return bool(val)

    def unmarshal(self, target_type: type[T]) -> T:
        """Decode the full configuration into the target type.

        Works with Pydantic models and dataclasses that accept ``**kwargs``.
        """
        return target_type(**self._data)

    def _resolve(self, key: str) -> object | None:
        """Walk dotted key path through nested dicts."""
        parts = key.split(".")
        current: object = self._data
        for part in parts:
            if not isinstance(current, dict):
                return None
            current = cast("dict[str, object]", current).get(part)
            if current is None:
                return None
        return current
