"""Sinks (``SinkKind``: ``noop``, ``opensearch``, ``bedrock``), their factory and their client-tier decorator."""

from techai_webutils.clients.sink.bedrock import BedrockLayout, BedrockSinkConfig
from techai_webutils.clients.sink.builder import SinkConfig, SinkKind, sink_from_config
from techai_webutils.clients.sink.decorated import decorate

__all__ = [
    "BedrockLayout",
    "BedrockSinkConfig",
    "SinkConfig",
    "SinkKind",
    "decorate",
    "sink_from_config",
]
