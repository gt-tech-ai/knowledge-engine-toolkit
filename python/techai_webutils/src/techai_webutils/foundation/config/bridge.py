"""YAML-to-environment variable bridge for Pydantic Settings integration.

This module bridges the gap between YAML configuration files and Pydantic Settings.
It loads the merged YAML configuration (``base.yaml`` + ``{env}.yaml`` overlay +
``secrets.yaml``) and exports every scalar leaf as a ``<PREFIX>_*`` environment variable,
allowing Pydantic Settings to consume them via its ``env_prefix`` behavior. The prefix
(default: none) and the env vars that select the overlay are the caller's
(``initialize_config(env_prefix=..., env_selectors=...)``).

Why this design:
  - Pydantic Settings reads from environment variables by default.
  - YAML provides the hierarchical, environment-layered config the Go services also use.
  - Exporting the whole tree as ``<PREFIX>_*`` env defaults mirrors Viper's ``AutomaticEnv`` +
    ``SetEnvPrefix`` in Go: the YAML supplies defaults, real env vars still win.
  - Environment variables always take precedence (12-factor app principle).
  - Idempotency ensures safe multi-call scenarios (tests, multi-module imports).

The env-var name for a YAML path is the dotted path upper-snake-cased behind the prefix
(``widget.size`` -> ``MYAPP_WIDGET_SIZE`` with prefix ``MYAPP``), matching the Pydantic field
names exactly. The prefix may be passed with or without its trailing underscore: ``MYAPP`` and
``MYAPP_`` both export ``MYAPP_*``, so a consumer can pass its settings' ``env_prefix`` verbatim.
Flattening the whole tree (rather than a hand-maintained path map) means a new config section is
exported without an edit here.

With no prefix the bridge writes bare names (``aws.region`` -> ``AWS_REGION``, ``debug`` ->
``DEBUG``) into ``os.environ``, where other libraries (boto, logging setups) and child processes also
read them, and ambient variables with those names (e.g. Kubernetes service links such as
``REDIS_PORT=tcp://...``) shadow the YAML. Prefer a consumer-specific prefix in deployments.

Usage:
    from techai_webutils.foundation.config.bridge import initialize_config

    # Early in application startup (before creating Settings instances)
    initialize_config("/path/to/config", env_prefix="MYAPP")

    # Now Pydantic Settings (env_prefix="MYAPP_") reads the MYAPP_* env vars set from YAML
    settings = MyAppSettings()
"""

import logging
import os
import threading
from pathlib import Path
from typing import Any, cast

from techai_webutils.core.errors import AppFileNotFoundError, AppValueError
from techai_webutils.foundation.config.loader import load_config

logger = logging.getLogger(__name__)

# Sentinel flag + lock ensuring initialize_config() loads the YAML exactly once, thread-safely
# (the former unlocked check-then-set was safe only if init preceded any thread spawn).
_initialized = False
_init_lock = threading.Lock()
_initialized_with: tuple[str, str, tuple[str, ...]] | None = None
"""The normalised ``(config_dir, env_prefix, env_selectors)`` of the call that initialized the config."""


def _flatten_config(config: dict[str, Any], prefix: str = "") -> list[tuple[str, str]]:
    """Flatten a nested config dict into ``(<PREFIX>_<UPPER_SNAKE>, value)`` pairs.

    Nested dicts extend the prefix (``a.b.c`` -> ``<PREFIX>_A_B_C``, or ``A_B_C`` with no prefix),
    which matches the Pydantic Settings field names under the same ``env_prefix``. Scalar leaves (str/int/
    float/bool) are stringified; ``None`` and list/sequence leaves are skipped — they
    are not representable as a single env-var override through this bridge, and no
    settings field reads one. Returns a flat list (not a generator) so the
    traversal is eager and easy to reason about at the call site.
    """
    items: list[tuple[str, str]] = []
    for key, value in config.items():
        env_key = f"{prefix}_{key.upper()}" if prefix else key.upper()
        if isinstance(value, dict):
            items.extend(_flatten_config(cast("dict[str, Any]", value), env_key))
        elif isinstance(value, bool):
            # bool is an int subclass — handle before the int/float/str branch so it
            # stringifies as "True"/"False" (Pydantic parses either casing).
            items.append((env_key, str(value)))
        elif isinstance(value, (str, int, float)):
            items.append((env_key, str(value)))
        # None and lists are intentionally skipped.
    return items


_DEFAULT_ENV_SELECTORS = ("APP_ENV", "ENVIRONMENT")
"""Env vars consulted, in order, to select the ``{env}.yaml`` overlay when a caller names none."""


def _normalize_prefix(prefix: str) -> str:
    """Drop the prefix's trailing underscores, so ``MYAPP`` and ``MYAPP_`` both export ``MYAPP_*``."""
    return prefix.rstrip("_")


def apply_yaml_defaults(config: dict[str, Any], prefix: str = "") -> None:
    """Export merged YAML values as ``<PREFIX>_*`` environment variables.

    Every scalar leaf of the config tree becomes a ``<PREFIX>_<UPPER_SNAKE>`` env var, but
    only when that var is NOT already set — so real environment variables (e.g. secrets
    injected by the deployment) take precedence over YAML values (12-factor principle).

    With an empty prefix the names are bare (``AWS_REGION``, ``DEBUG``) and land in the whole
    process environment, visible to other libraries and child processes; prefer a prefix.

    Args:
        config: Merged configuration dictionary loaded from YAML.
        prefix: Env-var prefix, with or without its trailing underscore (``MYAPP`` and ``MYAPP_``
            both export ``MYAPP_*``), so the consumer's settings ``env_prefix`` can be passed as is
            (default: none — bare names).

    """
    for env_var, value in _flatten_config(config, _normalize_prefix(prefix)):
        # Skip if env var already set (env vars take precedence over YAML)
        if env_var in os.environ:
            continue
        os.environ[env_var] = value


def _resolve_environment(selectors: tuple[str, ...]) -> tuple[str, str | None]:
    """Resolve the overlay environment name for ``{env}.yaml`` selection, and the variable that chose it.

    Returns the first non-empty value among ``selectors`` (by default ``APP_ENV``, then
    ``ENVIRONMENT``) with that selector's name, or ``("dev", None)`` so local runs load
    ``base.yaml`` + ``dev.yaml``.
    """
    for name in selectors:
        if value := os.environ.get(name):
            return value, name
    return "dev", None


def initialize_config(
    config_dir: str | Path = ".",
    *,
    env_prefix: str = "",
    env_selectors: tuple[str, ...] = _DEFAULT_ENV_SELECTORS,
    strict: bool = False,
) -> None:
    """Initialize configuration by loading the merged YAML and setting env variables.

    Loads ``base.yaml`` + the ``{env}.yaml`` overlay (selected by the first set variable in
    ``env_selectors``) + ``secrets.yaml``, then exports every scalar leaf as an
    ``<env_prefix>_*`` env var (without overriding vars already set). The first call loads the YAML
    and sets env vars; a later call with the same arguments is a no-op, and a later call with a
    different ``config_dir``, ``env_prefix`` or ``env_selectors`` raises ``ValueError`` (the first
    call's export is process-wide, so a silently ignored second configuration would leave its
    settings on built-in defaults).

    Args:
        config_dir: Directory containing base.yaml (default: current directory).
        env_prefix: Env-var prefix for the exported values; pass the settings classes' ``env_prefix``
            with or without its trailing underscore (default: none — bare names, which share the
            process environment with every other library; prefer a prefix in deployments).
        env_selectors: Env vars consulted, in order, to select the overlay (default
            ``APP_ENV``, then ``ENVIRONMENT``; ``dev`` when none is set).
        strict: Raise instead of warning when the directory or ``base.yaml`` is missing or the YAML
            cannot be loaded (default: warn and continue on env vars and code defaults).

    Raises:
        ValueError: The config was already initialized with different arguments.
        FileNotFoundError: ``strict`` and the directory or ``base.yaml`` is missing.

    """
    global _initialized_with  # noqa: PLW0603
    requested = (
        str(Path(config_dir).resolve()),
        _normalize_prefix(env_prefix),
        tuple(env_selectors),
    )
    # Idempotency + thread-safety: a fast-path read, then a double-check under the lock, so concurrent
    # initialize_config() calls (multi-module imports, off-thread wiring) load the YAML exactly once.
    # (Only reads _initialized here; the write happens in _load_and_apply_config under the lock.)
    if _initialized:
        _require_same_arguments(requested)
        return
    with _init_lock:
        if _initialized:
            _require_same_arguments(requested)
            return
        _load_and_apply_config(config_dir, env_prefix, env_selectors, strict=strict)
        _initialized_with = requested


def _require_same_arguments(requested: tuple[str, str, tuple[str, ...]]) -> None:
    """Raise ``ValueError`` if ``requested`` differs from the arguments that initialized the config."""
    if _initialized_with is None or requested == _initialized_with:
        return
    names = ("config_dir", "env_prefix", "env_selectors")
    conflicts = [
        f"{name}={new!r} (initialized with {old!r})"
        for name, old, new in zip(names, _initialized_with, requested, strict=True)
        if old != new
    ]
    msg = f"initialize_config was already called with different arguments: {', '.join(conflicts)}"
    raise AppValueError(msg)


def _load_and_apply_config(
    config_dir: str | Path,
    env_prefix: str,
    env_selectors: tuple[str, ...],
    *,
    strict: bool,
) -> None:
    """Load the merged YAML + export ``<env_prefix>_*`` env defaults, then set the initialized sentinel.

    Runs under ``_init_lock`` (called only from ``initialize_config``). Without ``strict`` every early
    return still marks the config initialized so a missing/broken config is not retried on every call;
    with ``strict`` the failure raises and the config stays uninitialized.
    """
    global _initialized  # noqa: PLW0603

    config_path = Path(config_dir)

    # Graceful fallback for missing config directory (Docker/K8s deployments)
    if not config_path.exists():
        if strict:
            msg = f"Config directory not found: {config_path}"
            raise AppFileNotFoundError(msg)
        logger.warning(
            "Config directory not found: %s. Proceeding with environment variables only.",
            config_path,
        )
        _initialized = True
        return

    config_file = config_path / "base.yaml"
    if not config_file.exists():
        if strict:
            msg = f"Config file not found: {config_file}"
            raise AppFileNotFoundError(msg)
        logger.warning(
            "Config file not found: %s. Proceeding with environment variables only.",
            config_file,
        )
        _initialized = True
        return

    # Load and deep-merge base.yaml + {env}.yaml + secrets.yaml (env vars still win below).
    environment, selector = _resolve_environment(env_selectors)
    _log_overlay(config_path, environment, selector)
    try:
        config = load_config(str(config_dir), environment=environment)
    except Exception:
        if strict:
            raise
        logger.warning("Failed to load config from %s", config_dir, exc_info=True)
        _initialized = True
        return

    # Apply YAML values to env vars (respecting existing env vars)
    apply_yaml_defaults(config, env_prefix)

    _initialized = True


def _log_overlay(config_path: Path, environment: str, selector: str | None) -> None:
    """Log the selected overlay; warn when a selector named an overlay file that does not exist.

    The loader skips a missing overlay silently, so without this a selector set to a name with no
    matching file (``ENVIRONMENT=production`` beside ``prod.yaml``) would run on ``base.yaml`` alone.
    The ``dev`` fallback (no selector set) is optional, so its absence is only logged at info.
    """
    overlay = config_path / f"{environment}.yaml"
    found = overlay.exists()
    source = selector or "default"
    logger.info(
        "Config overlay: environment=%s (from %s), %s found=%s",
        environment,
        source,
        overlay,
        found,
    )
    if selector is not None and not found:
        logger.warning(
            "Config overlay %s selected by %s=%s does not exist; loading base.yaml without an overlay.",
            overlay,
            selector,
            environment,
        )


def reset_config() -> None:
    """Reset the initialization sentinel flag and the arguments it was set with.

    This is for testing only. It allows tests to call initialize_config()
    multiple times with different configurations.
    """
    global _initialized, _initialized_with  # noqa: PLW0603
    _initialized = False
    _initialized_with = None
