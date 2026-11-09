"""The ``jsonl`` dataset: a committed ``.jsonl`` file, versioned by its content hash.

Each line is one JSON object ``{"id", "inputs", "expected", "metadata"?}``; blank lines
are skipped. A missing file is ``UNAVAILABLE``; a malformed line or a line without an
``id`` is ``INVALID_INPUT``.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import TYPE_CHECKING, cast, final, override

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.dataset import Dataset, EvalCase

if TYPE_CHECKING:
    from collections.abc import AsyncIterator


def _str_map(value: object, field: str, line_no: int) -> dict[str, str]:
    """Return ``value`` as a ``str → str`` mapping, or raise ``INVALID_INPUT``.

    Returns:
        ``value`` as a ``str → str`` mapping, or raise ``INVALID_INPUT``.

    Raises:
        AppError: Dataset line ``line_no``: ``field`` must be an object.

    """
    if value is None:
        return {}
    if not isinstance(value, dict):
        msg = f"dataset line {line_no}: {field} must be an object"
        raise AppError(ErrorCode.INVALID_INPUT, msg)
    return {
        str(key): str(item) for key, item in cast("dict[object, object]", value).items()
    }


def _parse_line(text: str, line_no: int) -> EvalCase:
    """Parse one non-blank line into an ``EvalCase``, or raise ``INVALID_INPUT``.

    Returns:
        One non-blank line into an ``EvalCase``, or raise ``INVALID_INPUT``.

    Raises:
        AppError: Dataset line ``line_no`` is not JSON; dataset line ``line_no`` must be
            an object.

    """
    try:
        row = cast("object", json.loads(text))
    except json.JSONDecodeError as exc:
        msg = f"dataset line {line_no} is not JSON"
        raise AppError(ErrorCode.INVALID_INPUT, msg, cause=exc) from exc
    if not isinstance(row, dict):
        msg = f"dataset line {line_no} must be an object"
        raise AppError(ErrorCode.INVALID_INPUT, msg)
    fields = cast("dict[str, object]", row)
    return EvalCase(
        id=str(fields.get("id") or ""),
        inputs=_str_map(fields.get("inputs"), "inputs", line_no),
        expected=_str_map(fields.get("expected"), "expected", line_no),
        metadata=_str_map(fields.get("metadata"), "metadata", line_no),
    )


@final
class JsonlDataset(Dataset):
    """Cases read from a ``.jsonl`` file.

    ``version()`` is the sha256 of the file bytes.
    """

    def __init__(self, path: str) -> None:
        """Bind the dataset to ``path``.

        (the file is read on each call, never at construction).
        """
        self._path = Path(path)

    def _read(self) -> bytes:
        """Return the file bytes, or raise ``UNAVAILABLE`` when the file can't be read.

        Returns:
            The file bytes, or raise ``UNAVAILABLE`` when the file can't be read.

        Raises:
            AppError: Dataset file unreadable: ``self._path``.

        """
        try:
            return self._path.read_bytes()
        except OSError as exc:
            msg = f"dataset file unreadable: {self._path}"
            raise AppError(ErrorCode.UNAVAILABLE, msg, cause=exc) from exc

    @override
    async def cases(self) -> AsyncIterator[EvalCase]:
        """Yield each line's case in file order.

        Yields:
            Each line's case in file order.

        """
        for line_no, text in enumerate(self._read().decode().splitlines(), start=1):
            if text.strip():
                yield _parse_line(text, line_no)

    @override
    def version(self) -> str:
        """Return the sha256 hex of the file bytes.

        Returns:
            The sha256 hex of the file bytes.

        """
        return hashlib.sha256(self._read()).hexdigest()
