"""Runs a publisher's sends through the client tier (mirrors Go ``messaging.Decorate``)."""

from __future__ import annotations

from typing import TYPE_CHECKING, Self, override

from techai_webutils.clients.client import ClientBase, Option, producer
from techai_webutils.core.interfaces.messaging import MessagePublisher

if TYPE_CHECKING:
    from types import TracebackType

    from techai_webutils.foundation.decorate import Decorations


def decorate(
    base: MessagePublisher, decorations: Decorations, name: str, *options: Option
) -> MessagePublisher:
    """Return ``base`` with every publish run through the client tier as the client ``name``.

    Each publish runs as the operation named by its topic, in a PRODUCER span, so the config
    tunes the publisher as a whole. It is retried only when the config turns retry on: a re-sent
    message is a duplicate an at-least-once consumer accepts.
    """
    return _DecoratedPublisher(
        base, ClientBase.wire(decorations, name, producer(), *options)
    )


class _DecoratedPublisher(MessagePublisher):
    """A publisher whose sends run through a ClientBase; its lifecycle is the wrapped one's."""

    def __init__(self, base: MessagePublisher, client: ClientBase) -> None:
        """Bind the wrapped publisher and the client tier it runs through."""
        self._base = base
        self._client = client

    async def __aenter__(self) -> Self:
        """Open the wrapped publisher."""
        await self._base.__aenter__()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Close the wrapped publisher."""
        await self._base.__aexit__(exc_type, exc_val, exc_tb)

    @override
    async def publish(self, topic: str, payload: bytes) -> None:
        """Send ``payload`` to ``topic`` through the tier."""
        await self._client.exec(topic, lambda: self._base.publish(topic, payload))

    @override
    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Send ``payloads`` to ``topic`` through the tier."""
        await self._client.exec(topic, lambda: self._base.publish_batch(topic, payloads))
