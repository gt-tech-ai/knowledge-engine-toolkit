"""Cross-cutting LLM provider decorators (decorators live at the client-package top).

``FallbackLlmProvider`` wraps a primary + fallback ``LLMProvider`` and, when the primary
is throttled (429) or returns a server error (5xx), transparently retries the request on
the fallback model. Providers code their SDK failures as ``AppError`` at their boundary,
so the decorator classifies the coded error by the botocore error it wraps. If the
fallback also fails it raises an ``UnavailableError`` (gRPC UNAVAILABLE / HTTP 503). A
non-transient coded error (e.g. a validation 400) is re-raised without a wasteful
fallback, and an uncoded exception (a bug) is never caught. Throttle-vs-error is recorded
on ``bedrock_fallback_activations_total`` so the fallback rate is observable.
"""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Self, cast, final, override

from prometheus_client import Counter

from techai_webutils.core.errors import AppError, UnavailableError
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

# Fallback activations, labelled by why the primary failed (throttle = 429, error = 5xx).
# Registered on import (only the bedrock factory path imports this module), so it appears
# on /metrics in stage/prod.
_FALLBACK_ACTIVATIONS = Counter(
    "bedrock_fallback_activations_total",
    "Times the primary LLM was throttled/errored and the request fell back to the "
    "secondary model.",
    ["reason"],
)
"""Prometheus counter of fallback activations, labelled by ``reason`` (``throttle`` /
``error``)."""

# Bedrock throttle error codes (in addition to a 429 status). Server errors are detected
# by a >=500 status or these codes. Names are matched loosely so a duck-typed test
# exception works without botocore.
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
# Error message for the both-models-failed case; UnavailableError maps it to gRPC
# UNAVAILABLE / HTTP 503.
_UNAVAILABLE_MSG = "knowledge_service_unavailable"
"""Error message raised when both primary and fallback fail (maps to gRPC UNAVAILABLE /
HTTP 503)."""


def _botocore_response(exc: BaseException) -> dict[str, dict[str, object]] | None:
    """Return the botocore-shaped ``response`` dict on ``exc`` or its causes.

    ``None`` when there is none.

    Providers translate SDK failures into a coded ``AppError`` at their boundary (``raise
    ... from exc``, with the SDK error also kept as ``AppError.cause``), so the
    ``response`` usually sits on a cause, not on the raised exception itself.

    Args:
        exc: The raised error.

    Returns:
        The botocore-shaped ``response`` dict on ``exc`` or along its cause chain.

    """
    seen: set[int] = set()
    current: BaseException | None = exc
    while current is not None and id(current) not in seen:
        seen.add(id(current))
        response = cast("object", getattr(current, "response", None))
        if isinstance(response, dict):
            return cast("dict[str, dict[str, object]]", response)
        current = current.__cause__ or getattr(current, "cause", None)
    return None


def _classify(exc: AppError) -> str | None:
    """Classify a provider error as ``"throttle"``, ``"error"`` or ``None``.

    The first two fall back; ``None`` re-raises.

    Duck-types botocore's ``ClientError.response`` (``{"Error": {"Code"},
    "ResponseMetadata": {"HTTPStatusCode"}}``), looked up on the error or the SDK error it
    wraps (``_botocore_response``), so the decorator neither imports botocore at module
    load nor couples tests to it. An error with no such response in its chain is not a
    Bedrock transient error and is re-raised.

    Args:
        exc: The primary's coded error.

    Returns:
        ``"throttle"`` or ``"error"`` when the error should fall back, else ``None``.

    """
    response = _botocore_response(exc)
    if response is None:
        return None
    code = str(response.get("Error", {}).get("Code", ""))
    status = int(
        cast("int", response.get("ResponseMetadata", {}).get("HTTPStatusCode", 0) or 0)
    )
    if status == _HTTP_TOO_MANY_REQUESTS or code in _THROTTLE_CODES:
        return "throttle"
    if status >= _HTTP_MIN_SERVER_ERROR or code in _SERVER_CODES:
        return "error"
    return None


@final
class FallbackLlmProvider(LLMProvider):
    """LLMProvider that retries a throttled/errored primary on a fallback model."""

    def __init__(self, primary: LLMProvider, fallback: LLMProvider) -> None:
        """Wrap the primary provider and the (cheaper) fallback provider.

        Args:
            primary: The provider tried first.
            fallback: The provider a throttled or failing primary falls back to.

        """
        self._primary = primary
        self._fallback = fallback
        # Owns the lifecycle of both wrapped providers: entering this composite enters
        # primary then fallback; the stack releases them in reverse (and rolls back a
        # partial enter).
        self._stack = AsyncExitStack()

    @override
    async def __aenter__(self) -> Self:
        """Enter both wrapped providers' async contexts (primary, then fallback).

        The providers are entered on a scratch stack that is handed to ``self`` only once
        both are open: if entering the fallback raises, Python will NOT call
        ``__aexit__``, so the scratch stack's own exit closes the entered primary and no
        half-opened composite leaks.

        Returns:
            This instance.

        """
        async with AsyncExitStack() as stack:
            _ = await stack.enter_async_context(self._primary)
            _ = await stack.enter_async_context(self._fallback)
            self._stack = stack.pop_all()
        return self

    @override
    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> bool | None:
        """Exit both wrapped providers, fallback first, then primary.

        Any suppression decision propagates.

        Args:
            exc_type: The exception's type, if any.
            exc_val: The exception, if any.
            exc_tb: Its traceback, if any.

        Returns:
            Whether the wrapped context suppresses the exception.

        """
        return await self._stack.__aexit__(exc_type, exc_val, exc_tb)

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Complete on the primary; on a throttle or 5xx, retry on the fallback.

        Otherwise it re-raises, or fails 503.

        Args:
            messages: The conversation so far, oldest first.
            config: Per-call settings (model, temperature, token limit); ``None`` uses the
                provider's defaults.

        Returns:
            The completion.

        Raises:
            AppError: The primary's coded error, when it does not warrant a fallback.
            UnavailableError: Both the primary and the fallback failed.

        """
        try:
            return await self._primary.complete(messages, config)
        except AppError as exc:
            reason = _classify(exc)
            if reason is None:
                raise
            _FALLBACK_ACTIVATIONS.labels(reason=reason).inc()
            try:
                return await self._fallback.complete(messages, config)
            except Exception as fallback_exc:
                raise UnavailableError(_UNAVAILABLE_MSG) from fallback_exc

    @override
    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        """Stream from the primary; before any token, a failure tries the fallback.

        Otherwise it re-raises, or fails 503.

        Args:
            messages: The conversation so far, oldest first.
            config: Per-call settings; ``None`` uses the provider's defaults.

        Returns:
            The stream of text deltas.

        """
        return text_only(await self.stream_with_usage(messages, config))

    @override
    async def stream_with_usage(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str | StreamUsage]:
        """Stream from the primary with the serving model's usage last.

        It falls back only before any token.

        Args:
            messages: The conversation so far, oldest first.
            config: Per-call settings; ``None`` uses the provider's defaults.

        Returns:
            The stream of text deltas, ending with the usage when the provider reports it.

        """
        return self._stream_with_fallback(messages, config)

    async def _stream_with_fallback(
        self,
        messages: list[LLMMessage],
        config: LLMConfig | None,
    ) -> AsyncIterator[str | StreamUsage]:
        """Yield the primary stream; fall back only if it fails before emitting a token.

        Args:
            messages: The conversation.
            config: Per-call settings; ``None`` uses the defaults.

        Yields:
            The primary stream.

        Raises:
            AppError: The primary's coded error, when it came mid-stream or does not
                warrant a fallback.
            UnavailableError: Both the primary and the fallback failed.

        """
        yielded = False
        try:
            async for delta in await self._primary.stream_with_usage(messages, config):
                yielded = True
                yield delta
        except AppError as exc:
            reason = _classify(exc)
            # A mid-stream failure (tokens already sent) or a non-transient error can't be
            # safely retried on the fallback, so it propagates.
            if yielded or reason is None:
                raise
            _FALLBACK_ACTIVATIONS.labels(reason=reason).inc()
        else:
            return
        try:
            async for delta in await self._fallback.stream_with_usage(messages, config):
                yield delta
        except Exception as fallback_exc:
            raise UnavailableError(_UNAVAILABLE_MSG) from fallback_exc

    @override
    def model_name(self) -> str:
        """Return the primary model id.

        It is the model a caller expects unless a fallback occurs.

        Returns:
            The primary model id.

        """
        return self._primary.model_name()
