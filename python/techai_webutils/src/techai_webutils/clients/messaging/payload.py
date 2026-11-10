"""The publish-side payload size check every messaging Kind runs before sending."""

from __future__ import annotations

from techai_webutils.core.errors import AppValueError


def check_payload_size(payload: bytes, limit: int) -> None:
    """Reject a payload larger than ``limit`` bytes before it is sent.

    Args:
        payload: The message's bytes.
        limit: The largest payload accepted.

    Raises:
        AppValueError: ``payload`` is over ``limit`` bytes (``INVALID_INPUT``).

    """
    if len(payload) > limit:
        msg = f"message payload of {len(payload)} bytes exceeds the {limit}-byte limit"
        raise AppValueError(msg)
