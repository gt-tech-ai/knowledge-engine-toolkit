"""Runs a vision client's page reads through the client tier.

``vision_from_config`` returns an undecorated ``VisionClient``; ``decorate`` gives it a
``ClientBase``, so each page carries the timeout, retry, breaker, bulkhead and
observability the consumer's ``decoration.clients`` configures for the client's name. A
page read has no side effects, so it may be retried when the config turns retry on.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, final, override

from techai_webutils.clients.client import ClientBase
from techai_webutils.core.interfaces.vision import VisionClient

if TYPE_CHECKING:
    from techai_webutils.foundation.decorate import Decorations


def decorate(client: VisionClient, decorations: Decorations, name: str) -> VisionClient:
    """Return ``client`` with every page read run through the client tier.

    Args:
        client: The vision client to wrap.
        decorations: The decorators the client tier wraps each page read in.
        name: The client name (its ``decoration.clients`` key).

    Returns:
        ``client`` with every page read run through the client tier.

    """
    return _DecoratedVision(client, ClientBase(VisionClient, decorations, name))


@final
class _DecoratedVision(VisionClient):
    """A vision client whose page reads run through a ``ClientBase``."""

    def __init__(self, inner: VisionClient, client: ClientBase) -> None:
        """Bind the wrapped vision client and the client tier it runs through.

        Args:
            inner: The wrapped vision client.
            client: The client tier it runs through.

        """
        self._inner = inner
        self._client = client

    @override
    async def describe_page(self, png: bytes, *, model_id: str) -> str:
        """Read one page through the tier as op ``describe_page``.

        Args:
            png: The rendered page, PNG-encoded.
            model_id: The vision model to ask.

        Returns:
            The wrapped client's text.

        """
        return await self._client.exec(
            "describe_page", lambda: self._inner.describe_page(png, model_id=model_id)
        )
