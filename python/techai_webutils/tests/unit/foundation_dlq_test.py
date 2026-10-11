"""Unit tests for the DeadLetterQueue facade over a pluggable backend."""

from unittest.mock import create_autospec

import pytest

from techai_webutils.core.interfaces.dlq import DeadLetter, DeadLetterBackend
from techai_webutils.foundation.resilience.dlq import (
    DeadLetterQueue,
    StubDeadLetterBackend,
)


class TestDeadLetterQueue:
    """Tests for the dead letter queue."""

    @pytest.mark.asyncio
    async def test_send_delivers_to_backend(self) -> None:
        """Test that a letter is handed to the backend and reported delivered.

        **Why this test is important:**
          - The DLQ is the terminal sink for poison messages; if send silently no-op'd, permanent
            failures would vanish with no record and no alert.

        **What it tests:**
          - send returns True and the stub backend recorded exactly the letter.
        """
        backend = StubDeadLetterBackend()
        dlq = DeadLetterQueue(backend)
        letter = DeadLetter(id="m1", payload=b"x", reason="unsupported_format")
        delivered = await dlq.send(letter)
        assert delivered is True
        assert backend.letters == [letter]

    @pytest.mark.asyncio
    async def test_send_swallows_backend_error_and_returns_false(self) -> None:
        """Test that a backend failure is swallowed+logged and reported as not delivered.

        **Why this test is important:**
          - A failing DLQ must not crash the consumer loop; and the caller must learn delivery
            failed (return False) so it can leave the source message for SQS redrive rather than
            deleting it — otherwise a DLQ outage becomes silent data loss.

        **What it tests:**
          - A raising backend causes send to return False (not raise).
        """
        backend = create_autospec(DeadLetterBackend, instance=True)
        backend.send.side_effect = RuntimeError("dlq down")

        dlq = DeadLetterQueue(backend)
        delivered = await dlq.send(DeadLetter(id="m1", payload=b"x", reason="x"))
        assert delivered is False
