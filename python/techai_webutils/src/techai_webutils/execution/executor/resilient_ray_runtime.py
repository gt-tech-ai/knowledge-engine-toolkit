"""ResilientRayRuntime: retry-with-backoff + logging decorator over the RayRuntime seam.

The Ray analogue of the client resilience decorators (e.g. ``clients/kb_ingestion.RetryingIngestor``):
a cold Ray Client connect can intermittently time out the head's per-connection client server
(head-side ``proxier`` "Timeout waiting for channel"), so this wraps the connect in
``retry_transient_async`` (tenacity exponential-backoff + jitter) and logs each attempt. It is
composed at ``executor_from_config`` OUTSIDE ``RealRayRuntime`` — resilience is a decorator, never
inlined into the runtime (ARCHITECTURE.md#decorators).

The connect is single-flight: every concurrent ``submit`` of a batch waits on ONE retry sequence
instead of running its own, and when that sequence is exhausted the callers that were waiting on it
share its ``RayUnavailableError`` rather than each starting another full sequence. Without this, a
batch of N items against a down cluster made N x ``max_attempts`` connect attempts, one item at a time.

It touches no ``ray`` (it delegates to an inner ``RayRuntime``), so ``techai_webutils`` still imports
without the ray extra. The task ``submit`` itself is NOT retried here — ``RayExecutor``'s ``fan_out``
already isolates per-item failures; only the (idempotent) connect is retried.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import UnavailableError
from techai_webutils.execution.executor.ray_runtime import RayRuntime, RayUnavailableError
from techai_webutils.foundation.logger import get_logger
from techai_webutils.foundation.resilience.async_retry import retry_transient_async

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import StepResult

logger = get_logger(__name__)

# Cold-connect retry budget. The head's client-server channel timeout is ~70s, so a couple of attempts
# with exponential backoff comfortably rides out a transient spawn hiccup without a runaway retry storm.
_DEFAULT_MAX_ATTEMPTS = 5
"""Default number of cold-connect attempts (the tenacity retry stop condition)."""
_DEFAULT_BASE_DELAY_SECONDS = 1.0
"""Default initial backoff delay in seconds before the first retry."""
_DEFAULT_MAX_DELAY_SECONDS = 20.0
"""Default ceiling in seconds on the exponential backoff between retries."""


class ResilientRayRuntime(RayRuntime):
    """Wraps a ``RayRuntime``, retrying a flaky cold connect with exponential backoff + jitter.

    ``warm_up`` (and the lazy connect inside ``submit``) is retried on any connect failure — translated
    to a transient ``UnavailableError`` so ``retry_transient_async`` acts on it and each attempt is
    logged. Every connect error is treated as retryable: the inner runtime clears a half-open or stale
    client between attempts, so even a refusal such as "already connected" is recoverable. The connect
    is single-flight (see the module docstring), and an exhausted sequence raises
    ``RayUnavailableError``, which ``RayExecutor`` treats as batch-wide. Once connected, each
    ``submit`` pays only the inner runtime's liveness check. Mirrors ``RetryingIngestor``: same seam
    in, same seam out.
    """

    def __init__(
        self,
        inner: RayRuntime,
        *,
        max_attempts: int = _DEFAULT_MAX_ATTEMPTS,
        base_delay: float = _DEFAULT_BASE_DELAY_SECONDS,
        max_delay: float = _DEFAULT_MAX_DELAY_SECONDS,
    ) -> None:
        """Wrap ``inner`` and build the reusable transient-retry policy (exponential backoff + jitter)."""
        self._inner: RayRuntime = inner
        # Built once — the tenacity decorator is stateless and applies a fresh retry state per call.
        self._retry: Callable[
            [Callable[[], Awaitable[None]]], Callable[[], Awaitable[None]]
        ] = retry_transient_async(
            max_attempts=max_attempts,
            base_delay=base_delay,
            max_delay=max_delay,
        )
        self._connected: bool = False
        self._lock: asyncio.Lock = asyncio.Lock()
        """Serializes connect sequences so concurrent callers share one."""
        self._completed: int = 0
        """Count of finished connect sequences; a waiter compares it to detect one that ended meanwhile."""
        self._last_failure: RayUnavailableError | None = None
        """The most recent sequence's failure, or None when it connected."""

    async def warm_up(self) -> None:
        """Establish the Ray connection, retrying a transient cold-connect failure with backoff.

        Raises:
            RayUnavailableError: When the connect retries are spent, or when the sequence this call
                waited on was spent.

        """
        seen = self._completed
        async with self._lock:
            if self._completed != seen:
                # A sequence finished while this call waited for the lock: share its outcome
                # instead of starting another.
                if self._last_failure is not None:
                    raise self._last_failure
                return
            try:
                await self._retry(self._connect)()
            except UnavailableError as exc:
                failure = RayUnavailableError(str(exc))
                self._last_failure = failure
                self._connected = False
                raise failure from exc
            else:
                self._last_failure = None
            finally:
                self._completed += 1

    async def submit[T](
        self, fn: Callable[[T], Awaitable[StepResult]], item: T
    ) -> StepResult:
        """Ensure the connection (retried), then dispatch the item; the task itself is not retried here.

        The connect sequence (and the inner runtime's liveness probe) runs only while disconnected,
        not per item. A connection the inner runtime reports lost marks this runtime disconnected,
        so the next batch's ``warm_up`` reconnects.

        Raises:
            RayUnavailableError: When the connect retries are spent or the connection is lost.

        """
        if not self._connected:
            await self.warm_up()
        try:
            return await self._inner.submit(fn, item)
        except RayUnavailableError:
            self._connected = False
            raise

    async def _connect(self) -> None:
        """One connect attempt: delegate to the inner runtime, re-raising a failure as transient.

        A cold Ray Client connect surfaces raw ``RuntimeError``/``grpc``/``ValueError`` errors that are
        not ``AppError``s, so ``retry_transient_async`` (which retries transient ``AppError``s) would
        ignore them — translate to a transient ``UnavailableError`` so the backoff policy retries, and
        log the attempt for observability.
        """
        try:
            await self._inner.warm_up()
        except Exception as exc:  # any connect failure is retried — the inner runtime resets the client between tries
            logger.warning(
                "ray connect attempt failed; retrying with backoff", error=str(exc)
            )
            msg = f"ray cluster connect failed: {exc}"
            raise UnavailableError(msg) from exc
        if not self._connected:
            self._connected = True
            logger.info("ray connection established")
