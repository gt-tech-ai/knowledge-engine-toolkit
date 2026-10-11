"""SQS-backed dead-letter backend (thin aiobotocore wrapper).

Split from ``dlq.py`` so the network/SDK lines are carved out of the unit coverage
denominator (see ``pyproject.toml`` ``[tool.coverage.report].omit``) and covered by the
integration suite against ElasticMQ — mirroring ``clients/messaging/sqs_publisher.py``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Self

import aiobotocore.session

from techai_webutils.core.errors import AppRuntimeError
from techai_webutils.core.interfaces.dlq import DeadLetterBackend

if TYPE_CHECKING:
    from types import TracebackType

    from aiobotocore.session import AioSession
    from types_aiobotocore_sqs import SQSClient
    from types_aiobotocore_sqs.type_defs import MessageAttributeValueTypeDef

    from techai_webutils.clients.messaging.config import SQSConfig
    from techai_webutils.core.interfaces.dlq import DeadLetter


class SqsDeadLetterBackend(DeadLetterBackend):
    """Dead-letter backend that sends letters to a real SQS-compatible queue.

    Usage::

        async with SqsDeadLetterBackend(config) as backend:
            await DeadLetterQueue(backend).send(letter)
    """

    def __init__(self, config: SQSConfig) -> None:
        """Store the SQS DLQ connection config; the client is created on context entry."""
        self._config = config
        self._client: SQSClient | None = None
        self._session: AioSession | None = None

    async def __aenter__(self) -> Self:
        """Open the underlying aiobotocore SQS client and return self."""
        self._session = aiobotocore.session.get_session()
        self._client = await self._session.create_client(
            "sqs",
            **self._config.client_kwargs(),
        ).__aenter__()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Close the underlying aiobotocore SQS client on context exit."""
        if self._client is not None:
            await self._client.__aexit__(exc_type, exc_val, exc_tb)

    async def send(self, letter: DeadLetter) -> None:
        """Send the dead letter to the configured DLQ queue with reason + source metadata.

        The body is decoded with ``errors="replace"`` because a poison message (the exact
        thing being dead-lettered) may not be valid UTF-8 — the DLQ must never fail on the
        malformed messages it exists to capture. User ``metadata`` cannot overwrite the
        reserved ``reason``/``source_id`` attributes (colliding keys are dropped).
        """
        if self._client is None:
            msg = "SqsDeadLetterBackend not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        reserved = {"reason", "source_id"}
        attributes: dict[str, MessageAttributeValueTypeDef] = {
            "reason": {"DataType": "String", "StringValue": letter.reason},
            "source_id": {"DataType": "String", "StringValue": letter.id},
        }
        attributes.update(
            {
                key: {"DataType": "String", "StringValue": value}
                for key, value in letter.metadata.items()
                if key not in reserved
            },
        )
        await self._client.send_message(
            QueueUrl=self._config.queue_url,
            MessageBody=letter.payload.decode("utf-8", errors="replace"),
            MessageAttributes=attributes,
        )
