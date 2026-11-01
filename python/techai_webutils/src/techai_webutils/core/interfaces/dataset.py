"""``Dataset`` — the swappable source of eval cases.

A dataset yields ``EvalCase``s and names its own ``version()``: a content hash, so an eval baseline
is keyed on exactly the cases it was measured against and a changed dataset can never be compared
with a stale baseline.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Mapping


@dataclass(frozen=True, slots=True)
class EvalCase:
    """One eval case: the inputs to run and what a correct answer looks like."""

    id: str
    """Stable case id (keys results and regressions across runs)."""
    inputs: Mapping[str, str]
    """What the system under test is given (e.g. ``query``)."""
    expected: Mapping[str, str]
    """Reference values the graders compare against (e.g. ``answer``, ``citation``)."""
    metadata: Mapping[str, str]
    """Free-form labels (slice, source, difficulty) for per-slice reporting."""

    def __post_init__(self) -> None:
        """Reject a case with no id (``INVALID_INPUT``)."""
        if not self.id:
            raise AppError(ErrorCode.INVALID_INPUT, "eval case requires an id")


class Dataset(ABC):
    """A versioned, iterable set of eval cases."""

    @abstractmethod
    def cases(self) -> AsyncIterator[EvalCase]:
        """Yield every case in the dataset, in a stable order."""

    @abstractmethod
    def version(self) -> str:
        """Return the dataset's content hash (equal versions mean identical cases)."""
