"""EmailSender — the email transport contract (SES / SMTP / noop backends implement it).

A generic, service-agnostic port: send one already-rendered email. The config-selected backends live in
``techai_webutils.clients.email`` (the client-package shape); consumers depend only on this
protocol and the ``new_email_from_config`` factory.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable


@runtime_checkable
class EmailSender(Protocol):
    """Sends one rendered email to a recipient."""

    async def send(self, *, to: str, subject: str, html_body: str) -> None:
        """Send an HTML email to ``to`` with ``subject``; raise a classifiable AppError on failure."""
        ...
