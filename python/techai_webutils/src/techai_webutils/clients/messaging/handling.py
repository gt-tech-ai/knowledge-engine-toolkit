"""Runs one consumed message's handler the way every subscriber backend does.

The handler continues the trace the publisher put in the message's metadata and runs through the
handler tier (``ClientBase.wire`` with ``handler()``) as the operation named by the topic: observed and bounded,
never retried, since the broker's redelivery is the retry.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.foundation.tracer.propagate import continued

if TYPE_CHECKING:
    from techai_webutils.clients.client import ClientBase
    from techai_webutils.core.interfaces.messaging import Message, MessageHandler


async def run_handler(
    handlers: ClientBase | None, topic: str, handler: MessageHandler, message: Message
) -> None:
    """Run ``handler`` on ``message`` in the publisher's trace, through ``handlers`` when set.

    The handler's exception propagates unchanged, so the backend decides between ack and nack.
    """
    with continued(message.metadata):
        if handlers is None:
            await handler(message)
            return
        await handlers.exec_once(topic, lambda: handler(message))
