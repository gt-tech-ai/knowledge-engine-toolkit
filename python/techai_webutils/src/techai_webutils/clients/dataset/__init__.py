"""Eval datasets: a config-selected ``Dataset``.

(``stub``, ``jsonl``, or a supplied ``aiops``).
"""

from techai_webutils.clients.dataset.builder import (
    DatasetConfig,
    DatasetKind,
    new_dataset_from_config,
)

__all__ = ["DatasetConfig", "DatasetKind", "new_dataset_from_config"]
