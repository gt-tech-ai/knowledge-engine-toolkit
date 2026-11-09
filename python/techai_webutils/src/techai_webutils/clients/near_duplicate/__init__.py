"""Near-duplicate detectors.

(``NearDuplicateKind``: ``noop``, ``minhash``) and their factory.
"""

from techai_webutils.clients.near_duplicate.builder import (
    NearDuplicateConfig,
    NearDuplicateKind,
    near_duplicate_detector_from_config,
)

__all__ = [
    "NearDuplicateConfig",
    "NearDuplicateKind",
    "near_duplicate_detector_from_config",
]
