"""``Fact`` — one analytics fact on the ``analytics.fact`` wire (JSON).

A fact is one observation of a cube: who (``org_id`` + ``dims``), when (``ts``), how much
(``measures``) and an ``idempotency_key`` the consumer upserts on, so a redelivered fact is a no-op.
``to_json`` is byte-identical to the Go ``types.Fact`` encoding; both suites assert the shared
``testdata/analytics_fact.golden.json``.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping

FACT_SCHEMA_VERSION = 1
"""Version of the fact wire shape; a consumer rejects a version it does not know."""

_GO_ESCAPES = (
    ("<", "\\u003c"),
    (">", "\\u003e"),
    ("&", "\\u0026"),
    ("\u2028", "\\u2028"),
    ("\u2029", "\\u2029"),
)
"""Characters Go's ``encoding/json`` escapes by default, with the escape it writes."""

_MAX_INTEGRAL_FLOAT = 1e21
"""Below this, an integral float renders as an integer — where Go's ``encoding/json`` switches to exponent form."""


def _rfc3339_nano(ts: datetime) -> str:
    """Render ``ts`` in UTC as Go's ``time.RFC3339Nano``: fractional zeros trimmed, ``Z`` suffix."""
    if ts.tzinfo is None:
        msg = "fact ts must be timezone-aware"
        raise AppValueError(msg)
    utc = ts.astimezone(UTC)
    fraction = f"{utc.microsecond:06d}".rstrip("0")
    return utc.strftime("%Y-%m-%dT%H:%M:%S") + (f".{fraction}" if fraction else "") + "Z"


def _number(value: float) -> float | int:
    """Render a measure as a JSON number the Go ``float64`` decoder accepts.

    An integral float renders as an int (``42`` not ``42.0``, matching Go's encoding), and a ``bool``
    (which Python accepts wherever a ``float`` is typed) renders as ``1`` / ``0``, never ``true``.
    """
    if isinstance(value, bool):
        return int(value)
    if (
        isinstance(value, float)
        and value.is_integer()
        and abs(value) < _MAX_INTEGRAL_FLOAT
    ):
        return int(value)
    return value


@dataclass(frozen=True, slots=True)
class Fact:
    """One analytics fact: an observation of ``cube`` for ``org_id`` at ``ts``."""

    cube: str
    """The cube (fact table) the fact belongs to, e.g. ``genai_calls``."""
    org_id: str
    """The organization the fact is partitioned under."""
    ts: datetime
    """When the observation happened (timezone-aware; serialized in UTC)."""
    dims: Mapping[str, str]
    """Dimension values the fact is grouped by (``model``, ``step``, ``team``, …)."""
    measures: Mapping[str, float]
    """Numeric measures the fact contributes (``tokens_in``, ``duration_s``, …); a ``bool`` is sent as 1/0."""
    idempotency_key: str
    """Unique per observation; a redelivered fact with the same key overwrites, never double-counts."""
    schema: int = field(default=FACT_SCHEMA_VERSION)
    """Wire-shape version (``FACT_SCHEMA_VERSION``)."""

    def to_json(self) -> bytes:
        """Serialize to the compact, key-sorted UTF-8 JSON the Go consumer decodes."""
        body = {
            "cube": self.cube,
            "dims": dict(self.dims),
            "idempotency_key": self.idempotency_key,
            "measures": {k: _number(v) for k, v in self.measures.items()},
            "org_id": self.org_id,
            "schema": self.schema,
            "ts": _rfc3339_nano(self.ts),
        }
        text = json.dumps(body, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        # Match Go's default HTML-safe escaping; these characters can only occur inside JSON strings.
        for char, escape in _GO_ESCAPES:
            text = text.replace(char, escape)
        return text.encode("utf-8")
