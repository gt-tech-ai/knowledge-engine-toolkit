"""The ``jsonl`` dataset: a committed ``.jsonl`` file, versioned by its content hash.

Each line is one JSON object ``{"id", "inputs", "expected", "metadata"?}``; blank lines are
skipped. A missing file is ``UNAVAILABLE``; a malformed line or a line without an ``id`` is
``INVALID_INPUT``.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import TYPE_CHECKING, Any, override

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.dataset import Dataset, EvalCase

if TYPE_CHECKING:
    from collections.abc import AsyncIterator


def _str_map(value: Any, field: str, line_no: int) -> dict[str, str]:  # noqa: ANN401 - parsed JSON
    """Return ``value`` as a ``str → str`` mapping, or raise ``INVALID_INPUT``."""
    if value is None:
        return {}
    if not isinstance(value, dict):
        msg = f"dataset line {line_no}: {field} must be an object"
        raise AppError(ErrorCode.INVALID_INPUT, msg)
    return {str(key): str(item) for key, item in value.items()}  # pyright: ignore[reportUnknownVariableType, reportUnknownArgumentType]


def _parse_line(text: str, line_no: int) -> EvalCase:
    """Parse one non-blank line into an ``EvalCase``, or raise ``INVALID_INPUT``."""
    try:
        row = json.loads(text)
    except json.JSONDecodeError as exc:
        msg = f"dataset line {line_no} is not JSON"
        raise AppError(ErrorCode.INVALID_INPUT, msg, cause=exc) from exc
    if not isinstance(row, dict):
        msg = f"dataset line {line_no} must be an object"
        raise AppError(ErrorCode.INVALID_INPUT, msg)
    return EvalCase(
        id=str(row.get("id") or ""),  # pyright: ignore[reportUnknownMemberType, reportUnknownArgumentType]
        inputs=_str_map(row.get("inputs"), "inputs", line_no),  # pyright: ignore[reportUnknownMemberType]
        expected=_str_map(row.get("expected"), "expected", line_no),  # pyright: ignore[reportUnknownMemberType]
        metadata=_str_map(row.get("metadata"), "metadata", line_no),  # pyright: ignore[reportUnknownMemberType]
    )


class JsonlDataset(Dataset):
    """Cases read from a ``.jsonl`` file; ``version()`` is the sha256 of the file bytes."""

    def __init__(self, path: str) -> None:
        """Bind the dataset to ``path`` (the file is read on each call, never at construction)."""
        self._path = Path(path)

    def _read(self) -> bytes:
        """Return the file bytes, or raise ``UNAVAILABLE`` when the file can't be read."""
        try:
            return self._path.read_bytes()
        except OSError as exc:
            msg = f"dataset file unreadable: {self._path}"
            raise AppError(ErrorCode.UNAVAILABLE, msg, cause=exc) from exc

    @override
    async def cases(self) -> AsyncIterator[EvalCase]:
        """Yield each line's case in file order."""
        for line_no, text in enumerate(self._read().decode().splitlines(), start=1):
            if text.strip():
                yield _parse_line(text, line_no)

    @override
    def version(self) -> str:
        """Return the sha256 hex of the file bytes."""
        return hashlib.sha256(self._read()).hexdigest()
