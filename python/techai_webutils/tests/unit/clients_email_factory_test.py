"""Tests for the config-selected email backend factory (noop <-> smtp <-> ses)."""

import pytest

# Imported eagerly (the factory lazy-imports ses/smtp) so the selection tests can isinstance-check the
# concrete types; the test venv has aiobotocore + aiosmtplib, so these loads are harmless here.
from techai_webutils.clients.email.builder import (
    EmailConfig,
    EmailKind,
    new_email_from_config,
)
from techai_webutils.clients.email.noop import NoopEmailSender
from techai_webutils.clients.email.ses import SesEmailSender
from techai_webutils.clients.email.smtp import SmtpEmailSender
from techai_webutils.core.interfaces.email import EmailSender


def _sender(kind: EmailKind) -> EmailSender:
    """Build a sender for ``kind`` with fixed, backend-agnostic config."""
    return new_email_from_config(
        EmailConfig(
            kind=kind,
            from_address="notify@example.com",
            region="us-east-1",
            smtp_host="mailpit",
            smtp_port=1025,
        )
    )


class TestEmailFactory:
    """Tests for the email factory."""

    def test_noop_kind_selects_noop_backend(self) -> None:
        """Test that an unrecognised/noop kind builds the logging noop sender (the dev default).

        **Why this test is important:**
          - Dev must run the email channel without a real SES/SMTP endpoint; the noop default is the
            seam that lets local dispatch complete, mirroring the kb factory's stub default.

        **What it tests:**
          - new_email_from_config("noop") returns a NoopEmailSender that satisfies the EmailSender port.
        """
        sender = _sender(EmailKind.NOOP)
        assert isinstance(sender, NoopEmailSender)
        assert isinstance(sender, EmailSender)

    def test_default_kind_is_noop(self) -> None:
        """Test that an ``EmailConfig`` with no explicit kind defaults to the noop sender.

        **Why this test is important:**
          - "Defaulting to noop" is a stub-first guarantee (ARCHITECTURE.md#stub-first-backends): a service that never sets
            an email kind must build and dispatch with no SES/SMTP endpoint, not fail or send for real.
            The other tests pass an explicit kind; only this one locks the DEFAULT.

        **What it tests:**
          - ``new_email_from_config(EmailConfig())`` — kind unspecified — returns a NoopEmailSender.
        """
        assert isinstance(new_email_from_config(EmailConfig()), NoopEmailSender)

    def test_smtp_kind_selects_smtp_backend(self) -> None:
        """Test that kind=smtp builds the SMTP sender (the dev Mailpit path).

        **Why this test is important:**
          - Dev-with-Mailpit and any SMTP relay flip on by config alone; a wrong selection would send
            nowhere or crash.

        **What it tests:**
          - new_email_from_config("smtp") returns a SmtpEmailSender.
        """
        assert isinstance(_sender(EmailKind.SMTP), SmtpEmailSender)

    def test_ses_kind_selects_ses_backend(self) -> None:
        """Test that kind=ses builds the real SES sender (staging/prod).

        **Why this test is important:**
          - Staging/prod must use SES; the same config seam flips to the real transport with no code
            change.

        **What it tests:**
          - new_email_from_config("ses") returns a SesEmailSender.
        """
        assert isinstance(_sender(EmailKind.SES), SesEmailSender)

    def test_unknown_kind_raises(self) -> None:
        """Test that ``new_email_from_config`` fails loudly on an unrecognised kind.

        **Why this test is important:**
          - A config typo like ``kind: sess`` (or a newly-added EmailKind the factory forgets to
            handle) must surface as a startup error, not silently degrade to the no-op sender that
            suppresses every email — the Go ``NewFromConfig`` "unknown kinds fail loudly" contract
            (ARCHITECTURE.md#swappable-components), and the same defensive ``raise`` the sibling ``new_lock_from_config`` has.

        **What it tests:**
          - ``new_email_from_config`` with a kind outside the handled set raises ValueError.
        """
        # Deliberately bypass the EmailKind enum with a bogus value to exercise the defensive raise.
        with pytest.raises(ValueError, match="unknown email kind"):
            new_email_from_config(EmailConfig(kind="bogus"))  # type: ignore[arg-type]

    @pytest.mark.asyncio
    async def test_noop_send_suppresses_without_error(self) -> None:
        """Test that the noop backend's send completes silently (the dev default sends nowhere).

        **Why this test is important:**
          - The noop sender is the default dev path; its send must be a safe no-op so local dispatch
            completes without an SMTP/SES endpoint.

        **What it tests:**
          - NoopEmailSender.send returns without raising.
        """
        await _sender(EmailKind.NOOP).send(
            to="user@example.com", subject="hi", html_body="<p>hi</p>"
        )
