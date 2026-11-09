"""Lazily opened aiobotocore bedrock-runtime client for the Bedrock embedding provider.

Carved out of unit coverage like the other aiobotocore wrappers: its lines are the real
    SDK calls,
exercised against real Bedrock, not by unit tests.
"""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import Any

import aiobotocore.session  # type: ignore[import-untyped]
from aiobotocore.config import AioConfig  # type: ignore[import-untyped]


class LazyBedrockRuntime:
    """A ``BedrockRuntimeInvoker`` that opens one bedrock-runtime client on first use.

    And reuses it.

    Construction does no I/O, so the composition root can build the provider at startup.
    """

    def __init__(
        self, *, region: str, timeout_seconds: float, endpoint: str | None = None
    ) -> None:
        """Store the region, the per-call timeout, and an optional endpoint.

        The client opens on first use.

        ``timeout_seconds`` bounds both the connect and the read of each ``InvokeModel``
        call.
        """
        self._region = region
        self._endpoint = endpoint
        self._config = AioConfig(
            connect_timeout=timeout_seconds, read_timeout=timeout_seconds
        )
        self._session = aiobotocore.session.get_session()
        self._stack: AsyncExitStack | None = None
        self._client: Any = None

    async def invoke_model(
        self, *, modelId: str, body: str, contentType: str, accept: str
    ) -> dict[str, Any]:
        """Call bedrock-runtime ``InvokeModel``.

        Opening the shared client first if needed.
        """
        if self._client is None:
            self._stack = stack = AsyncExitStack()
            self._client = await stack.enter_async_context(
                self._session.create_client(
                    "bedrock-runtime",
                    region_name=self._region,
                    endpoint_url=self._endpoint,
                )
            )
        return await self._client.invoke_model(
            modelId=modelId, body=body, contentType=contentType, accept=accept
        )

    async def aclose(self) -> None:
        """Close the shared client if one was opened (idempotent)."""
        if self._stack is not None:
            await self._stack.aclose()
            self._stack = None
            self._client = None
