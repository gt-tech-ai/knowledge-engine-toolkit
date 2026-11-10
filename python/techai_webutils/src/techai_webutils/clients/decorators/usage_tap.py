"""``UsageTapProvider``: hands each model call's terminal usage to an injected callback.

A caller that sees only an answer's text (a workflow over a streaming provider) still
needs the model and token counts that produced it, e.g. to store them with a cached answer
or an audit record. The tap is an ``LLMProvider`` decorator, outermost on the provider:
``complete`` records its response's usage; ``stream`` reads the provider's
``stream_with_usage``, re-yields only the text and records the terminal ``StreamUsage``
once the stream is exhausted; ``stream_with_usage`` re-yields every item and records the
usage the same way. ``model_name`` and the async context are the wrapped provider's. Where
the usage goes (a request-scoped slot, a log) is the consumer's ``record`` callback.

The model recorded is the catalog id a model gateway served the call with (its route
decision, read in the call's own context), falling back to the provider's model id. A
routed gateway sets a stream's decision only after its last item, so reading it on the
usage item would name the previous call's model or none.
"""

from __future__ import annotations

from dataclasses import replace
from typing import TYPE_CHECKING, Self, cast, override

from techai_webutils.core.interfaces.llm import LLMProvider, StreamUsage
from techai_webutils.core.interfaces.model_gateway import current_route_decision

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Callable
    from types import TracebackType

    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage, LLMResponse


def tap_usage(inner: LLMProvider, record: Callable[[StreamUsage], None]) -> LLMProvider:
    """Wrap ``inner`` in a ``UsageTapProvider`` handing each call's usage to ``record``.

    Args:
        inner: The (decorated) provider.
        record: Receives each call's terminal usage.

    Returns:
        The tapped provider.

    """
    return UsageTapProvider(inner, record)


class UsageTapProvider(LLMProvider):
    """``LLMProvider`` decorator that hands each call's terminal usage to a callback."""

    def __init__(self, inner: LLMProvider, record: Callable[[StreamUsage], None]) -> None:
        """Wrap ``inner``.

        Args:
            inner: The (decorated) provider.
            record: Receives each call's terminal usage.

        """
        self._inner: LLMProvider = inner
        self._record: Callable[[StreamUsage], None] = record

    @override
    async def __aenter__(self) -> Self:
        """Enter the wrapped provider's context.

        Returns:
            This tap.

        """
        _ = await self._inner.__aenter__()
        return self

    @override
    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> bool | None:
        """Exit the wrapped provider's context.

        Args:
            exc_type: The exception type, if any.
            exc_val: The exception, if any.
            exc_tb: The traceback, if any.

        Returns:
            The wrapped provider's suppression decision.

        """
        return await self._inner.__aexit__(exc_type, exc_val, exc_tb)

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Complete through the inner provider and record the response's usage.

        Args:
            messages: The conversation.
            config: The call's decoding config.

        Returns:
            The response.

        """
        response = await self._inner.complete(messages, config)
        self._emit(
            StreamUsage(
                model=response.model,
                input_tokens=response.input_tokens,
                output_tokens=response.output_tokens,
                finish_reason=response.finish_reason,
            )
        )
        return response

    @override
    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        """Stream the text of ``stream_with_usage``, recording its usage.

        Args:
            messages: The conversation.
            config: The call's decoding config.

        Returns:
            The text deltas.

        """
        # Without the usage item every item is text.
        return cast(
            "AsyncIterator[str]",
            self._recorded(
                await self._inner.stream_with_usage(messages, config), with_usage=False
            ),
        )

    @override
    async def stream_with_usage(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str | StreamUsage]:
        """Stream the inner provider's items, recording the usage once it ends.

        Args:
            messages: The conversation.
            config: The call's decoding config.

        Returns:
            The text deltas, then the usage when the provider reports it.

        """
        return self._recorded(
            await self._inner.stream_with_usage(messages, config), with_usage=True
        )

    @override
    def model_name(self) -> str:
        """Return the wrapped provider's default model id.

        Returns:
            The model id.

        """
        return self._inner.model_name()

    async def _recorded(
        self, items: AsyncIterator[str | StreamUsage], *, with_usage: bool
    ) -> AsyncIterator[str | StreamUsage]:
        """Re-yield ``items`` and record the usage at the end.

        The usage is re-yielded only ``with_usage``.

        Closing this stream early closes ``items`` too, so an abandoned answer stops the
        provider's stream instead of leaving it to the garbage collector.

        Args:
            items: A ``stream_with_usage`` iterator.
            with_usage: Whether the usage item is re-yielded (``stream`` drops it).

        Yields:
            The text deltas, then the usage when ``with_usage``.

        """
        usage: StreamUsage | None = None
        try:
            async for item in items:
                if isinstance(item, StreamUsage):
                    usage = item
                    if not with_usage:
                        continue
                yield item
            # Recorded once the inner stream is exhausted: a routed gateway sets its route
            # decision (the catalog id that served) only then, after yielding the usage.
            if usage is not None:
                self._emit(usage)
        finally:
            close = getattr(items, "aclose", None)
            if close is not None:
                await close()

    def _emit(self, usage: StreamUsage) -> None:
        """Hand ``usage`` to the callback.

        It is keyed by the catalog id of the route that served it.

        Args:
            usage: The provider's usage.

        """
        decision = current_route_decision()
        if decision is not None and decision.model_id:
            usage = replace(usage, model=decision.model_id)
        self._record(usage)
