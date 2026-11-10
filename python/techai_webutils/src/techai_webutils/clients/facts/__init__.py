"""Analytics fact publishing: a config-selected ``FactPublisher``.

The kinds are ``stub`` and ``messaging``.
"""

from techai_webutils.clients.facts.builder import (
    FactPublisherConfig,
    FactPublisherKind,
    new_fact_publisher_from_config,
)

__all__ = ["FactPublisherConfig", "FactPublisherKind", "new_fact_publisher_from_config"]
