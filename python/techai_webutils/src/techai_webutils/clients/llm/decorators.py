"""Cross-cutting LLM provider decorators (decorators live at the client-package top).

``FallbackLlmProvider`` wraps a primary + fallback ``LLMProvider`` and, when the primary is throttled
(429) or returns a server error (5xx), transparently retries the request on the fallback model. If
the fallback also fails it raises a ``UnavailableError`` (mapped to gRPC UNAVAILABLE / HTTP
503) rather than leaking a raw botocore error. A non-transient error (e.g. a validation 400) is
re-raised without a wasteful fallback. Throttle-vs-error is recorded on ``bedrock_fallback_activations
_total`` so the fallback rate is observable.
"""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Any, Self, cast

from prometheus_client import Counter

from techai_webutils.core.errors import UnavailableError
from techai_webutils.core.interfaces.llm import LLMProvider, text_only

if TYPE_CHECKING:
    from collections.abc import AsyncIterator
    from types import TracebackType

    from techai_webutils.core.interfaces.llm import (
        LLMConfig,
        LLMMessage,
        LLMResponse,
        StreamUsage,
    )

# Fallback activations, labelled by why the primary failed (throttle = 429, error = 5xx). Registered
# on import (only the bedrock factory path imports this module), so it appears on /metrics in stage/prod.
_FALLBACK_ACTIVATIONS = Counter(
    "bedrock_fallback_activations_total",
    "Times the primary LLM was throttled/errored and the request fell back to the secondary model.",
    ["reason"],
)
"""Prometheus counter of fallback activations, labelled by ``reason`` (``throttle`` / ``error``)."""

# Bedrock throttle error codes (in addition to a 429 status). Server errors are detected by a >=500
# status or these codes. Names are matched loosely so a duck-typed test exception works without botocore.
_THROTTLE_CODES = frozenset({"ThrottlingException", "TooManyRequestsException"})
"""Bedrock error codes treated as throttling (fall back to the secondary model)."""
_SERVER_CODES = frozenset({
    "InternalServerException",
    "ServiceUnavailableException",
    "ModelTimeoutException",
})
"""Bedrock error codes treated as server errors (fall back to the secondary model)."""
_HTTP_TOO_MANY_REQUESTS = 429
"""HTTP status classifying a response as throttled (429 Too Many Requests)."""
_HTTP_MIN_SERVER_ERROR = 500
"""Lowest HTTP status classifying a response as a server error (>= 500 → fall back)."""
# Error message for the both-models-failed case; UnavailableError maps it to gRPC UNAVAILABLE / HTTP 503.
_UNAVAILABLE_MSG = "knowledge_service_unavailable"
"""Error message raised when both primary and fallback fail (maps to gRPC UNAVAILABLE / HTTP 503)."""


def _botocore_response(exc: BaseException) -> dict[str, Any] | None:
    """Return the botocore-shaped ``response`` dict on ``exc`` or along its cause chain, else ``None``.

    Providers translate SDK failures into a coded ``AppError`` at their boundary (``raise ... from
    exc``, with the SDK error also kept as ``AppError.cause``), so the ``response`` usually sits on a
    cause, not on the raised exception itself.
    """
    seen: set[int] = set()
    current: BaseException | None = exc
    while current is not None and id(current) not in seen:
        seen.add(id(current))
        response = getattr(current, "response", None)
        if isinstance(response, dict):
            return cast("dict[str, Any]", response)
        current = current.__cause__ or getattr(current, "cause", None)
    return None


def _classify(exc: Exception) -> str | None:
    """Classify a provider error as ``"throttle"`` / ``"error"`` (fall back), or ``None`` (re-raise).

    Duck-types botocore's ``ClientError.response`` (``{"Error": {"Code"}, "ResponseMetadata":
    {"HTTPStatusCode"}}``), looked up on the error or the SDK error it wraps (``_botocore_response``),
    so the decorator neither imports botocore at module load nor couples tests to it. An error with no
    such response in its chain is not a Bedrock transient error and is re-raised.
    """
    response = _botocore_response(exc)
    if response is None:
        return None
    code = str(response.get("Error", {}).get("Code", ""))
    status = int(response.get("ResponseMetadata", {}).get("HTTPStatusCode", 0) or 0)
    if status == _HTTP_TOO_MANY_REQUESTS or code in _THROTTLE_CODES:
        return "throttle"
    if status >= _HTTP_MIN_SERVER_ERROR or code in _SERVER_CODES:
        return "error"
    return None


class FallbackLlmProvider(LLMProvider):
    """LLMProvider that retries a throttled/errored primary on a fallback model."""

    def __init__(self, primary: LLMProvider, fallback: LLMProvider) -> None:
        """Wrap the primary provider and the (cheaper) fallback provider."""
        self._primary = primary
        self._fallback = fallback
        # Owns the lifecycle of both wrapped providers: entering this composite enters primary then
        # fallback; the stack releases them in reverse (and rolls back a partial enter).
        self._stack = AsyncExitStack()

    async def __aenter__(self) -> Self:
        """Enter both wrapped providers' async contexts (primary, then fallback); return self.

        If entering the fallback raises after the primary was already entered, __aenter__
        propagates the exception and Python will NOT call __aexit__, so roll the stack back
        here (closing the entered primary) to avoid leaking a half-opened composite.
        """
        try:
            await self._stack.enter_async_context(self._primary)
            await self._stack.enter_async_context(self._fallback)
        except BaseException:
            await self._stack.aclose()
            raise
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> bool | None:
        """Exit both wrapped providers (fallback, then primary), propagating any suppression decision."""
        return await self._stack.__aexit__(exc_type, exc_val, exc_tb)

    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Complete on the primary; on a throttle/5xx, retry on the fallback (else re-raise / 503)."""
        try:
            return await self._primary.complete(messages, config)
        except Exception as exc:
            reason = _classify(exc)
            if reason is None:
                raise
            _FALLBACK_ACTIVATIONS.labels(reason=reason).inc()
            try:
                return await self._fallback.complete(messages, config)
            except Exception as fallback_exc:
                raise UnavailableError(_UNAVAILABLE_MSG) from fallback_exc

    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        """Stream from the primary; if it fails before any token, retry the fallback (else re-raise / 503)."""
        return text_only(await self.stream_with_usage(messages, config))

    async def stream_with_usage(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str | StreamUsage]:
        """Stream (with the serving model's usage last) from the primary, falling back before any token."""
        return self._stream_with_fallback(messages, config)

    async def _stream_with_fallback(
        self,
        messages: list[LLMMessage],
        config: LLMConfig | None,
    ) -> AsyncIterator[str | StreamUsage]:
        """Yield the primary stream; fall back only if it fails before emitting a token."""
        yielded = False
        try:
            async for delta in await self._primary.stream_with_usage(messages, config):
                yielded = True
                yield delta
            return
        except Exception as exc:
            reason = _classify(exc)
            # A mid-stream failure (tokens already sent) or a non-transient error can't be safely
            # retried on the fallback, so it propagates.
            if yielded or reason is None:
                raise
            _FALLBACK_ACTIVATIONS.labels(reason=reason).inc()
        try:
            async for delta in await self._fallback.stream_with_usage(messages, config):
                yield delta
        except Exception as fallback_exc:
            raise UnavailableError(_UNAVAILABLE_MSG) from fallback_exc

    def model_name(self) -> str:
        """Return the primary model id (the model a caller expects unless a fallback occurs)."""
        return self._primary.model_name()
