"""``bulk_ingest``: hold an index in bulk-ingest mode for one backfill.

The settings are restored however it ends; ``begin_bulk_settings`` and
``end_bulk_settings`` are the requests a store sends to enter and leave the mode.

A backfill re-index writes far faster with refresh off and no replicas, but an index left
that way serves stale reads and loses data with its node. ``bulk_ingest`` puts the index
into that mode on entry and restores (and verifies) the recorded settings on exit, whether
the backfill finished, raised or was cancelled. It works over a single-domain store and a
multi-domain one alike.
"""

from __future__ import annotations

from contextlib import asynccontextmanager
from typing import TYPE_CHECKING, Protocol, cast

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import AsyncGenerator, Mapping

    from opensearchpy import AsyncOpenSearch

_BULK_PREVIOUS = "bulk_ingest_previous"
"""The ``_meta`` key holding an index's settings from before ``begin_bulk_settings``."""


class BulkIngestible(Protocol):
    """A store that can switch one of its indices into bulk-ingest mode and back."""

    async def begin_bulk_ingest(self, index: str) -> None:
        """Turn refresh off and replicas to zero on ``index``.

        The previous settings are recorded.

        Args:
            index: The index.

        """
        ...

    async def end_bulk_ingest(self, index: str) -> None:
        """Restore and verify ``index``'s recorded settings.

        A no-op when none are recorded.

        Args:
            index: The index.

        """
        ...


@asynccontextmanager
async def bulk_ingest(store: BulkIngestible, index: str) -> AsyncGenerator[None]:
    """Run the body with ``index`` in bulk-ingest mode; restore it on every exit.

    The restore also runs when entering the mode failed partway, since the store records
    the previous settings before changing them and restoring nothing recorded is a no-op.
    When the body and the restore both fail, the restore's error is raised with the body's
    as its context.

    A callee raises ``AppValueError``: The restored settings did not take (from
    ``end_bulk_ingest``).

    Args:
        store: The store that owns the index.
        index: The index.

    """
    try:
        await store.begin_bulk_ingest(index)
        yield
    finally:
        await store.end_bulk_ingest(index)


async def begin_bulk_settings(
    client: AsyncOpenSearch, index: str, params: Mapping[str, object]
) -> None:
    """Turn refresh off and replicas to zero on ``index``, recording the previous ones.

    The previous settings go into the mapping's ``_meta`` before anything changes, and a
    second call keeps the first recording.

    Args:
        client: The domain client.
        index: The index.
        params: The request params (the request timeout).

    """
    response = cast(
        "dict[str, object]", await client.indices.get_settings(index=index, params=params)
    )
    settings = cast("dict[str, dict[str, dict[str, object]]]", response)[index][
        "settings"
    ]
    current = cast("dict[str, object]", settings["index"])
    mapping = cast(
        "dict[str, object]", await client.indices.get_mapping(index=index, params=params)
    )
    meta = mapping_meta(mapping, index)
    if _BULK_PREVIOUS not in meta:
        previous = {
            "refresh_interval": str(current.get("refresh_interval", "1s")),
            "number_of_replicas": str(current.get("number_of_replicas", "1")),
        }
        await _put_meta(client, index, meta | {_BULK_PREVIOUS: previous}, params)
    await _put_settings(
        client, index, {"refresh_interval": "-1", "number_of_replicas": "0"}, params
    )


async def end_bulk_settings(
    client: AsyncOpenSearch, index: str, params: Mapping[str, object]
) -> None:
    """Restore and verify the settings ``begin_bulk_settings`` recorded on ``index``.

    A no-op when none are recorded.

    Args:
        client: The domain client.
        index: The index.
        params: The request params (the request timeout).

    Raises:
        AppValueError: The restored settings did not take.

    """
    mapping = cast(
        "dict[str, object]", await client.indices.get_mapping(index=index, params=params)
    )
    meta = mapping_meta(mapping, index)
    previous = cast("dict[str, str] | None", meta.get(_BULK_PREVIOUS))
    if previous is None:
        return
    await _put_settings(client, index, previous, params)
    response = cast(
        "dict[str, object]", await client.indices.get_settings(index=index, params=params)
    )
    applied = cast("dict[str, dict[str, dict[str, dict[str, object]]]]", response)
    current = applied[index]["settings"]["index"]
    if any(str(current.get(key)) != value for key, value in previous.items()):
        msg = f"bulk-ingest settings of {index} were not restored: {previous}"
        raise AppValueError(msg)
    await _put_meta(
        client, index, {k: v for k, v in meta.items() if k != _BULK_PREVIOUS}, params
    )


def mapping_meta(mapping: Mapping[str, object], index: str) -> dict[str, object]:
    """Return the ``_meta`` of ``index`` from a get-mapping response.

    Args:
        mapping: The get-mapping response.
        index: The index.

    Returns:
        The ``_meta``; empty when the mapping has none.

    """
    body = cast("dict[str, dict[str, dict[str, object]]]", mapping)[index]["mappings"]
    return dict(cast("dict[str, object]", body.get("_meta", {})))


async def _put_settings(
    client: AsyncOpenSearch,
    index: str,
    values: Mapping[str, str],
    params: Mapping[str, object],
) -> None:
    """Apply index settings.

    Args:
        client: The domain client.
        index: The index.
        values: The settings.
        params: The request params.

    """
    await client.indices.put_settings(
        index=index, body={"index": dict(values)}, params=dict(params)
    )


async def _put_meta(
    client: AsyncOpenSearch,
    index: str,
    meta: Mapping[str, object],
    params: Mapping[str, object],
) -> None:
    """Replace the index mapping's ``_meta``.

    Args:
        client: The domain client.
        index: The index.
        meta: The new ``_meta``.
        params: The request params.

    """
    await client.indices.put_mapping(
        index=index, body={"_meta": dict(meta)}, params=dict(params)
    )
