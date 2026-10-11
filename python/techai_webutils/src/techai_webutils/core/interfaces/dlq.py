"""Dead-letter-queue interfaces.

A ``DeadLetter`` is the record written when a message cannot be processed and must not
be retried (permanent/poison failure). ``DeadLetterBackend`` is the pluggable sink
(a real SQS queue in stage/prod, an in-memory stub in tests); the ``DeadLetterQueue``
facade (foundation) wraps a backend with swallow-and-report semantics.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field


@dataclass(frozen=True, slots=True)
class DeadLetter:
    """A message routed to the dead-letter queue after a permanent failure."""

    id: str
    """The source message identifier, used as the ``source_id`` DLQ attribute for correlation back to the
    original message."""

    payload: bytes
    """The raw source message body, preserved verbatim for forensics."""

    reason: str
    """A short machine/human tag for why the message was dead-lettered (e.g. ``unsupported_format``);
    surfaced as the ``reason`` DLQ attribute."""

    metadata: dict[str, str] = field(default_factory=dict)
    """Optional extra context propagated as DLQ message attributes; keys colliding with the reserved
    ``reason``/``source_id`` attributes are dropped."""


class DeadLetterBackend(ABC):
    """A sink that durably records dead letters (SQS queue, stub, ...)."""

    @abstractmethod
    async def send(self, letter: DeadLetter) -> None:
        """Persist a dead letter; raise if the sink is unavailable."""
        ...
