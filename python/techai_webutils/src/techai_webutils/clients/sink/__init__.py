"""Sinks (``SinkKind``: ``noop``, ``opensearch``, ``bedrock``) and their factory."""

from techai_webutils.clients.sink.bedrock import BedrockLayout, BedrockSinkConfig
from techai_webutils.clients.sink.builder import SinkConfig, SinkKind, sink_from_config

__all__ = [
    "BedrockLayout",
    "BedrockSinkConfig",
    "SinkConfig",
    "SinkKind",
    "sink_from_config",
]
