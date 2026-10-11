"""SMTP email backend — sends via aiosmtplib (the dev Mailpit path). Carved out of unit coverage."""

from __future__ import annotations

from email.message import EmailMessage

import aiosmtplib


class SmtpEmailSender:
    """An EmailSender over SMTP (Mailpit in dev); implements the EmailSender port."""

    def __init__(self, *, host: str, port: int, from_address: str) -> None:
        """Wire the SMTP host/port and the From address."""
        self._host = host
        self._port = port
        self._from = from_address

    async def send(self, *, to: str, subject: str, html_body: str) -> None:
        """Send a multipart/alternative email (plain-text fallback + HTML) via SMTP."""
        message = EmailMessage()
        message["From"] = self._from
        message["To"] = to
        message["Subject"] = subject
        message.set_content("This notification requires an HTML-capable email client.")
        message.add_alternative(html_body, subtype="html")
        await aiosmtplib.send(message, hostname=self._host, port=self._port)
