"""EventHandler decorator stack for a message handler coroutine (ARCHITECTURE.md#decorator-order).

Composes, outermost -> innermost, ``Dedup -> DeadLetter -> [Retry -> CircuitBreaker -> Timeout]
-> Handler``. Mirrors Go's ``clients/messaging/decorators.WrapHandler``. A message already
processed is skipped; one that still fails after retries is routed to the dead-letter queue and
acked when it lands there. Built from the existing foundation
primitives (``retry_transient_async`` + ``with_timeout`` + the circuit breaker) and the DLQ
facade, so the mechanism is composed once, not re-implemented.

Pair ``dedup`` with a ``dlq`` so a terminal failure is dead-lettered rather than skipped as a
duplicate on redrive.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.dlq import DeadLetter
from techai_webutils.foundation.resilience.async_retry import retry_transient_async
from techai_webutils.foundation.resilience.timeout import with_timeout

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.dedup import Deduplicator
    from techai_webutils.core.interfaces.messaging import Message, MessageHandler
    from techai_webutils.foundation.resilience.dlq import DeadLetterQueue


@dataclass(frozen=True, slots=True)
class EventStackDeps:
    """Collaborators for the EventHandler stack.

    A None layer (or non-positive timeout / ``retry_max_attempts`` <= 1) is skipped, so callers
    opt into exactly the concerns they have wired.
    """

    dedup: Deduplicator | None = None
    """Skips a message whose key was already processed (None disables dedup)."""

    key_of: Callable[[Message], str] | None = None
    """Extracts the dedup key from a message; None defaults to ``msg.id``."""

    dlq: DeadLetterQueue | None = None
    """Routes a message that still fails after retries (None disables the DLQ)."""

    circuit_breaker: CircuitBreakerInterface | None = None
    """Fails fast when the handler's downstream is down (None disables it)."""

    retry_max_attempts: int = 1
    """Retries a transient handler failure; <= 1 disables retry."""

    timeout_seconds: float | None = None
    """Bounds each handle attempt, in seconds; None/<= 0 disables the timeout layer."""


def wrap_handler(inner: MessageHandler, deps: EventStackDeps) -> MessageHandler:
    """Compose the EventHandler stack around ``inner``.

    Outermost -> innermost: ``Dedup -> DeadLetter -> [Retry -> CircuitBreaker -> Timeout] ->
    Handler``. A message whose key was already processed is acked without re-running; one that
    still fails after retries is routed to the dead-letter queue and acked when it lands there,
    otherwise the failure is re-raised so the source message is redriven.
    """
    key_of: Callable[[Message], str] = deps.key_of or (lambda m: m.id)

    def start(msg: Message) -> Awaitable[None]:
        """Start one handle attempt, bounded by the optional timeout."""
        if deps.timeout_seconds and deps.timeout_seconds > 0:
            return with_timeout(inner(msg), deps.timeout_seconds)
        return inner(msg)

    async def attempt(msg: Message) -> None:
        """Run one handle attempt under the optional timeout + circuit breaker.

        The handler coroutine is created only once the breaker admits the call, so an open breaker
        fails fast without leaving an un-awaited coroutine behind.
        """
        if deps.circuit_breaker is not None:
            with deps.circuit_breaker:
                await start(msg)
        else:
            await start(msg)

    runner: MessageHandler = attempt
    if deps.retry_max_attempts > 1:
        runner = retry_transient_async(max_attempts=deps.retry_max_attempts)(attempt)

    async def wrapped(msg: Message) -> None:
        """Dedup-gate, run the retry/timeout/CB inner, and dead-letter a terminal failure."""
        if deps.dedup is not None and await deps.dedup.seen(key_of(msg)):
            return
        try:
            await runner(msg)
        except Exception as exc:  # terminal failure is routed to the DLQ (or redriven)
            if deps.dlq is not None and await deps.dlq.send(
                DeadLetter(id=msg.id, payload=msg.payload, reason=str(exc)),
            ):
                return
            raise

    return wrapped
