"""Lazily opened aiobotocore bedrock-runtime client for the Bedrock vision client."""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Unpack, cast, final

import aiobotocore.session
from aiobotocore.config import AioConfig

if TYPE_CHECKING:
    from techai_webutils.clients.vision.bedrock.client import (
        ConverseInvoker,
        ConverseRequest,
    )


@final
class LazyBedrockConverse:
    """A ``ConverseInvoker`` that opens one bedrock-runtime client on first use.

    Construction does no I/O, so the composition root can build the client at startup.
    """

    def __init__(self, *, region: str, timeout_seconds: float) -> None:
        """Store the region and the per-call timeout; the client opens on first use.

        Args:
            region: The AWS region.
            timeout_seconds: Bounds both the connect and the read of each call.

        """
        self._region = region
        self._config = AioConfig(
            connect_timeout=timeout_seconds, read_timeout=timeout_seconds
        )
        self._session = aiobotocore.session.get_session()
        self._stack: AsyncExitStack | None = None
        self._client: ConverseInvoker | None = None

    async def converse(self, **kwargs: Unpack[ConverseRequest]) -> dict[str, object]:
        """Call bedrock-runtime ``Converse``, opening the shared client first if needed.

        Args:
            **kwargs: The ``Converse`` request fields.

        Returns:
            The ``Converse`` response.

        """
        if self._client is None:
            self._stack = stack = AsyncExitStack()
            self._client = cast(
                "ConverseInvoker",
                await stack.enter_async_context(
                    self._session.create_client(
                        "bedrock-runtime",
                        region_name=self._region,
                        config=self._config,
                    )
                ),
            )
        return await self._client.converse(**kwargs)

    async def aclose(self) -> None:
        """Close the shared client if one was opened (idempotent)."""
        if self._stack is not None:
            await self._stack.aclose()
            self._stack = None
            self._client = None
