"""S3 backend for the storage client — the concrete ``aiobotocore``-backed impl.

Backend subpackage of ``clients/storage``: the tier root's
``builder.py`` selects this backend. ``StorageKind`` + a ``memory`` stub land in
.
"""

from techai_webutils.clients.storage.s3.s3_client import S3StorageClient

__all__ = ["S3StorageClient"]
