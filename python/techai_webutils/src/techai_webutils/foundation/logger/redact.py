"""PII redaction for log lines and span events — a port of the Go ``logger.RedactPII``.

One alternation regexp, applied in a single pass, replaces any e-mail, phone number, SSN, IPv4
address or auth token (``bearer <t>``, ``token=<t>``, ``access_token=<t>``) with ``[REDACTED]``.
The alternatives keep Go's order and Python's ``re`` is leftmost-first like RE2, so both languages
redact identically; the shared ``testdata/redact_vectors.json`` pins that in both test suites.
"""

from __future__ import annotations

import re

_PII_PATTERN = re.compile(
    r"[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}"  # email
    r"|(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}"  # phone
    r"|\b\d{3}-\d{2}-\d{4}\b"  # SSN
    r"|\b(?:\d{1,3}\.){3}\d{1,3}\b"  # IPv4
    r"|(?i:(?:bearer\s+|(?:access_)?token[=:]\s*)[a-zA-Z0-9\-._~+/]+=*)",  # auth token (incl. access_token=)
    re.ASCII,
)
"""The Go ``piiPattern`` alternation; ``re.ASCII`` keeps ``\\d``/``\\s``/``\\b`` to RE2's ASCII meaning."""

REDACTED = "[REDACTED]"
"""The replacement for every PII match."""


def redact_pii(text: str) -> str:
    """Replace PII (email, phone, SSN, IPv4, auth tokens) in ``text`` with ``[REDACTED]`` in one pass."""
    return _PII_PATTERN.sub(REDACTED, text)
