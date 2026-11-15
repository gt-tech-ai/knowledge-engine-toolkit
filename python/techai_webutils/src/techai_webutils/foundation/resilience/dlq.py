"""Dead-letter-queue facade + in-memory backend.

``DeadLetterQueue`` wraps a ``DeadLetterBackend`` with swallow-and-report semantics
(mirroring the SSE reference): a backend failure never propagates to crash the consumer
loop, but ``send`` returns ``False`` so the caller can leave the source message for SQS
redrive instead of deleting it (avoiding silent data loss on a DLQ outage).

The real SQS backend lives in ``sqs_dlq.py`` (thin aiobotocore wrapper,
integration-tested); ``StubDeadLetterBackend`` here is the in-memory backend for
tests/local. ``with_recorder`` counts each letter ``sent`` or ``failed``, as Go's
``deadletter.Queue.WithRecorder`` does.
"""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING, Self, final, override

from techai_webutils.core.interfaces.dlq import DeadLetter, DeadLetterBackend
from techai_webutils.foundation.resilience.observer import UNOBSERVED
from techai_webutils.foundation.resilience.series import DeadLetterOutcome

if TYPE_CHECKING:
    from techai_webutils.foundation.resilience.series import Recorder

logger = logging.getLogger(__name__)


@final
class DeadLetterQueue:
    """Facade over a ``DeadLetterBackend`` that swallows+logs backend failures.

    ``send`` returns True when the letter was durably recorded, False when the backend
    failed (already logged) -- the caller uses that to decide delete-vs-redrive of the
    source message.
    """

    def __init__(self, backend: DeadLetterBackend) -> None:
        """Wrap the given backend.

        Args:
            backend: Where dead letters go.

        """
        self._backend = backend
        self._recorder: Recorder = UNOBSERVED.recorder

    def with_recorder(self, recorder: Recorder) -> Self:
        """Count each letter ``sent`` or ``failed``.

        Args:
            recorder: Where the outcomes are counted.

        Returns:
            This queue.

        """
        self._recorder = recorder
        return self

    async def send(self, letter: DeadLetter) -> bool:
        """Send a dead letter; return True on success, False if the backend failed.

        Args:
            letter: The failed message with its error and attempt count.

        Returns:
            Whether the letter was sent.

        """
        try:
            await self._backend.send(letter)
        except Exception:
            logger.exception(
                "Failed to record dead letter %s",
                letter.id,
                extra={"dead_letter_id": letter.id, "reason": letter.reason},
            )
            self._recorder.dead_lettered(DeadLetterOutcome.FAILED)
            return False
        self._recorder.dead_lettered(DeadLetterOutcome.SENT)
        return True


class StubDeadLetterBackend(DeadLetterBackend):
    """In-memory dead-letter backend that records letters (tests / local dev)."""

    def __init__(self) -> None:
        """Start with an empty list of recorded letters."""
        self.letters: list[DeadLetter] = []

    @override
    async def send(self, letter: DeadLetter) -> None:
        """Record the letter in memory.

        Args:
            letter: The failed message with its error and attempt count.

        """
        self.letters.append(letter)


class NoopDeadLetterBackend(DeadLetterBackend):
    """Dead-letter backend that DROPS letters.

    For local and test runs without a real DLQ queue.

    Distinct from ``StubDeadLetterBackend`` (which accumulates letters for inspection):
    this one discards them, matching the ``noop`` DLQ kind a worker selects on the local
    path.
    """

    @override
    async def send(self, letter: DeadLetter) -> None:
        """Discard the dead letter.

        Args:
            letter: The failed message with its error and attempt count.

        """
