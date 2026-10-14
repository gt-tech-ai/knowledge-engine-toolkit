"""Go-style durations in config (mirrors Go ``time.ParseDuration``).

The layered YAML is shared by the Go and Python services, so a duration is written the Go way
(``"30s"``, ``"1m30s"``, ``"250ms"``); ``parse_duration`` reads it as seconds.
"""

from __future__ import annotations

import re

from techai_webutils.core.errors import AppValueError

_UNITS: dict[str, float] = {
    "ns": 1e-9,
    "us": 1e-6,
    "\u00b5s": 1e-6,  # micro sign
    "\u03bcs": 1e-6,  # Greek mu
    "ms": 1e-3,
    "s": 1.0,
    "m": 60.0,
    "h": 3600.0,
}
"""Seconds per Go duration unit."""

_PART = re.compile(
    r"(\d+(?:\.\d*)?|\.\d+)(" + "|".join(sorted(_UNITS, key=len, reverse=True)) + ")"
)
"""One ``<number><unit>`` term of a duration."""


def parse_duration(value: object) -> float:
    """Return ``value`` in seconds: a Go duration string, or a number of seconds.

    A string is a signed sequence of ``<number><unit>`` terms (``"1h30m"``, ``"-1.5s"``), or
    ``"0"``. A malformed string, or a value of any other type, raises ``AppValueError``.
    """
    if isinstance(value, bool):
        msg = f"invalid duration {value!r}"
        raise AppValueError(msg)
    if isinstance(value, int | float):
        return float(value)
    if not isinstance(value, str):
        msg = f"invalid duration {value!r}"
        raise AppValueError(msg)
    text = value.strip()
    sign = -1.0 if text.startswith("-") else 1.0
    body = text.lstrip("+-")
    if body == "0":
        return 0.0
    parts = _PART.findall(body)
    if not body or "".join(n + u for n, u in parts) != body:
        msg = f"invalid duration {value!r}"
        raise AppValueError(msg)
    return sign * sum(float(n) * _UNITS[u] for n, u in parts)
