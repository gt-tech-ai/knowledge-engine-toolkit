"""Job decorator stack for a scheduled job coroutine (ARCHITECTURE.md#decorator-order).

Composes, outermost -> innermost, ``LeaderElection -> RateLimit -> [Retry -> Timeout] ->
Job``. Mirrors Go's ``clients/jobs/decorators.WrapJob``. A non-leader instance skips the run
(a job scheduled across N replicas fires once); the rate limiter then smooths the start
rate before the inner retry/timeout runs the job. Built from the existing foundation
primitives (``retry_transient_async`` + ``with_timeout``), so the mechanism is composed once,
not re-implemented.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.foundation.resilience.async_retry import retry_transient_async
from techai_webutils.foundation.resilience.timeout import with_timeout

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.leader import LeaderElector
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter

JobFunc = Callable[[], Awaitable[None]]
"""One run of a scheduled job."""


@dataclass(frozen=True, slots=True)
class JobStackDeps:
    """Collaborators for the Job stack.

    A None layer (or non-positive timeout / ``retry_max_attempts`` <= 1) is skipped, so callers
    opt into exactly the concerns they have wired.
    """

    leader: LeaderElector | None = None
    """Gates execution: a non-leader instance skips the job (None = always run)."""

    limiter: RateLimiter | None = None
    """Bounds the job's start rate, awaiting a token per run (None disables it)."""

    retry_max_attempts: int = 1
    """Retries a transient failure; <= 1 disables retry."""

    timeout_seconds: float | None = None
    """Bounds each run attempt, in seconds; None/<= 0 disables the timeout layer."""


def wrap_job(fn: JobFunc, deps: JobStackDeps) -> JobFunc:
    """Compose the Job stack around ``fn``.

    Outermost -> innermost: ``LeaderElection -> RateLimit -> [Retry -> Timeout] -> Job``. A
    non-leader instance returns without running; otherwise the limiter awaits a token and the
    inner retry/timeout runs the job. Each retry attempt builds a fresh coroutine (and a fresh
    timeout), so a transient failure re-runs the job cleanly.
    """

    async def attempt() -> None:
        """Run one job attempt under the optional per-attempt timeout."""
        if deps.timeout_seconds and deps.timeout_seconds > 0:
            await with_timeout(fn(), deps.timeout_seconds)
        else:
            await fn()

    runner: JobFunc = attempt
    if deps.retry_max_attempts > 1:
        runner = retry_transient_async(max_attempts=deps.retry_max_attempts)(attempt)

    async def wrapped() -> None:
        """Gate on leadership, await a rate-limit token, then run the retry/timeout inner."""
        if deps.leader is not None and not await deps.leader.is_leader():
            return
        if deps.limiter is not None:
            await deps.limiter.wait()
        await runner()

    return wrapped
