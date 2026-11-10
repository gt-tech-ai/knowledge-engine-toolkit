"""``new_email_from_config`` — select the email transport backend by ``EmailKind``.

(ses | smtp | noop).

The env-aware factory (client-package shape): the concrete backend is chosen from an
``EmailConfig`` and returned behind the ``core.interfaces.email.EmailSender`` port. The
ses/smtp backends are imported lazily so the default noop path never loads aiobotocore /
aiosmtplib. An unknown kind fails loudly at ``EmailKind`` construction (config load) —
matching the other tier factories and the Go ``NewFromConfig`` contract — rather than
silently degrading to noop.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.email.noop import NoopEmailSender
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.email.ses import SesEmailSender as _LazySesEmailSender
    from techai_webutils.clients.email.smtp import SmtpEmailSender as _LazySmtpEmailSender
    from techai_webutils.core.interfaces.email import EmailSender


class EmailKind(StrEnum):
    """Which email transport backend to build."""

    NOOP = "noop"
    """The logging no-op sender (dev/local; sends nothing)."""
    SES = "ses"
    """The AWS SES transport (stage/prod)."""
    SMTP = "smtp"
    """The SMTP transport (dev Mailpit / any SMTP relay)."""


@dataclass(frozen=True, slots=True)
class EmailConfig:
    """Email transport configuration.

    (the consumer maps its own config section onto it).
    """

    kind: EmailKind = EmailKind.NOOP
    """Selects the transport backend."""
    from_address: str = ""
    """The From: address stamped on every message."""
    region: str = "us-east-1"
    """AWS region for the SES client (unused by smtp/noop)."""
    smtp_host: str = "localhost"
    """SMTP server host (unused by ses/noop)."""
    smtp_port: int = 1025
    """SMTP server port (unused by ses/noop)."""


def new_email_from_config(config: EmailConfig) -> EmailSender:
    """Return the ``EmailSender`` selected by ``config.kind`` (ses/smtp imported lazily).

    Every kind is matched explicitly and an unrecognized kind raises, mirroring the Go
    ``NewFromConfig`` default case (and the sibling ``new_lock_from_config``): a
    newly-added ``EmailKind`` that this factory forgets to handle fails loudly here
    instead of silently degrading to the no-op sender.

    Args:
        config: Selects the backend and carries its settings.

    Returns:
        The ``EmailSender`` selected by ``config.kind`` (ses/smtp imported lazily).

    Raises:
        AppValueError: Unknown email kind: ``config.kind``.

    """
    if config.kind is EmailKind.SES:
        # Lazy import: skip aiobotocore off the SES path.
        ses_email_sender_cls = cast(
            "type[_LazySesEmailSender]",
            importlib.import_module("techai_webutils.clients.email.ses").SesEmailSender,
        )

        return ses_email_sender_cls(
            region=config.region, from_address=config.from_address
        )
    if config.kind is EmailKind.SMTP:
        # Lazy import: skip aiosmtplib off the SMTP path.
        smtp_email_sender_cls = cast(
            "type[_LazySmtpEmailSender]",
            importlib.import_module("techai_webutils.clients.email.smtp").SmtpEmailSender,
        )

        return smtp_email_sender_cls(
            host=config.smtp_host, port=config.smtp_port, from_address=config.from_address
        )
    if config.kind is EmailKind.NOOP:
        return NoopEmailSender()
    msg = f"unknown email kind: {config.kind!r}"
    raise AppValueError(msg)
