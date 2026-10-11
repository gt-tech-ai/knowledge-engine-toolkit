"""Unit tests for the Python PII redactor (a port of the Go ``RedactPII``) and its logger wiring."""

import io
import json
import logging
from collections.abc import Iterator
from pathlib import Path

import pytest
import structlog
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.foundation.logger import configure_logging, get_logger
from techai_webutils.foundation.logger.redact import REDACTED, redact_pii

_VECTORS = Path(__file__).resolve().parents[4] / "testdata" / "redact_vectors.json"


@pytest.fixture
def restore_logging() -> Iterator[None]:
    """Restore the global structlog configuration and root logging handlers after the test.

    ``configure_logging`` reconfigures both process-wide; without the restore the test's
    ``StringIO`` stream would stay installed for every later test.
    """
    saved = structlog.get_config()
    root = logging.getLogger()
    handlers, level = root.handlers[:], root.level
    yield
    structlog.configure(**saved)
    root.handlers[:] = handlers
    root.setLevel(level)


def test_redact_pii_matches_go_vectors() -> None:
    """Test that ``redact_pii`` produces Go's exact output for every shared vector.

    **Why this test is important:**
      - Go and Python both emit logs and span events from the same requests; a pattern that redacts
        in one language and leaks in the other is a compliance gap no single-language test sees.

    **What it tests:**
      - for each case in ``testdata/redact_vectors.json`` (also read by the Go suite),
        ``redact_pii(input) == want``
    """
    vectors = json.loads(_VECTORS.read_text(encoding="utf-8"))

    assert vectors
    for vector in vectors:
        assert redact_pii(vector["input"]) == vector["want"], vector["name"]


@pytest.mark.usefixtures("restore_logging")
def test_logger_applies_redaction_when_flag_set() -> None:
    """Test that ``configure_logging(redact_pii=True)`` redacts the message and string fields.

    **Why this test is important:**
      - ``logging_redact_pii`` is the setting operators rely on to keep PII out of the log store;
        it must actually change the emitted line.

    **What it tests:**
      - with the flag set, the message and a string field are redacted while ``trace``-style
        structural fields and non-string values are untouched
      - with the flag unset, the same line is emitted verbatim
    """
    redacted_stream = io.StringIO()
    configure_logging(level="INFO", stream=redacted_stream, redact_pii=True)
    get_logger("t").info("mail john@example.com", caller="555-123-4567", attempts=3)

    plain_stream = io.StringIO()
    configure_logging(level="INFO", stream=plain_stream)
    get_logger("t").info("mail john@example.com", caller="555-123-4567", attempts=3)

    redacted = json.loads(redacted_stream.getvalue().strip().splitlines()[-1])
    plain = json.loads(plain_stream.getvalue().strip().splitlines()[-1])
    assert redacted["message"] == "mail [REDACTED]"
    assert redacted["caller"] == "[REDACTED]"
    assert redacted["attempts"] == 3
    assert plain["message"] == "mail john@example.com"
    assert plain["caller"] == "555-123-4567"


@pytest.mark.usefixtures("restore_logging")
def test_logger_redacts_nested_fields() -> None:
    """Test that redaction walks into dict, list and tuple field values.

    **Why this test is important:**
      - Request payloads and chat ``messages`` are logged as nested structures; redacting only
        top-level strings would ship an email or phone number inside them to the log store.

    **What it tests:**
      - a dict field's string values, a list of dicts' string values and a tuple's strings are
        each replaced by ``[REDACTED]`` where they match, with non-matching strings, numbers and
        keys left unchanged
    """
    stream = io.StringIO()
    configure_logging(level="INFO", stream=stream, redact_pii=True)
    get_logger("t").info(
        "llm.request",
        request={"user": "john@example.com", "retries": 2, "note": "ok"},
        messages=[{"role": "user", "content": "call 555-123-4567"}],
        pair=("john@example.com", "plain"),
    )

    line = json.loads(stream.getvalue().strip().splitlines()[-1])
    assert line["request"] == {"user": "[REDACTED]", "retries": 2, "note": "ok"}
    assert line["messages"] == [{"role": "user", "content": "call [REDACTED]"}]
    assert line["pair"] == ["[REDACTED]", "plain"]


_LOCAL_PART = st.text(
    alphabet="abcdefghijklmnopqrstuvwxyz0123456789._+-", min_size=1, max_size=12
)
"""E-mail local parts drawn from the characters the pattern accepts."""

_FILLER = st.text(alphabet="abcdefghij ,;!?", max_size=20)
"""Text around an e-mail that holds no PII and cannot extend the e-mail's match."""


@given(_FILLER, _LOCAL_PART, _FILLER)
def test_redact_pii_removes_any_embedded_email_and_is_idempotent(
    before: str, local: str, after: str
) -> None:
    """Test, over generated text, that an embedded e-mail never survives and redaction is idempotent.

    **Why this test is important:**
      - The fixed vectors cover known shapes only; a generated local part or surrounding text that
        defeats the pattern would leak an address into logs and span events.
      - Redaction runs at several layers (logger, span capture); applying it twice must not alter
        an already redacted line.

    **What it tests:**
      - for ``<before> <local>@example.com <after>``, the address is absent from the output and
        ``[REDACTED]`` is present
      - ``redact_pii(redact_pii(x)) == redact_pii(x)``
    """
    address = f"{local}@example.com"
    text = f"{before} {address} {after}"

    once = redact_pii(text)

    assert address not in once
    assert REDACTED in once
    assert redact_pii(once) == once
