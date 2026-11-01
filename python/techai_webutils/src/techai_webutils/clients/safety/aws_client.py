"""A lazily opened aiobotocore client shared by the managed safety backends.

The client opens on the first scan (never at construction, so building the graph does no I/O)
and is released by ``aclose``. botocore failures cross the boundary as coded ``AppError``s
through the shared ``botocore_error_to_app_error``.
"""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Any, Protocol, cast

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable
    from contextlib import AbstractAsyncContextManager


class _AwsSession(Protocol):
    """The aiobotocore session slice used here (aiobotocore is unstubbed)."""

    def create_client(
        self, service_name: str, *, region_name: str
    ) -> AbstractAsyncContextManager[object]:
        """Return an async context manager that opens one service client."""
        ...


class LazyAwsClient:
    """Opens one AWS service client on first use and keeps it until ``aclose``."""

    def __init__(self, session: object, service: str, region: str) -> None:
        """Bind the aiobotocore session, service name and region; nothing is opened yet."""
        self._session = cast("_AwsSession", session)
        self._service = service
        self._region = region
        self._stack: AsyncExitStack | None = None
        self._client: object | None = None

    async def call(self, operation: str, **kwargs: object) -> dict[str, Any]:
        """Invoke ``operation`` on the client, opening it on first use.

        Raises:
            AppError: the call failed (``UNAVAILABLE`` when transient, else ``INTERNAL``).

        """
        from botocore.exceptions import BotoCoreError, ClientError  # noqa: PLC0415

        from techai_webutils.foundation.resilience.aws_boundary import (  # noqa: PLC0415
            botocore_error_to_app_error,
        )

        if self._client is None:
            self._stack = stack = AsyncExitStack()
            client_cm = self._session.create_client(
                self._service, region_name=self._region
            )
            self._client = await stack.enter_async_context(client_cm)
        method = cast(
            "Callable[..., Awaitable[dict[str, Any]]]", getattr(self._client, operation)
        )
        try:
            return await method(**kwargs)
        except (ClientError, BotoCoreError) as exc:
            raise botocore_error_to_app_error(
                exc, f"{self._service} {operation}"
            ) from exc

    async def aclose(self) -> None:
        """Close the client if one was opened (idempotent)."""
        if self._stack is not None:
            await self._stack.aclose()
            self._stack = None
            self._client = None
