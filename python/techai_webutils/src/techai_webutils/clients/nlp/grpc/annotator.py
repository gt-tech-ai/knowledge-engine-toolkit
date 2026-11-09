"""``GrpcAnnotator``: annotations from a remote NLP service through an injected client.

The consumer adapts its generated stub (e.g. vertiva_nlp's ``AnnotateBatch``) to
``AnnotateClient``. A gRPC failure becomes a coded error: ``UNAVAILABLE`` and
``DEADLINE_EXCEEDED`` are transient (``foundation.resilience.grpc_boundary``), so the
client tier's retry and the degrading decorator act on them. The service may run consumer
tasks, so any registered task is accepted; results are checked against the registry.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from grpc.aio import AioRpcError

from techai_webutils.core.interfaces.annotator import Annotator, check_annotations
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.resilience.grpc_boundary import grpc_error_to_app_error

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.annotator import (
        AnnotateClient,
        Annotation,
        TaskRegistry,
        TextBlock,
    )


class GrpcAnnotator(NoOpAsyncResource, Annotator):
    """Annotates through a remote service's ``AnnotateBatch``."""

    def __init__(self, client: AnnotateClient, *, registry: TaskRegistry) -> None:
        """Call ``client`` (the composition root owns its channel).

        Check tasks with ``registry``.
        """
        self._client = client
        self._registry = registry

    @override
    async def annotate_batch(
        self, blocks: Sequence[TextBlock], tasks: frozenset[str], profile: str
    ) -> list[Annotation]:
        """Return the service's annotations, checked against the blocks and the registry.

        Returns:
            The service's annotations, checked against the blocks and the registry.

        A callee raises ``AppError``: ``UNAVAILABLE`` / ``TIMEOUT`` (transient) or another
        code from the gRPC status; ``INVALID_INPUT`` for an unregistered task;
        ``INTERNAL`` for a reply that does not match the blocks.

        Raises:
            grpc_error_to_app_error: The coded error for a failed RPC.

        """
        self._registry.require(tasks)
        try:
            annotations = await self._client.annotate_batch(blocks, tasks, profile)
        except AioRpcError as exc:
            raise grpc_error_to_app_error(exc) from exc
        return check_annotations(blocks, annotations, self._registry)
