"""The exponential-backoff ``Retrier`` (mirrors Go ``retry/exponential``).

The decoration's retry layer runs an operation through it: a transient failure
(``classify.is_transient``) is retried with exponential backoff and jitter, honoring a server's
pushback; a permanent one is raised at once.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, override

from tenacity import (
    AsyncRetrying,
    RetryCallState,
    retry_if_exception,
    stop_after_attempt,
    stop_after_delay,
    wait_exponential_jitter,
)

from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.foundation.resilience.async_retry import retry_after_s
from techai_webutils.foundation.resilience.classify import is_transient

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


@dataclass(frozen=True, slots=True)
class ExponentialRetrierConfig:
    """Tunes the exponential retrier."""

    max_retries: int = 3
    """The retries after the first attempt."""

    initial_interval: float = 0.1
    """The first backoff, in seconds."""

    max_interval: float = 5.0
    """The longest single backoff, in seconds."""

    multiplier: float = 2.0
    """The backoff growth factor."""

    max_elapsed_time: float = 30.0
    """The total time budget across attempts, in seconds; zero means unbounded."""


class ExponentialRetrier(Retrier):
    """Retries transient failures with exponential backoff and jitter."""

    def __init__(
        self,
        config: ExponentialRetrierConfig,
        *,
        sleep: Callable[[float], Awaitable[None]] | None = None,
    ) -> None:
        """Bind the tuning; ``sleep`` (optional) replaces the wait between attempts."""
        self._config = config
        self._sleep = sleep

    @override
    async def retry(self, op: Callable[[], Awaitable[object]]) -> object:
        """Run ``op``, retrying a transient failure; the last failure is raised."""
        cfg = self._config
        stop = stop_after_attempt(cfg.max_retries + 1)
        if cfg.max_elapsed_time > 0:
            stop |= stop_after_delay(cfg.max_elapsed_time)
        backoff = wait_exponential_jitter(
            initial=cfg.initial_interval,
            max=cfg.max_interval,
            exp_base=cfg.multiplier,
            jitter=cfg.initial_interval,
        )

        def wait(state: RetryCallState) -> float:
            """Wait the backoff, or the server's pushback when longer (capped at max_interval)."""
            delay = backoff(state)
            err = state.outcome.exception() if state.outcome is not None else None
            pushback = retry_after_s(err) if err is not None else None
            return (
                delay if pushback is None else max(delay, min(pushback, cfg.max_interval))
            )

        kwargs: dict[str, object] = {}
        if self._sleep is not None:
            kwargs["sleep"] = self._sleep
        retrying = AsyncRetrying(
            retry=retry_if_exception(is_transient),
            stop=stop,
            wait=wait,
            reraise=True,
            **kwargs,  # pyright: ignore[reportArgumentType]
        )
        return await retrying(op)
