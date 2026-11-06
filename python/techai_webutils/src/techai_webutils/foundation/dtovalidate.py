"""The leaf rules a generated Python DTO validator composes (the Go ``foundation/dtovalidate`` mirror).

``protoc-gen-dtovalidate`` (mode ``py``) emits one ``validate_<dto>()`` function per DTO bound
to a request message by ``(dtovalidate.v1.dto)``; each calls these checks for the request's
``buf.validate`` rules, so a Python server enforces the proto's bounds on its domain DTO as a
Go server does. Every failure is an ``InvalidInputError`` (``INVALID_INPUT``, so
``INVALID_ARGUMENT`` at the gRPC edge) whose message matches the Go library's.
"""

from __future__ import annotations

import uuid as _uuid

from techai_webutils.core.errors import InvalidInputError

__all__ = ["int_range", "max_bytes", "max_items", "min_max_len", "uuid"]


def uuid(field: str, value: str) -> None:
    """Refuse ``value`` unless it is a valid RFC 4122 UUID.

    Raises:
        InvalidInputError: When ``value`` does not parse as a UUID.

    """
    try:
        _uuid.UUID(value)
    except ValueError as exc:
        msg = f"{field}: must be a valid UUID"
        raise InvalidInputError(msg) from exc


def min_max_len(field: str, value: str, min_len: int, max_len: int) -> None:
    """Refuse ``value`` unless its length in code points is in ``[min_len, max_len]``.

    A ``max_len`` of 0 or less is no upper bound (a proto rule with only ``min_len``).

    Raises:
        InvalidInputError: When the length is out of bounds.

    """
    n = len(value)
    if n < min_len or 0 < max_len < n:
        msg = f"{field}: length {n} not in [{min_len}, {max_len}]"
        raise InvalidInputError(msg)


def max_bytes(field: str, value: str, limit: int) -> None:
    """Refuse ``value`` when its UTF-8 encoding is longer than ``limit`` bytes.

    Raises:
        InvalidInputError: When the encoding is over the cap.

    """
    n = len(value.encode())
    if n > limit:
        msg = f"{field}: {n} bytes exceeds max {limit}"
        raise InvalidInputError(msg)


def max_items(field: str, count: int, limit: int) -> None:
    """Refuse a repeated field of ``count`` items when that is more than ``limit``.

    Raises:
        InvalidInputError: When there are too many items.

    """
    if count > limit:
        msg = f"{field}: {count} items exceeds max {limit}"
        raise InvalidInputError(msg)


def int_range(field: str, value: int, lo: int | None, hi: int | None) -> None:
    """Refuse ``value`` outside the inclusive ``[lo, hi]``; ``None`` is an absent bound.

    A real bound of 0 (a proto ``lte: 0``) is enforced, never read as unbounded.

    Raises:
        InvalidInputError: When ``value`` is out of range.

    """
    if (lo is not None and value < lo) or (hi is not None and value > hi):
        low = "-inf" if lo is None else str(lo)
        high = "inf" if hi is None else str(hi)
        msg = f"{field}: {value} out of range [{low}, {high}]"
        raise InvalidInputError(msg)
