"""``reranker_from_config``: the config-selected ``Reranker``."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.reranking.cross_encoder.reranker import (
    CrossEncoderReranker,
)
from techai_webutils.clients.reranking.noop.reranker import NoopReranker
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.reranker import RerankClient, Reranker


class RerankerKind(StrEnum):
    """Which reranker to build."""

    NOOP = "noop"
    """Keeps the retriever's order (the default)."""
    CROSS_ENCODER = "cross_encoder"
    """Scores each passage with a cross-encoder through the injected ``RerankClient``."""


@dataclass(frozen=True, slots=True)
class RerankerConfig:
    """Selects the reranker and its batch size."""

    kind: RerankerKind = RerankerKind.NOOP
    """The reranker to build; ``noop`` by default."""
    batch_size: int = 32
    """Most passages sent to the client per call (``cross_encoder``)."""


def reranker_from_config(
    config: RerankerConfig, *, client: RerankClient | None = None
) -> Reranker:
    """Build the ``Reranker`` ``config.kind`` selects.

    The ``cross_encoder`` kind scores through ``client``, the composition root's transport
    to the model server (e.g. the NLP service's gRPC stub).

    Raises:
        AppValueError: An unknown kind, ``cross_encoder`` without a client, or a batch
            size below one (``INVALID_INPUT``).

    """
    if config.kind == RerankerKind.NOOP:
        return NoopReranker()
    if config.kind == RerankerKind.CROSS_ENCODER:
        if client is None:
            msg = "the cross_encoder reranker needs a rerank client"
            raise AppValueError(msg)
        if config.batch_size < 1:
            msg = f"reranker batch_size must be at least 1, got {config.batch_size}"
            raise AppValueError(msg)
        return CrossEncoderReranker(client, batch_size=config.batch_size)
    msg = f"unknown reranker kind: {config.kind!r}"
    raise AppValueError(msg)
