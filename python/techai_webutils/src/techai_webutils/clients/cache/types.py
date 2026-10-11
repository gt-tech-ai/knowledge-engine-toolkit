"""Shared cache types.

Tier-root types shared by the cache builder and every backend, kept here (not in a
backend subpackage) so the generic builder can reference them without importing a
concrete backend — the same reason it avoids a circular import between builder and
the redis backend.
"""

from enum import StrEnum


class FailureMode(StrEnum):
    """How the cache decorator treats a backend failure (a generic cache-resilience policy).

    Applied by the tier-root cache builder to whichever backend it wraps (redis today,
    any future backend the same way), not a redis-specific concept.

    BYPASS: Connection errors return a cache miss (None), not exceptions — the caller
        proceeds as if uncached, so a cache outage degrades gracefully.
    ERROR: Connection errors propagate as exceptions — the caller decides how to handle
        an unavailable cache.
    """

    BYPASS = "bypass"
    """A backend failure returns a cache miss (None), degrading gracefully to uncached reads."""
    ERROR = "error"
    """A backend failure propagates as an exception for the caller to handle."""
