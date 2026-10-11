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
from typing import Any, cast

import yaml

from techai_webutils.core.errors import AppFileNotFoundError

_ENV_VAR_PATTERN = re.compile(r"\$\{([^}]+)\}")
"""Matches ``${VAR}`` / ``${VAR:-default}`` placeholders substituted in config values."""


def _substitute_env_vars(value: object) -> object:
    """Recursively substitute ``${VAR}`` and ``${VAR:-default}`` in config values.

    Walks dicts, lists, and strings. Non-string leaves are returned unchanged.
    Environment variables that are not set and have no default are left as-is.
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
    """
    expr = match.group(1)
    if ":-" in expr:
        var_name, default = expr.split(":-", 1)
        return os.environ.get(var_name, default)
    return os.environ.get(expr, match.group(0))


def _deep_merge(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    """Deep-merge *override* into *base*, returning a new dict.

    Nested dicts are merged recursively. For all other types the
    *override* value wins. Neither input dict is mutated.
    """
    result = base.copy()
    for key, value in override.items():
        if key in result and isinstance(result[key], dict) and isinstance(value, dict):
            result[key] = _deep_merge(result[key], cast("dict[str, Any]", value))
        else:
            result[key] = value
    return result


def load_config(
    config_dir: str,
    environment: str | None = None,
) -> dict[str, Any]:
    """Load and merge YAML configuration files.

    Args:
        config_dir: Path to directory containing base.yaml.
        environment: Optional environment name (loads {env}.yaml).

    Returns:
        Merged configuration dictionary with env vars substituted.

    Raises:
        FileNotFoundError: If config_dir doesn't exist.

    """
    config_path = Path(config_dir)
    if not config_path.exists():
        msg = f"Config directory not found: {config_dir}"
        raise AppFileNotFoundError(msg)

    # Layer 1: base config
    base_file = config_path / "base.yaml"
    config: dict[str, Any] = {}
    if base_file.exists():
        with base_file.open() as f:
            loaded = yaml.safe_load(f)
            if loaded:
                config = loaded

    # Layer 2: environment overlay
    if environment:
        env_file = config_path / f"{environment}.yaml"
        if env_file.exists():
            with env_file.open() as f:
                env_config = yaml.safe_load(f)
                if env_config:
                    config = _deep_merge(config, env_config)

    # Layer 3: secrets overlay
    secrets_file = config_path / "secrets.yaml"
    if secrets_file.exists():
        with secrets_file.open() as f:
            secrets_config = yaml.safe_load(f)
            if secrets_config:
                config = _deep_merge(config, secrets_config)

    # Layer 4: environment variable substitution
    return _substitute_env_vars(config)  # type: ignore[assignment]
