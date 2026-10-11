"""Config-selected ``AuditSink`` factory (the ``clients/llm/builder.py`` pattern).

``AuditSinkConfig.kind`` selects the built-in ``stub`` (the default) or a consumer-supplied kind
from the injected ``backends`` mapping — the durable sink calls a product service the KE must not
import, so the consumer injects it (e.g. ``{"grpc": …}``). An unknown kind, or an enforcement mode
other than ``shadow`` / ``enforced``, raises a coded ``AppError(INVALID_INPUT)``.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from types import MappingProxyType
from typing import TYPE_CHECKING, Literal

from techai_webutils.clients.audit.stub import StubAuditSink
from techai_webutils.core.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

    from techai_webutils.core.interfaces.audit import AuditSink

_ENFORCEMENT_MODES = ("shadow", "enforced")
"""The accepted ``AuditSinkConfig.enforcement`` values."""


class AuditSinkKind(StrEnum):
    """The built-in audit sink backends."""

    STUB = "stub"
    """In-memory, observable list (dev/test; zero infrastructure)."""


@dataclass(frozen=True, slots=True)
class AuditSinkConfig:
    """Audit sink configuration."""

    kind: str = AuditSinkKind.STUB
    """An ``AuditSinkKind`` value or a key of the injected ``backends``."""
    enforcement: Literal["shadow", "enforced"] = "shadow"
    """``shadow``: a failed append is logged and the answer proceeds; ``enforced``: it fails the call."""
    endpoint: str = ""
    """The durable store's address, for a consumer-supplied backend."""
    timeout_s: float = 2.0
    """Per-call deadline, in seconds, for a consumer-supplied backend."""

    def __post_init__(self) -> None:
        """Reject an enforcement mode other than ``shadow`` / ``enforced`` (``INVALID_INPUT``)."""
        if self.enforcement not in _ENFORCEMENT_MODES:
            raise AppError(
                ErrorCode.INVALID_INPUT,
                f"unknown audit enforcement mode: {self.enforcement!r}",
            )


def new_audit_sink_from_config(
    config: AuditSinkConfig,
    *,
    backends: Mapping[str, Callable[[AuditSinkConfig], AuditSink]] = MappingProxyType({}),
) -> AuditSink:
    """Build the ``AuditSink`` selected by ``config.kind``.

    Args:
        config: The sink configuration.
        backends: Factories for consumer-supplied kinds, keyed by kind; a built-in kind name is
            never looked up here.

    Raises:
        AppError: ``INVALID_INPUT`` for an unknown kind.

    """
    if config.kind == AuditSinkKind.STUB:
        return StubAuditSink()
    factory = backends.get(config.kind)
    if factory is None:
        raise AppError(
            ErrorCode.INVALID_INPUT, f"unknown audit sink kind: {config.kind!r}"
        )
    return factory(config)
