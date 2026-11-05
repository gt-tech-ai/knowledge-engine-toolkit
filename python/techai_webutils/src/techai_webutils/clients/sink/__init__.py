"""Sinks (``SinkKind``: ``noop``, ``opensearch``) and their factory."""

from techai_webutils.clients.sink.builder import SinkConfig, SinkKind, sink_from_config

__all__ = ["SinkConfig", "SinkKind", "sink_from_config"]
