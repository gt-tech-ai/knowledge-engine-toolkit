"""Embedding provider interface for text-to-vector conversion."""

from abc import ABC, abstractmethod
from dataclasses import dataclass

from techai_webutils.core.interfaces.lifecycle import ManagedResource


@dataclass(slots=True)
class EmbeddingResult:
    """Result of an embedding operation."""

    embedding: list[float]
    """The dense vector representation of the input text."""
    model: str
    """Identifier of the model that produced the embedding."""
    token_count: int
    """Number of tokens the input consumed (for cost/usage accounting)."""


class EmbeddingProvider(ManagedResource, ABC):
    """Abstract embedding provider for converting text to vector embeddings."""

    @abstractmethod
    async def embed(self, text: str) -> EmbeddingResult:
        """Generate an embedding for a single text input."""

    @abstractmethod
    async def embed_batch(self, texts: list[str]) -> list[EmbeddingResult]:
        """Generate embeddings for multiple text inputs."""

    @abstractmethod
    def dimension(self) -> int:
        """Return the embedding dimension for this provider's model."""

    @abstractmethod
    def model_name(self) -> str:
        """Return the model identifier used by this provider."""
