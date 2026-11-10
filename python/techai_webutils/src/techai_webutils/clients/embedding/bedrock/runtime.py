"""Lazily opened aiobotocore bedrock-runtime client for the Bedrock embedding provider.

Carved out of unit coverage like the other aiobotocore wrappers: its lines are the real
SDK calls, exercised against real Bedrock, not by unit tests.
"""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Unpack, cast, final

import aiobotocore.session
from aiobotocore.config import AioConfig

if TYPE_CHECKING:
    from techai_webutils.clients.embedding.bedrock.provider import (
        BedrockRuntimeInvoker,
        InvokeModelRequest,
    )


@final
class LazyBedrockRuntime:
    """A ``BedrockRuntimeInvoker`` that opens one bedrock-runtime client.

    The client opens on first use and is reused.

    Construction does no I/O, so the composition root can build the provider at startup.
    """

    def __init__(
        self, *, region: str, timeout_seconds: float, endpoint: str | None = None
    ) -> None:
        """Store the region, the per-call timeout and an optional endpoint.

        The client opens on first use.

        ``timeout_seconds`` bounds both the connect and the read of each ``InvokeModel``
        call.

        Args:
            region: The AWS region.
            timeout_seconds: Each call's read timeout.
            endpoint: An endpoint override; ``None`` uses the region's.

        """
        self._region = region
        self._endpoint = endpoint
        self._config = AioConfig(
            connect_timeout=timeout_seconds, read_timeout=timeout_seconds
        )
        self._session = aiobotocore.session.get_session()
        self._stack: AsyncExitStack | None = None
        self._client: BedrockRuntimeInvoker | None = None

    async def invoke_model(
        self, **kwargs: Unpack[InvokeModelRequest]
    ) -> dict[str, object]:
        """Call bedrock-runtime ``InvokeModel``.

        The shared client opens first if needed.

        Args:
            **kwargs: The ``InvokeModel`` request fields.

        Returns:
            The decoded ``InvokeModel`` response.

        """
        if self._client is None:
            self._stack = stack = AsyncExitStack()
            self._client = cast(
                "BedrockRuntimeInvoker",
                await stack.enter_async_context(
                    self._session.create_client(
                        "bedrock-runtime",
                        region_name=self._region,
                        endpoint_url=self._endpoint,
                    )
                ),
            )
        return await self._client.invoke_model(**kwargs)

    async def aclose(self) -> None:
        """Close the shared client if one was opened (idempotent)."""
        if self._stack is not None:
            await self._stack.aclose()
            self._stack = None
            self._client = None
