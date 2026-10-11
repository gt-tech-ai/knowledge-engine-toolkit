"""S3-compatible storage client (shape: ``builder.py`` + ``s3/`` backend).

The concrete ``S3StorageClient`` is intentionally NOT re-exported here so importing the tier
(for ``StorageConfig`` / ``new_storage_from_config``) does not eagerly load aiobotocore — that
is what the builder's lazy import is for. Import it from ``clients.storage.s3.s3_client`` when
the concrete backend is needed.
"""

from techai_webutils.clients.storage.builder import (
    StorageConfig,
    StorageKind,
    new_storage_from_config,
)
from techai_webutils.clients.storage.config import S3Config

__all__ = [
    "S3Config",
    "StorageConfig",
    "StorageKind",
    "new_storage_from_config",
]
