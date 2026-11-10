"""ResilientRayRuntime: retry-with-backoff + logging decorator over the RayRuntime seam.

The Ray analogue of the client resilience decorators (e.g.
``clients/kb_ingestion.RetryingIngestor``): a cold Ray Client connect can intermittently
time out the head's per-connection client server (head-side ``proxier`` "Timeout waiting
for channel"), so this wraps the connect in ``retry_transient_async`` (tenacity
exponential-backoff + jitter) and logs each attempt. It is composed at
``executor_from_config`` OUTSIDE ``RealRayRuntime`` — resilience is a decorator, never
inlined into the runtime (ARCHITECTURE.md#decorators).

The connect is single-flight: every concurrent ``submit`` of a batch waits on ONE retry
sequence instead of running its own, and when that sequence is exhausted the callers that
were waiting on it share its ``RayUnavailableError`` rather than each starting another
full sequence. Without this, a batch of N items against a down cluster made N x
``max_attempts`` connect attempts, one item at a time.

It also reports the connection's health without deciding anything about it:
``consecutive_connect_failures()`` (reset by a successful connect) and the
``ray_connect_failures_total{reason}`` counter, where ``reason`` is the failure's gRPC
status name; and ``head_reachable()``, a TCP probe of the head that tells "client stuck,
head up" from "head down". Thresholds and liveness wiring belong to the consumer.

It touches no ``ray`` (it delegates to an inner ``RayRuntime``), so ``techai_webutils``
still imports without the ray extra. The task ``submit`` itself is NOT retried here —
``RayExecutor``'s ``fan_out`` already isolates per-item failures; only the (idempotent)
connect is retried.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.errors.errors import UnavailableError
from techai_webutils.execution.executor.ray_runtime import RayRuntime, RayUnavailableError
from techai_webutils.foundation.logger import get_logger
from techai_webutils.foundation.resilience.async_retry import retry_transient_async

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import StepResult
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider

logger = get_logger(__name__)

# Cold-connect retry budget. The head's client-server channel timeout is ~70s, so a couple
# of attempts with exponential backoff comfortably rides out a transient spawn hiccup
# without a runaway retry storm.
_DEFAULT_MAX_ATTEMPTS = 5
"""Default number of cold-connect attempts (the tenacity retry stop condition)."""
_DEFAULT_BASE_DELAY_SECONDS = 1.0
"""Default initial backoff delay in seconds before the first retry."""
_DEFAULT_MAX_DELAY_SECONDS = 20.0
"""Default ceiling in seconds on the exponential backoff between retries."""
_DEFAULT_HEAD_PROBE_TIMEOUT_SECONDS = 2.0
"""Default ceiling in seconds on one TCP probe of the head."""
_UNKNOWN_REASON = "UNKNOWN"
"""The ``reason`` label of a connect failure that carries no gRPC status."""


def _grpc_status_name(exc: BaseException) -> str:
    """Return the gRPC status name carried by ``exc`` or an exception in its chain.

    Else ``UNKNOWN``.

    A ``grpc.RpcError`` exposes ``code()`` returning a ``grpc.StatusCode``; it is read
    duck-typed so this module imports no gRPC. The walk follows ``__cause__``, then
    ``__context__``.

    Args:
        exc: The connect failure.

    Returns:
        The gRPC status name carried by ``exc`` or an exception in its chain.

    """
    seen: set[int] = set()
    current: BaseException | None = exc
    while current is not None and id(current) not in seen:
        seen.add(id(current))
        code = getattr(current, "code", None)
        if callable(code):
            name = getattr(code(), "name", None)
            if isinstance(name, str):
                return name
        current = current.__cause__ or current.__context__
    return _UNKNOWN_REASON


def _failure_counter(metrics: MetricsProvider | None) -> MetricCounter | None:
    """Return the ``ray_connect_failures_total{reason}`` counter.

    Or ``None`` without metrics.

    Args:
        metrics: Where the counter is created; ``None`` creates none.

    Returns:
        The ``ray_connect_failures_total{reason}`` counter.

    """
    if metrics is None:
        return None
    return metrics.counter(
        "ray_connect_failures_total",
        "Failed Ray Client connect attempts, by gRPC status.",
        ["reason"],
    )


def _split_address(address: str) -> tuple[str, int]:
    """Split ``host:port`` into its host and port.

    Args:
        address: The ``host:port`` address.

    Returns:
        The host and the port.

    Raises:
        AppValueError: The address has no port, or the port is not a number.

    """
    host, sep, port = address.rpartition(":")
    if not sep or not host or not port.isdigit():
        msg = f"head_probe_address must be host:port, got {address!r}"
        raise AppValueError(msg)
    return host, int(port)


@dataclass(frozen=True, kw_only=True)
class ResilientRayRuntimeOptions:
    """What ``ResilientRayRuntime`` takes, grouped into one value."""

    max_attempts: int = _DEFAULT_MAX_ATTEMPTS
    """Connect attempts per sequence."""

    base_delay: float = _DEFAULT_BASE_DELAY_SECONDS
    """Initial backoff in seconds."""

    max_delay: float = _DEFAULT_MAX_DELAY_SECONDS
    """Ceiling on the backoff in seconds."""

    metrics: MetricsProvider | None = None
    """Emits ``ray_connect_failures_total{reason}``; ``None`` emits nothing."""

    head_probe_address: str | None = None
    """The head's ``host:port`` that ``head_reachable`` probes; ``None`` makes the probe
    refuse, since a guessed address would report the wrong host.
    """

    head_probe_timeout_seconds: float = _DEFAULT_HEAD_PROBE_TIMEOUT_SECONDS
    """Ceiling on one probe."""


@final
class ResilientRayRuntime(RayRuntime):
    """Wraps a ``RayRuntime``.

    Retrying a flaky cold connect with exponential backoff + jitter.

    ``warm_up`` (and the lazy connect inside ``submit``) is retried on any connect failure
    — translated to a transient ``UnavailableError`` so ``retry_transient_async`` acts on
    it and each attempt is logged. Every connect error is treated as retryable: the inner
    runtime clears a
        half-open or stale
    client between attempts, so even a refusal such as "already connected" is recoverable.
    The connect is single-flight (see the module docstring), and an exhausted sequence
    raises ``RayUnavailableError``, which ``RayExecutor`` treats as batch-wide. Once
    connected, each ``submit`` pays only the inner runtime's liveness check. Mirrors
    ``RetryingIngestor``: same seam in, same seam out.
    """

    def __init__(self, inner: RayRuntime, options: ResilientRayRuntimeOptions) -> None:
        """Wrap ``inner`` and build the retried connect once.

        (exponential backoff + jitter).

        Args:
            inner: The runtime whose connect is retried.
            options: The settings and collaborators, grouped.

        """
        self._inner: RayRuntime = inner
        # Built once per runtime: the tenacity wrapper is stateless and applies a fresh
        # retry state per call, so rebuilding it on every reconnect would only add work.
        self._connect_with_retry: Callable[[], Awaitable[None]] = retry_transient_async(
            max_attempts=options.max_attempts,
            base_delay=options.base_delay,
            max_delay=options.max_delay,
        )(self._connect)
        self._failures: int = 0
        """Connect attempts that failed since the last successful one."""
        self._failure_counter = _failure_counter(options.metrics)
        self._head_probe_address = options.head_probe_address
        self._head_probe_timeout_seconds = options.head_probe_timeout_seconds
        self._connected: bool = False
        self._lock: asyncio.Lock = asyncio.Lock()
        """Serializes connect sequences so concurrent callers share one."""
        self._completed: int = 0
        """Count of finished connect sequences; a waiter compares it to detect one that
        ended meanwhile."""
        self._last_failure: RayUnavailableError | None = None
        """The most recent sequence's failure, or None when it connected."""

    @override
    async def warm_up(self) -> None:
        """Establish the Ray connection, retrying a transient cold-connect failure.

        Raises:
            RayUnavailableError: The connect retries were spent, by this call or by the
                sequence it waited on (then caused by that sequence's failure).

        """
        seen = self._completed
        async with self._lock:
            if self._completed != seen:
                # A sequence finished while this call waited for the lock: share its
                # outcome instead of starting another.
                if self._last_failure is not None:
                    msg = str(self._last_failure)
                    raise RayUnavailableError(msg) from self._last_failure
                return
            try:
                await self._connect_with_retry()
            except UnavailableError as exc:
                self._last_failure = RayUnavailableError(str(exc))
                self._connected = False
                msg = str(exc)
                raise RayUnavailableError(msg) from exc
            else:
                self._last_failure = None
            finally:
                self._completed += 1

    @override
    async def submit[T](
        self, fn: Callable[[T], Awaitable[StepResult]], item: T
    ) -> StepResult:
        """Ensure the connection (retried), then dispatch the item.

        The task itself is not retried here.

        The connect sequence (and the inner runtime's liveness probe) runs only while
        disconnected, not per item. A connection the inner runtime reports lost marks this
        runtime disconnected, so the next batch's ``warm_up`` reconnects.

        Args:
            fn: The step.
            item: The item.

        Returns:
            The step result for the item.

        Raises:
            RayUnavailableError: When the connect retries are spent or the connection is
                lost.

        """
        if not self._connected:
            await self.warm_up()
        try:
            return await self._inner.submit(fn, item)
        except RayUnavailableError:
            self._connected = False
            raise

    def consecutive_connect_failures(self) -> int:
        """Return the connect attempts that failed since the last successful connect.

        Returns:
            The connect attempts that failed since the last successful connect.

        """
        return self._failures

    async def head_reachable(self) -> bool:
        """Return whether the head accepts a TCP connection on ``head_probe_address``.

        Independent of the Ray Client: True while the client keeps failing means the
            client is
        stuck and the head is up; False means the head itself is down or unreachable.

        Returns:
            Whether the head accepts a TCP connection on ``head_probe_address``.

        Raises:
            AppValueError: No ``head_probe_address`` is configured, or it is not
                ``host:port``.

        """
        if not self._head_probe_address:
            msg = "head_reachable needs a head_probe_address (host:port of the Ray head)"
            raise AppValueError(msg)
        host, port = _split_address(self._head_probe_address)
        try:
            async with asyncio.timeout(self._head_probe_timeout_seconds):
                _, writer = await asyncio.open_connection(host, port)
        except (OSError, TimeoutError):
            return False
        writer.close()
        await writer.wait_closed()
        return True

    @override
    async def cluster_cpus(self) -> float:
        """Ensure the connection (retried), then return the inner runtime's cluster CPUs.

        A callee raises ``RayUnavailableError``: When the connect retries are spent.

        Returns:
            The cluster CPU count.

        """
        if not self._connected:
            await self.warm_up()
        return await self._inner.cluster_cpus()

    async def _connect(self) -> None:
        """One connect attempt: delegate to the inner runtime.

        Re-raising a failure as transient.

        A cold Ray Client connect surfaces raw ``RuntimeError``/``grpc``/``ValueError``
        errors that are not ``AppError``s, so ``retry_transient_async`` (which retries
        transient ``AppError``s) would ignore them — translate to a transient
        ``UnavailableError`` so the backoff policy retries, and log the attempt for
        observability.

        Raises:
            UnavailableError: Ray cluster connect failed: ``exc``.

        """
        try:
            await self._inner.warm_up()
        # any connect failure is retried — the inner runtime resets the client between
        # tries
        except Exception as exc:
            self._failures += 1
            reason = _grpc_status_name(exc)
            if self._failure_counter is not None:
                self._failure_counter.inc(1.0, reason=reason)
            logger.warning(
                "ray connect attempt failed; retrying with backoff",
                error=str(exc),
                reason=reason,
                consecutive_failures=self._failures,
            )
            msg = f"ray cluster connect failed: {exc}"
            raise UnavailableError(msg) from exc
        self._failures = 0
        if not self._connected:
            self._connected = True
            logger.info("ray connection established")
