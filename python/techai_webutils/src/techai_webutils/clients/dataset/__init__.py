"""Eval datasets: a config-selected ``Dataset``.

The kinds are ``stub``, ``jsonl`` and a supplied ``aiops``.
"""

from techai_webutils.clients.dataset.builder import (
    DatasetConfig,
    DatasetKind,
    new_dataset_from_config,
)

__all__ = ["DatasetConfig", "DatasetKind", "new_dataset_from_config"]
