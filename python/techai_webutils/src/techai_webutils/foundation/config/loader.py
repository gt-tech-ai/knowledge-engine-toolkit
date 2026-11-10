"""Hierarchical YAML config loader with env var substitution.

Load order (deep merge):
  1. base.yaml (base)
  2. {ENV}.yaml (environment overlay)
  3. secrets.yaml (secrets overlay)
  4. Environment variables (${VAR} substitution with ${VAR:-default} support)
"""

from __future__ import annotations

import os
import re
from pathlib import Path
from typing import cast

import yaml

from techai_webutils.core.errors import AppFileNotFoundError

_ENV_VAR_PATTERN = re.compile(r"\$\{([^}]+)\}")
"""Matches ``${VAR}`` / ``${VAR:-default}`` placeholders substituted in config values."""


def _substitute_env_vars(value: object) -> object:
    """Recursively substitute ``${VAR}`` and ``${VAR:-default}`` in config values.

    Walks dicts, lists, and strings. Non-string leaves are returned unchanged.
    Environment variables that are not set and have no default are left as-is.

    Args:
        value: A config value.

    Returns:
        The value with variables substituted.

    """
    if isinstance(value, str):
        return _ENV_VAR_PATTERN.sub(_resolve_var, value)
    if isinstance(value, dict):
        return {
            k: _substitute_env_vars(v)
            for k, v in cast("dict[object, object]", value).items()
        }
    if isinstance(value, list):
        return [_substitute_env_vars(item) for item in cast("list[object]", value)]
    return value


def _resolve_var(match: re.Match[str]) -> str:
    """Resolve a single ``${VAR}`` or ``${VAR:-default}`` regex match.

    If the expression contains ``:-``, the text after it is used as a
    fallback when the environment variable is unset. Without a default,
    an unset variable is left as the original ``${VAR}`` literal.

    Args:
        match: The ``${VAR}`` match.

    Returns:
        A single ``${VAR}`` or ``${VAR:-default}`` regex match.

    """
    expr = match.group(1)
    if ":-" in expr:
        var_name, default = expr.split(":-", 1)
        return os.environ.get(var_name, default)
    return os.environ.get(expr, match.group(0))


def _read_yaml(path: Path) -> dict[str, object]:
    """Return the mapping a YAML file holds, empty when it is absent or empty.

    Args:
        path: The YAML file.

    Returns:
        The file's top-level mapping.

    """
    if not path.exists():
        return {}
    with path.open(encoding="utf-8") as f:
        return cast("dict[str, object] | None", yaml.safe_load(f)) or {}


def _deep_merge(
    base: dict[str, object], override: dict[str, object]
) -> dict[str, object]:
    """Deep-merge *override* into *base*, returning a new dict.

    Nested dicts are merged recursively. For all other types the
    *override* value wins. Neither input dict is mutated.

    Args:
        base: The lower config.
        override: The overriding config.

    Returns:
        The merged dict.

    """
    result = base.copy()
    for key, value in override.items():
        current = result.get(key)
        if isinstance(current, dict) and isinstance(value, dict):
            result[key] = _deep_merge(
                cast("dict[str, object]", current), cast("dict[str, object]", value)
            )
        else:
            result[key] = value
    return result


def load_config(
    config_dir: str,
    environment: str | None = None,
) -> dict[str, object]:
    """Load and merge YAML configuration files.

    Args:
        config_dir: Path to directory containing base.yaml.
        environment: Optional environment name (loads {env}.yaml).

    Returns:
        Merged configuration dictionary with env vars substituted.

    A callee raises ``FileNotFoundError``: If config_dir doesn't exist.

    Raises:
        AppFileNotFoundError: Config directory not found: ``config_dir``.

    """
    config_path = Path(config_dir)
    if not config_path.exists():
        msg = f"Config directory not found: {config_dir}"
        raise AppFileNotFoundError(msg)

    # Layer 1: base config, then the environment overlay, then the secrets overlay
    config = _read_yaml(config_path / "base.yaml")
    if environment:
        config = _deep_merge(config, _read_yaml(config_path / f"{environment}.yaml"))
    config = _deep_merge(config, _read_yaml(config_path / "secrets.yaml"))

    # Layer 4: environment variable substitution
    return cast("dict[str, object]", _substitute_env_vars(config))
