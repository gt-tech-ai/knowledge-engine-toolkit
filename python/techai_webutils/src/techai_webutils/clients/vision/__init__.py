"""Page-image readers (``stub``, ``bedrock``) and their factory."""

from techai_webutils.clients.vision.builder import vision_from_config
from techai_webutils.clients.vision.config import VisionConfig, VisionKind

__all__ = ["VisionConfig", "VisionKind", "vision_from_config"]
