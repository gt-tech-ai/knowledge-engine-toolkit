"""SES email backend (staging/prod)."""

from techai_webutils.clients.email.ses.provider import SesEmailSender

__all__ = ["SesEmailSender"]
