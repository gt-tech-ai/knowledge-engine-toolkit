"""``checkpoint_store_from_config``: the config-selected ``CheckpointStore``."""

from __future__ import annotations

from datetime import UTC, datetime
from typing import TYPE_CHECKING

from techai_webutils.clients.checkpoint.config import CheckpointConfig, CheckpointKind
from techai_webutils.clients.checkpoint.noop import NoopCheckpointStore
from techai_webutils.clients.checkpoint.s3db import S3DbCheckpointStore
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.core.interfaces.checkpoint import (
        CheckpointStore,
        StageStateClient,
    )
    from techai_webutils.core.interfaces.storage import StorageClient


def _utc_now() -> datetime:
    """Return the current UTC time (the default stage-record clock)."""
    return datetime.now(UTC)


def checkpoint_store_from_config(
    config: CheckpointConfig,
    *,
    object_store: StorageClient | None = None,
    state_client: StageStateClient | None = None,
    clock: Callable[[], datetime] = _utc_now,
) -> CheckpointStore:
    """Build the ``CheckpointStore`` ``config.kind`` selects.

    ``noop`` (the default) needs nothing. ``s3db`` needs the app's ``object_store``, the
    ``state_client`` that writes stage records, and a ``bucket``.

    Raises:
        AppValueError: An unknown kind, or ``s3db`` without a bucket, object store or state client
            (``INVALID_INPUT``).

    """
    if config.kind == CheckpointKind.NOOP:
        return NoopCheckpointStore()
    if config.kind == CheckpointKind.S3DB:
        if object_store is None or state_client is None or not config.bucket:
            msg = "the s3db checkpoint store needs a bucket, an object_store and a state_client"
            raise AppValueError(msg)
        return S3DbCheckpointStore(object_store, state_client, config, clock)
    msg = f"unknown checkpoint kind: {config.kind!r}"
    raise AppValueError(msg)
