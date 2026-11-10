"""Near-duplicate detectors and their factory.

The kinds (``NearDuplicateKind``) are ``noop`` and ``minhash``.
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
