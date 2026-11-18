"""Runs a vision client's page reads through the client tier.

``vision_from_config`` returns an undecorated ``VisionClient``; ``decorate`` gives it a
``ClientBase``, so each page carries the timeout, retry, breaker, bulkhead and
observability the consumer's ``decoration.clients`` configures for the client's name. A
page read has no side effects, so it may be retried when the config turns retry on.

The wrapper is a ``ManagedResource``: entering and leaving it enters and leaves the
wrapped client when that one owns a connection (the Bedrock client does), and does
nothing otherwise (the stub owns none). ``DelegatingAsyncResource`` needs a
``ManagedResource`` to wrap, which a ``VisionClient`` is not always, so the delegation is
written out here.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Self, final, override

from techai_webutils.clients.client import ClientBase
from techai_webutils.core.interfaces.lifecycle import ManagedResource
from techai_webutils.core.interfaces.vision import VisionClient

if TYPE_CHECKING:
    from types import TracebackType

    from techai_webutils.foundation.decorate import Decorations


def decorate(client: VisionClient, decorations: Decorations, name: str) -> VisionClient:
    """Return ``client`` with every page read run through the client tier.

    Args:
        client: The vision client to wrap.
        decorations: The decorators the client tier wraps each page read in.
        name: The client name (its ``decoration.clients`` key).

    Returns:
        ``client`` with every page read run through the client tier; a
        ``ManagedResource`` whose enter and exit reach ``client``'s own.

    """
    return _DecoratedVision(client, ClientBase(VisionClient, decorations, name))


@final
class _DecoratedVision(VisionClient):
    """A vision client whose page reads run through a ``ClientBase``.

    Its async context delegates to the wrapped client when that one is a
    ``ManagedResource``.
    """

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

    async def __aenter__(self) -> Self:
        """Enter the wrapped client's context when it has one; return self.

        Returns:
            This instance.

        """
        if isinstance(self._inner, ManagedResource):
            _ = await self._inner.__aenter__()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> bool | None:
        """Leave the wrapped client's context when it has one.

        Args:
            exc_type: The body's exception type, or ``None`` on success.
            exc_val: The body's exception, or ``None`` on success.
            exc_tb: The body's traceback, or ``None`` on success.

        Returns:
            The wrapped client's answer, or ``None`` when it has no context.

        """
        if isinstance(self._inner, ManagedResource):
            return await self._inner.__aexit__(exc_type, exc_val, exc_tb)
        return None
