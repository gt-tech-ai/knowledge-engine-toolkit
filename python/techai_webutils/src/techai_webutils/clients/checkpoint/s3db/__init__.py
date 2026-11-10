"""The ``s3db`` checkpoint store: artifacts in an object store.

Stage records go through a state client.
"""

from techai_webutils.clients.checkpoint.s3db.store import S3DbCheckpointStore

__all__ = ["S3DbCheckpointStore"]
