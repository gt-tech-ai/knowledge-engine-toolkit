"""The ``messaging`` fact publisher: a bounded.

Batching sender over a ``MessagePublisher``.
"""

from techai_webutils.clients.facts.messaging.publisher import MessagingFactPublisher

__all__ = ["MessagingFactPublisher"]
