"""What a resilience component reports through (mirrors Go ``series.Observer``).

An ``Observer`` bundles the ``Recorder`` bound to a component's tier and name with the
logger its Warning lines go to and the tier and name those lines carry. The decorate
layers, the circuit breaker and the standalone primitives report through one; an
``Observer`` with no metrics provider records nothing and one with no logger logs
nothing. ``retries_done`` is the one rule for counting a finished retried call, shared
by the retry layer and the standalone retriers.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import TYPE_CHECKING, final

from techai_webutils.foundation.resilience.budget import RetryBudgetExhaustedError
from techai_webutils.foundation.resilience.series import Reason, Recorder

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricsProvider


@final
@dataclass(frozen=True, slots=True)
class Observer:
    """A component's recorder, logger, and the tier and name its Warning lines carry."""

    recorder: Recorder
    """Records the component's resilience events (inert without a provider)."""

    logger: Logger | None = None
    """Receives the component's Warning lines; ``None`` logs nothing."""

    tier: str = ""
    """The tier a Warning line names: the decoration tier, or ``TIER_STANDALONE`` for a
    primitive outside one (``Observer.of`` binds the same tier to the recorder and the
    lines); empty names no tier."""

    name: str = ""
    """The subject a Warning line names."""

    @classmethod
    def of(
        cls,
        metrics: MetricsProvider | None,
        logger: Logger | None,
        tier: str,
        name: str,
    ) -> Observer:
        """Return the Observer for the subject ``name`` in ``tier``.

        Args:
            metrics: Where the series are registered; ``None`` records nothing.
            logger: Where Warning lines go; ``None`` logs nothing.
            tier: The tier label (``series.TIER_STANDALONE`` outside a tier).
            name: The subject's name.

        Returns:
            The Observer for the subject ``name`` in ``tier``.

        """
        return cls(Recorder(metrics, tier, name), logger, tier, name)

    def warn(self, msg: str, **fields: object) -> None:
        """Write one Warning line naming the tier (when set) and the subject.

        The tier and name are the Observer's own: a ``tier`` or ``name`` among
        ``fields`` is dropped rather than raising.

        Args:
            msg: The message.
            **fields: The line's other fields, after the tier and name.

        """
        if self.logger is None:
            return
        line: dict[str, object] = {"tier": self.tier} if self.tier else {}
        line["name"] = self.name
        line.update((k, v) for k, v in fields.items() if k not in _SUBJECT_KEYS)
        self.logger.warning(msg, **line)

    def retries_done(
        self, operation: str, attempts: int, error: BaseException | None
    ) -> None:
        """Record one retried call (``retries_done``) and warn when it was exhausted.

        Args:
            operation: The operation label ("" for a standalone retrier).
            attempts: How many attempts ran.
            error: What the call finally raised, or ``None`` when it succeeded.

        """
        if retries_done(self.recorder, operation, attempts, error):
            self.warn("retries exhausted", operation=operation, attempts=attempts)


_SUBJECT_KEYS = frozenset({"tier", "name"})
"""The fields ``Observer.warn`` sets itself."""

UNOBSERVED = Observer(Recorder(None, "", ""))
"""The Observer that records and logs nothing."""


def retries_done(
    recorder: Recorder, operation: str, attempts: int, error: BaseException | None
) -> bool:
    """Record one call a retrier finished with ``error`` after ``attempts`` attempts.

    One retry per attempt after the first; then, when ``error`` is the retry budget
    refusing another attempt, a ``budget_exhausted`` rejection and nothing more; when
    the caller cancelled the call (``asyncio.CancelledError``, which is also how a
    caller's deadline reaches the retrier), nothing more; else, when ``error`` is a
    failure after at least one retry, an exhaustion. Mirrors Go
    ``series.Recorder.RetriesDone``.

    Args:
        recorder: Where the events are recorded.
        operation: The operation label ("" for a standalone retrier).
        attempts: How many attempts ran.
        error: What the call finally raised, or ``None`` when it succeeded.

    Returns:
        Whether it counted an exhaustion (so a caller with a logger can warn).

    """
    for _ in range(attempts - 1):
        recorder.retried(operation)
    if error is None:
        return False
    if isinstance(error, RetryBudgetExhaustedError):
        recorder.rejected(operation, Reason.BUDGET_EXHAUSTED)
        return False
    if attempts <= 1 or isinstance(error, asyncio.CancelledError):
        return False
    recorder.retry_exhausted(operation)
    return True
