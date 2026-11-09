"""A lazily opened aiobotocore client shared by the managed safety backends.

The client opens on the first scan (never at construction, so building the graph does no
I/O), once even under concurrent first scans, and is released by ``aclose``. Every call is
bounded by the injected ``timeout_s``; an expired call fails ``UNAVAILABLE`` (transient).
botocore failures cross the boundary as coded ``AppError``s through the shared
``botocore_error_to_app_error``.
"""

from __future__ import annotations

import asyncio
import importlib
from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Any, Protocol, cast

from techai_webutils.core.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable
    from contextlib import AbstractAsyncContextManager

    from botocore.exceptions import BotoCoreError as _LazyBotoCoreError
    from botocore.exceptions import ClientError as _LazyClientError


class _AwsSession(Protocol):
    """The aiobotocore session slice used here (aiobotocore is unstubbed)."""

    def create_client(
        self, service_name: str, *, region_name: str
    ) -> AbstractAsyncContextManager[object]:
        """Return an async context manager that opens one service client."""
        ...


class LazyAwsClient:
    """Opens one AWS service client on first use and keeps it until ``aclose``."""

    def __init__(
        self, session: object, service: str, region: str, *, timeout_s: float
    ) -> None:
        """Bind the aiobotocore session, service, region and per-call deadline.

        Nothing opens yet.
        """
        self._session = cast("_AwsSession", session)
        self._service = service
        self._region = region
        self._timeout_s = timeout_s
        self._stack: AsyncExitStack | None = None
        self._client: object | None = None
        self._opening = asyncio.Lock()

    async def _opened(self) -> object:
        """Return the client, opening it once.

        (concurrent first calls share one client).

        Returns:
            The client, opening it once.

        """
        async with self._opening:
            if self._client is None:
                stack = AsyncExitStack()
                client_cm = self._session.create_client(
                    self._service, region_name=self._region
                )
                self._client = await stack.enter_async_context(client_cm)
                self._stack = stack
            return self._client

    async def call(self, operation: str, **kwargs: object) -> dict[str, Any]:
        """Invoke ``operation`` on the client, opening it on first use.

        Returns:
            What the operation returns.

        Raises:
            AppError: the call failed (``UNAVAILABLE`` when transient or past the
                deadline, else ``INTERNAL``).

        """
        boto_core_error_cls = cast(
            "type[_LazyBotoCoreError]",
            importlib.import_module("botocore.exceptions").BotoCoreError,
        )
        client_error_cls = cast(
            "type[_LazyClientError]",
            importlib.import_module("botocore.exceptions").ClientError,
        )

        botocore_error_to_app_error = cast(
            "Callable[[Exception, str], AppError]",
            importlib.import_module(
                "techai_webutils.foundation.resilience.aws_boundary"
            ).botocore_error_to_app_error,
        )

        client = self._client if self._client is not None else await self._opened()
        method = cast(
            "Callable[..., Awaitable[dict[str, Any]]]", getattr(client, operation)
        )
        try:
            async with asyncio.timeout(self._timeout_s):
                return await method(**kwargs)
        except TimeoutError as exc:
            msg = f"{self._service} {operation} timed out after {self._timeout_s}s"
            raise AppError(ErrorCode.UNAVAILABLE, msg, cause=exc) from exc
        except (client_error_cls, boto_core_error_cls) as exc:
            raise botocore_error_to_app_error(
                exc, f"{self._service} {operation}"
            ) from exc

    async def aclose(self) -> None:
        """Close the client if one was opened (idempotent)."""
        if self._stack is not None:
            await self._stack.aclose()
            self._stack = None
            self._client = None
