"""SES email backend — sends via aiobotocore SESv2 (staging/prod). Carved out of unit coverage.

aiobotocore is lazy-imported inside ``send`` so the dev/stub (noop/smtp) path never loads it.
"""

from __future__ import annotations


class SesEmailSender:
    """An EmailSender over AWS SESv2 (aiobotocore); implements the EmailSender port."""

    def __init__(self, *, region: str, from_address: str) -> None:
        """Wire the SES region and the verified From address."""
        self._region = region
        self._from = from_address

    async def send(self, *, to: str, subject: str, html_body: str) -> None:
        """Send an HTML email via SESv2 ``send_email`` (regional endpoint + IRSA credentials)."""
        import aiobotocore.session  # noqa: PLC0415 — lazy so the dev/stub path never loads aiobotocore

        session = aiobotocore.session.get_session()
        async with session.create_client("sesv2", region_name=self._region) as raw_client:
            client: object = raw_client
            await client.send_email(  # type: ignore[attr-defined]  # ty: ignore[unresolved-attribute]
                FromEmailAddress=self._from,
                Destination={"ToAddresses": [to]},
                Content={
                    "Simple": {
                        "Subject": {"Data": subject, "Charset": "UTF-8"},
                        "Body": {"Html": {"Data": html_body, "Charset": "UTF-8"}},
                    },
                },
            )
