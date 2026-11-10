"""The ``messaging`` fact publisher over a ``MessagePublisher``.

It sends in bounded batches.
"""

from techai_webutils.clients.facts.messaging.publisher import MessagingFactPublisher

__all__ = ["MessagingFactPublisher"]
