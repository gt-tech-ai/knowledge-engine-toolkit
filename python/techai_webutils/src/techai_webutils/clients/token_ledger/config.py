"""The token ledger's kinds and configuration, shared by the factory and its backends.

A leaf module: ``builder`` selects a backend from ``TokenLedgerConfig`` and each backend
reads it, so the config lives where neither imports the other.
"""

from dataclasses import dataclass
from enum import StrEnum

from techai_webutils.core.interfaces.token_ledger import Period


class TokenLedgerKind(StrEnum):
    """The built-in token ledger backends."""

    STUB = "stub"
    """Allow-all, record-nothing (dev/test; zero infrastructure)."""
    REDIS = "redis"
    """Redis counters + a bounded usage stream (the hot path)."""


@dataclass(frozen=True, slots=True)
class TokenLedgerConfig:
    """Token ledger configuration."""

    kind: str = TokenLedgerKind.STUB
    """A ``TokenLedgerKind`` value or a key of the injected ``backends``."""
    window: Period = Period.MONTHLY
    """The budget window the hot-path counters cover."""
    redis_prefix: str = "token_ledger"
    """Key prefix of the Redis counters (``<prefix>:{org}:{workspace}:{window}``)."""
    stream: str = "token_ledger:usage"
    """The Redis stream each recorded usage is appended to."""
    stream_maxlen: int = 1_000_000
    """Approximate cap on the usage stream length (``XADD MAXLEN ~``)."""
