"""The ``stub`` dataset: three fixed in-memory cases (zero infrastructure)."""

from __future__ import annotations

import hashlib
import json
from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.dataset import Dataset, EvalCase

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

_CASES = (
    EvalCase(
        id="stub-1",
        inputs={"query": "What is the refund window?"},
        expected={"answer": "30 days"},
        metadata={"slice": "lookup"},
    ),
    EvalCase(
        id="stub-2",
        inputs={"query": "Summarize the travel policy."},
        expected={"answer": "Book economy; file expenses within 14 days."},
        metadata={"slice": "summarize"},
    ),
    EvalCase(
        id="stub-3",
        inputs={"query": "Who approves purchases over $10,000?"},
        expected={"answer": "The finance director"},
        metadata={"slice": "lookup"},
    ),
)


def _content_hash() -> str:
    """Return the sha256 hex of the cases' canonical JSON (the stub's version)."""
    canonical = json.dumps(
        [
            {
                "id": case.id,
                "inputs": dict(case.inputs),
                "expected": dict(case.expected),
                "metadata": dict(case.metadata),
            }
            for case in _CASES
        ],
        sort_keys=True,
        separators=(",", ":"),
    )
    return hashlib.sha256(canonical.encode()).hexdigest()


class StubDataset(Dataset):
    """Three fixed cases, so an eval run is exercisable before any dataset exists."""

    @override
    async def cases(self) -> AsyncIterator[EvalCase]:
        """Yield the three fixed cases in order."""
        for case in _CASES:
            yield case

    @override
    def version(self) -> str:
        """Return the sha256 hex of the cases' canonical JSON."""
        return _content_hash()
