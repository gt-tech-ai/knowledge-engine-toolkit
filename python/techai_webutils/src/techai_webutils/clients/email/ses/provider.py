"""SES email backend — sends via aiobotocore SESv2 (staging/prod).

Carved out of unit coverage.

aiobotocore is lazy-imported inside ``send`` so the dev/stub (noop/smtp) path never loads
it.
"""

from __future__ import annotations

import importlib
from typing import TYPE_CHECKING, Protocol, cast, final

if TYPE_CHECKING:
    from collections.abc import Callable
    from contextlib import AbstractAsyncContextManager


class _SesClient(Protocol):
    """The ``sesv2`` call this provider makes."""

    async def send_email(self, **kwargs: object) -> object:
        """Send one email.

        Args:
            **kwargs: The SES ``SendEmail`` request fields.

        Returns:
            The SES response.

        """
        ...


class _SesSession(Protocol):
    """The aiobotocore session, as far as this provider uses it."""

    def create_client(
        self, service: str, *, region_name: str
    ) -> AbstractAsyncContextManager[_SesClient]:
        """Open a client for ``service`` in ``region_name``.

        Args:
            service: The AWS service name.
            region_name: The AWS region.

        Returns:
            A context manager that yields the client.

        """
        ...


@final
class SesEmailSender:
    """An EmailSender over AWS SESv2 (aiobotocore); implements the EmailSender port."""

    def __init__(self, *, region: str, from_address: str) -> None:
        """Wire the SES region and the verified From address.

        Args:
            region: The SES region.
            from_address: The verified sender address.

        """
        self._region = region
        self._from = from_address

    async def send(self, *, to: str, subject: str, html_body: str) -> None:
        """Send an HTML email via SESv2 ``send_email``.

        (regional endpoint + IRSA credentials).

        Args:
            to: The recipient's address.
            subject: The subject line.
            html_body: The message body, as HTML.

        """
        get_session = cast(
            "Callable[[], _SesSession]",
            importlib.import_module("aiobotocore.session").get_session,
        )
        session = get_session()
        async with session.create_client("sesv2", region_name=self._region) as client:
            _ = await client.send_email(
                FromEmailAddress=self._from,
                Destination={"ToAddresses": [to]},
                Content={
                    "Simple": {
                        "Subject": {"Data": subject, "Charset": "UTF-8"},
                        "Body": {"Html": {"Data": html_body, "Charset": "UTF-8"}},
                    },
                },
            )
