"""Retrieval pipeline interfaces."""

from abc import ABC, abstractmethod
from collections.abc import AsyncIterator, Mapping, Sequence
from dataclasses import dataclass, field
from typing import Protocol

from techai_webutils.core.interfaces.lifecycle import ManagedResource


@dataclass(slots=True)
class RetrievalResult:
    """A single retrieval result with citation information."""

    document_id: str
    """Identifier of the source document this passage was retrieved from."""
    document_name: str
    """Human-readable source document name, for citation display."""
    chunk_content: str
    """The retrieved passage text passed to the answer generator as context."""
    score: float
    """Relevance score of this passage against the query (higher is more relevant)."""
    page_number: int | None
    """Source page the passage appears on (``None`` for non-paginated content)."""
    metadata: dict[str, str]
    """Metadata carried with the chunk (the backend's and the consumer's own keys)."""


@dataclass(slots=True)
class Citation:
    """A citation referencing a source document."""

    document_id: str
    """Identifier of the cited source document."""
    document_name: str
    """Human-readable name of the cited document, for display."""
    chunk: str
    """The specific passage text the citation points to."""
    page_number: int | None
    """Source page the cited passage appears on (``None`` when not paginated/unknown)."""
    confidence: float
    """The extractor's confidence that this citation supports the answer (higher is stronger)."""
    attributes: dict[str, str] = field(default_factory=dict)
    """Consumer-chosen passage metadata carried with the citation (see ``PassageCitationExtractor``)."""


@dataclass
class QueryIntent:
    """Classified intent of a user query."""

    intent_type: str  # "factual", "analytical", "comparative", "procedural"
    """The classified intent category: ``factual``, ``analytical``, ``comparative``, or ``procedural``."""
    confidence: float
    """The classifier's confidence in the assigned ``intent_type`` (higher is more certain)."""
    entities: list[str]
    """Named entities the classifier extracted from the query."""
    keywords: list[str]
    """Salient keywords extracted from the query, used to steer retrieval."""


@dataclass(slots=True)
class HistoryTurn:
    """A prior conversation turn, role-tagged for query rewriting + answer generation.

    ``role`` is ``"user"`` or ``"assistant"``; system turns are never carried as history
    (the prompt strategies inject them separately).
    """

    role: str
    """The turn's speaker: ``"user"`` or ``"assistant"`` (system turns are never carried as history)."""
    content: str
    """The turn's message text."""


class RetrievalEngine(ManagedResource, ABC):
    """Abstract retrieval engine for searching documents."""

    @abstractmethod
    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        index_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve relevant document chunks for a query.

        ``filters`` is the caller's request scope (e.g. a tenant or the caller's access level); a backend
        may push some of it down, and ``FilteringRetrievalEngine`` policies re-validate against it.
        ``index_id`` selects the index to query per call (e.g. a knowledge base per tenant); ``None``
        uses the engine's construction-time index. An engine constructed with no default index and
        called with ``None`` must fail loudly, not silently fall back.
        """


class PassagePolicy(Protocol):
    """A rule every retrieved passage must pass to reach the caller (``FilteringRetrievalEngine``).

    Consumers implement it for their own scope rules; ``clients/retrieval/filtering`` ships
    ``MinScore``, ``MetadataEquals`` and ``OrdinalCeiling``.
    """

    def admits(self, passage: RetrievalResult, request: Mapping[str, str]) -> bool:
        """Return True iff ``passage`` may be returned for ``request`` (the call's filters)."""
        ...


class CitationExtractor(ABC):
    """Abstract citation extractor for identifying source references (chunk-level, with document metadata)."""

    @abstractmethod
    async def extract(
        self, answer: str, sources: list[RetrievalResult]
    ) -> list[Citation]:
        """Extract citations from a generated answer against source documents."""


class QueryRewriter(ABC):
    """Abstract query rewriter for improving retrieval quality (e.g. expansion or LLM rewriting)."""

    @abstractmethod
    async def rewrite(
        self, query: str, history: Sequence[HistoryTurn] | None = None
    ) -> list[str]:
        """Rewrite a query into one or more optimized variants, given prior conversation turns."""


class IntentClassifier(ABC):
    """Abstract intent classifier for understanding query purpose."""

    @abstractmethod
    async def classify(self, query: str) -> QueryIntent:
        """Classify the intent of a user query."""


class AnswerGenerator(ABC):
    """Abstract answer generator: retrieved passages + query -> a cited answer (RAG generate step).

    Generation is single-pass with inline citation markers, streaming or unary.
    """

    @abstractmethod
    def stream(
        self,
        query: str,
        passages: list[RetrievalResult],
        history: Sequence[HistoryTurn] | None = None,
    ) -> AsyncIterator[str]:
        """Stream the generated answer token-by-token (an async generator; iterate, don't await).

        ``history`` is supplied to the prompt as non-citable context; passages remain the only
        citable source.
        """

    @abstractmethod
    async def complete(
        self,
        query: str,
        passages: list[RetrievalResult],
        history: Sequence[HistoryTurn] | None = None,
    ) -> str:
        """Generate the full (non-streaming) answer from the query, retrieved passages, and history.

        ``history`` is non-citable context; passages remain the only citable source.
        """
