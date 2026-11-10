"""Config-selected ``Dataset`` factory (the ``clients/token_ledger/builder.py`` pattern).

``DatasetConfig.kind`` selects a built-in backend — ``stub`` (three fixed cases, the
default) or ``jsonl`` (a committed, content-hashed file) — or a consumer-supplied kind
from the injected ``backends`` mapping (``aiops``: the curated dataset the aiops service
owns). An unknown kind raises ``AppValueError`` (a ``ValueError`` coded
``INVALID_INPUT``); the ``jsonl`` backend is imported lazily.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from types import MappingProxyType
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.dataset.stub import StubDataset
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

    from techai_webutils.clients.dataset.jsonl import JsonlDataset as _LazyJsonlDataset
    from techai_webutils.core.interfaces.dataset import Dataset


class DatasetKind(StrEnum):
    """The eval dataset backends."""

    STUB = "stub"
    """Three fixed in-memory cases (dev/test; zero infrastructure)."""
    JSONL = "jsonl"
    """A committed ``.jsonl`` file, versioned by its content hash."""
    AIOPS = "aiops"
    """The curated dataset the aiops service owns (a consumer-supplied backend)."""


@dataclass(frozen=True, slots=True)
class DatasetConfig:
    """Eval dataset configuration."""

    kind: str = DatasetKind.STUB
    """A ``DatasetKind`` value or a key of the injected ``backends``."""
    path: str = ""
    """The ``.jsonl`` file the ``jsonl`` kind reads."""


_DEFAULT_BACKENDS: Mapping[str, Callable[[DatasetConfig], Dataset]] = MappingProxyType({})
"""The default ``backends``."""


def new_dataset_from_config(
    config: DatasetConfig,
    *,
    backends: Mapping[str, Callable[[DatasetConfig], Dataset]] = _DEFAULT_BACKENDS,
) -> Dataset:
    """Build the ``Dataset`` selected by ``config.kind``.

    Args:
        config: The dataset configuration.
        backends: Factories for consumer-supplied kinds (``aiops``), keyed by kind.

    Returns:
        The ``Dataset`` selected by ``config.kind``.

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for the ``aiops`` kind with no injected backend.

    """
    if config.kind == DatasetKind.STUB:
        return StubDataset()
    if config.kind == DatasetKind.JSONL:
        jsonl_dataset_cls = cast(
            "type[_LazyJsonlDataset]",
            importlib.import_module("techai_webutils.clients.dataset.jsonl").JsonlDataset,
        )

        return jsonl_dataset_cls(config.path)
    factory = backends.get(config.kind)
    if factory is not None:
        return factory(config)
    if config.kind == DatasetKind.AIOPS:
        raise AppError(ErrorCode.INVALID_INPUT, "aiops dataset backend not supplied")
    msg = f"unknown dataset kind: {config.kind!r}"
    raise AppValueError(msg)
