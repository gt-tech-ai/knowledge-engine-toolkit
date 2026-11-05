"""Bedrock sink backend: chunk files and ``.metadata.json`` sidecars for a KB data source."""

from techai_webutils.clients.sink.bedrock.config import BedrockSinkConfig
from techai_webutils.clients.sink.bedrock.layout import BedrockLayout
from techai_webutils.clients.sink.bedrock.sink import BedrockSink

__all__ = ["BedrockLayout", "BedrockSink", "BedrockSinkConfig"]
