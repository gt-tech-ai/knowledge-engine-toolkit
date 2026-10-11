"""Cursor-based pagination utilities.

Mirrors Go's ``repos/repository/cursor.go``.
"""

from __future__ import annotations

import base64
import json
from dataclasses import dataclass

from techai_webutils.core.errors import AppValueError


@dataclass(frozen=True)
class CursorPayload:
    """Data encoded in a pagination cursor."""

    last_id: str
    """Id of the last row on the page, resuming the next page after it."""
    last_value: str = ""
    """Sort-column value of the last row, tie-breaking the keyset scan (empty when id-only)."""


class CursorCodec:
    """Encodes and decodes opaque pagination cursors.

    Uses base64url + JSON for safe transport in URLs and JSON payloads.
    """

    @staticmethod
    def encode(payload: CursorPayload) -> str:
        """Encode a CursorPayload into an opaque cursor string."""
        data = json.dumps({"id": payload.last_id, "v": payload.last_value})
        return base64.urlsafe_b64encode(data.encode("utf-8")).decode("ascii")

    @staticmethod
    def decode(cursor: str) -> CursorPayload:
        """Decode an opaque cursor string into a CursorPayload.

        Raises ValueError if the cursor is malformed.
        """
        try:
            data = json.loads(base64.urlsafe_b64decode(cursor.encode("ascii")))
            return CursorPayload(
                last_id=data["id"],
                last_value=data.get("v", ""),
            )
        except (json.JSONDecodeError, KeyError, ValueError) as e:
            msg = f"invalid cursor: {cursor!r}"
            raise AppValueError(msg) from e
