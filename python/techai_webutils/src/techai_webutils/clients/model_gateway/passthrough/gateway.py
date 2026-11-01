"""``PassthroughGateway`` — wraps one provider and delegates byte-for-byte (the default kind).

``resolve`` ignores the context. Each served call records a ``passthrough`` route decision naming
the wrapped model; a stream records it once fully consumed.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Self, TypeVar, override

from techai_webutils.core.interfaces.model_gateway import (
    ModelGateway,
    RouteDecision,
    set_route_decision,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from techai_webutils.core.interfaces.llm import (
        LLMConfig,
        LLMMessage,
        LLMProvider,
        LLMResponse,
        StreamUsage,
    )
    from techai_webutils.core.interfaces.model_gateway import RouteContext

_T = TypeVar("_T")


class PassthroughGateway(ModelGateway):
    """A gateway over exactly one provider: adopting it changes no behaviour."""

    def __init__(self, inner: LLMProvider) -> None:
        """Wrap ``inner``."""
        self._inner = inner

    @property
    def wrapped_provider(self) -> LLMProvider:
        """The provider every call is delegated to (decorators read its identity)."""
        return self._inner

    def _record(self) -> None:
        """Record the passthrough decision for the call just served."""
        model = self._inner.model_name()
        set_route_decision(
            RouteDecision(
                reason="passthrough", decider="local", model_id=model, endpoint=model
            )
        )

    async def _recorded(self, items: AsyncIterator[_T]) -> AsyncIterator[_T]:
        """Re-yield ``items`` and record the decision once they are exhausted."""
        async for item in items:
            yield item
        self._record()

    @override
    def resolve(self, ctx: RouteContext) -> LLMProvider:
        """Return the wrapped provider for any context."""
        return self._inner

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Delegate to the wrapped provider."""
        response = await self._inner.complete(messages, config)
        self._record()
        return response

    @override
    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        """Delegate to the wrapped provider's stream."""
        return self._recorded(await self._inner.stream(messages, config))

    @override
    async def stream_with_usage(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str | StreamUsage]:
        """Delegate to the wrapped provider's usage-reporting stream."""
        return self._recorded(await self._inner.stream_with_usage(messages, config))

    @override
    def model_name(self) -> str:
        """Return the wrapped provider's model."""
        return self._inner.model_name()

    async def __aenter__(self) -> Self:
        """Enter the wrapped provider's context."""
        await self._inner.__aenter__()
        return self

    async def __aexit__(self, *exc: object) -> None:
        """Exit the wrapped provider's context (releases its client)."""
        await self._inner.__aexit__(None, None, None)


_INTERFACE_CHECK: type[ModelGateway] = PassthroughGateway
"""Type-checker assertion that ``PassthroughGateway`` satisfies ``ModelGateway``."""
