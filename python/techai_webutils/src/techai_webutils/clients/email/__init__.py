"""Config-selected email transport backends (SES / SMTP / noop) + the ``new_email_from_config`` factory.

The client-package shape: ``builder.py`` at the top selects a backend from ``kind``; each
backend lives in its own subpackage behind the ``core.interfaces.email.EmailSender`` port.
"""

from techai_webutils.clients.email.builder import (
    EmailConfig,
    EmailKind,
    new_email_from_config,
)

__all__ = ["EmailConfig", "EmailKind", "new_email_from_config"]
