"""Histogram bucket boundaries shared by every tier and transport.

Mirrors the Go ``core/types.DefaultDurationBuckets`` so both languages record a
duration under the same ``le`` labels.
"""

from __future__ import annotations

DEFAULT_DURATION_BUCKETS: tuple[float, ...] = (
    0.005,
    0.01,
    0.025,
    0.05,
    0.1,
    0.25,
    0.5,
    1,
    2.5,
    5,
    10,
    30,
    60,
    120,
    300,
)
"""Operation-duration histogram upper bounds, in seconds: 5 ms to 5 min, so slow
operations (document parses, model calls, index rounds) resolve above 10 s instead of
all landing in +Inf."""
