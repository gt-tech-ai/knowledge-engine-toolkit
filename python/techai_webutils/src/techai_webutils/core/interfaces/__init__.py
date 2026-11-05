"""Core interfaces for the knowledge-engine-toolkit Python library.

Defines ABC-based abstract contracts for all Python service dependencies.
Implementations live in the foundation or service layer; these interfaces enable
dependency inversion and testability.

Service Interfaces:
    BaseService: Service lifecycle (startup, shutdown, health check).
    CrudService: Generic CRUD operations with authorization.
    ServiceHooks: Lifecycle hooks for CRUD service operations.
    Pipeline: Stateless data transformation (sync and async variants).
    Workflow: Multi-step business process orchestration (sync and async variants).
    DocumentParser: Parse raw documents into text + metadata chunks.
    ChunkingStrategy: Split documents into retrieval-sized chunks.
    ChunkContextualizer: Situate each chunk in its document before indexing (ContextDocument).
    EmbeddingProvider: Generate vector embeddings from text (as a query or a document).
    KnowledgeBase: Sync documents and query a vector knowledge base.
    LLMProvider: Generate text completions from a language model.
    RetrievalEngine: Orchestrate semantic search with reranking.
    Reranker: Score retrieved passages against the query (RerankClient: its transport).
    FilterExpr: A backend-neutral metadata filter (FilterEq, FilterIn, FilterAnd).
    Annotator: Annotate text blocks with entities and registered tasks (TaskRegistry).
    SentenceSplitter: Split text into sentence spans that tile it (TextSpan), in process.
    LanguageIdentifier: Identify a text's language (LanguageGuess), in process.
    QueryRewriter: Transform user queries for better retrieval.
    IntentClassifier: Classify query intent (search, summarize, compare, etc.).
    CitationExtractor: Extract source citations from LLM responses.
    VectorStore: Low-level vector similarity search operations.

Data Access Interfaces:
    Repository: Generic CRUD data access (business boundary).
    Store: Generic CRUD data access (infrastructure boundary).
    Transaction: Database transaction handle.
    TransactionManager: Transaction lifecycle management.

Foundation Interfaces:
    Cache: Async key-value cache (Redis, in-memory, null).
    CacheInvalidator: Cache invalidation (prefix, all).
    ByteCache: Sync raw-byte cache for infrastructure wrappers.
    Logger: Structured, leveled logging.
    MetricsProvider: Counter, histogram, gauge metric recording.
    TracerProvider: Distributed tracing with spans.
    ConfigLoader: Read-only configuration access.

Resilience Interfaces:
    CircuitBreakerInterface: Fail-fast circuit breaker.
    RateLimiter: Token-bucket rate limiting.
    Bulkhead: Concurrency limiting.
    Retrier: Automatic retry on transient failures.
    Deduplicator: Idempotency for at-least-once delivery.
    LeaderElector: Single-instance execution across replicas.
    ResourceProbe: The CPU and memory limits a process may use (lane sizing).
    Hedger: Tail-latency reduction by racing a backup attempt.
    AdaptiveThrottler: Client-side SRE load-shedding.

Infrastructure Interfaces:
    MessagePublisher: Publish messages to a broker.
    KeyedPublisher: Publish messages under a partition key.
    LeaseExtender: Extend the lease on a message still being handled.
    BacklogProbe: Report the work waiting on a topic.
    CheckpointStore: Store stage artifacts for resumable ingestion.
    StageStateClient: Record which stage finished with which fingerprint.
    InflightClaim: Run a costly stage of one content once across consumers.
    StartBudget: Cap and pace an account's knowledge-base ingestion jobs.
    Sink: Write, update and delete one document's chunks in the search backend.
    MetricsQuerier: Evaluate PromQL against a metrics backend.
    MessageConsumer: Consume messages from a queue.
    StorageClient: Object storage operations.

Job Interfaces:
    Job: Background job definition.
    Worker: Job processor.
    JobEnqueuer: Submit jobs for async processing.
    JobScheduler: Schedule periodic jobs.
    WorkerRegistry: Register workers by kind.
"""

from techai_webutils.core.interfaces.adaptive_throttler import AdaptiveThrottler
from techai_webutils.core.interfaces.annotator import (
    AnnotateClient,
    Annotation,
    Annotator,
    Entity,
    TaskRegistry,
    TaskResult,
    TextBlock,
)
from techai_webutils.core.interfaces.bulkhead import Bulkhead
from techai_webutils.core.interfaces.byte_cache import ByteCache
from techai_webutils.core.interfaces.cache import Cache, CacheInvalidator
from techai_webutils.core.interfaces.checkpoint import (
    CheckpointKey,
    CheckpointStore,
    Stage,
    StageArtifact,
    StageState,
    StageStateClient,
)
from techai_webutils.core.interfaces.chunk_contextualizer import (
    ChunkContextualizer,
    ContextDocument,
)
from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
from techai_webutils.core.interfaces.config_loader import ConfigLoader
from techai_webutils.core.interfaces.crud_service import CrudService, ServiceHooks
from techai_webutils.core.interfaces.dedup import Deduplicator
from techai_webutils.core.interfaces.document import ChunkingStrategy, DocumentParser
from techai_webutils.core.interfaces.embedding import EmbeddingProvider
from techai_webutils.core.interfaces.hedger import Hedger
from techai_webutils.core.interfaces.inflight_claim import InflightClaim, inflight_key
from techai_webutils.core.interfaces.jobs import (
    Job,
    JobEnqueuer,
    JobScheduler,
    PeriodicJob,
    Worker,
    WorkerRegistry,
)
from techai_webutils.core.interfaces.knowledge_base import KnowledgeBase
from techai_webutils.core.interfaces.language_identifier import (
    LanguageGuess,
    LanguageIdentifier,
)
from techai_webutils.core.interfaces.leader import LeaderElector
from techai_webutils.core.interfaces.lifecycle import (
    Client,
    HealthChecker,
    ManagedResource,
)
from techai_webutils.core.interfaces.llm import LLMProvider
from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.core.interfaces.messaging import (
    PARTITION_KEY_ATTRIBUTE,
    KeyedPublisher,
    LeaseExtender,
    Message,
    MessageConsumer,
    MessageHandler,
    MessagePublisher,
)
from techai_webutils.core.interfaces.messaging_backlog import Backlog, BacklogProbe
from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricGauge,
    MetricHistogram,
    MetricsProvider,
)
from techai_webutils.core.interfaces.metrics_query import MetricSample, MetricsQuerier
from techai_webutils.core.interfaces.pipeline import AsyncPipeline, Pipeline
from techai_webutils.core.interfaces.rate_limiter import RateLimiter
from techai_webutils.core.interfaces.repository import (
    Repository,
    Transaction,
    TransactionManager,
)
from techai_webutils.core.interfaces.reranker import RerankClient, Reranker
from techai_webutils.core.interfaces.resource_probe import ResourceLimits, ResourceProbe
from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.core.interfaces.retrieval import (
    CitationExtractor,
    FilterAnd,
    FilterEq,
    FilterExpr,
    FilterIn,
    IntentClassifier,
    PassagePolicy,
    QueryRewriter,
    RetrievalEngine,
)
from techai_webutils.core.interfaces.sentence_splitter import SentenceSplitter, TextSpan
from techai_webutils.core.interfaces.service import BaseService
from techai_webutils.core.interfaces.sink import (
    ChunkPayload,
    Sink,
    SinkDocument,
    SinkStore,
)
from techai_webutils.core.interfaces.start_budget import Slot, StartBudget
from techai_webutils.core.interfaces.storage import StorageClient, StorageObject
from techai_webutils.core.interfaces.store import Store
from techai_webutils.core.interfaces.tracer import TracerProvider, TracerSpan
from techai_webutils.core.interfaces.vector_store import VectorStore
from techai_webutils.core.interfaces.workflow import AsyncWorkflow, Workflow

__all__ = [
    "PARTITION_KEY_ATTRIBUTE",
    "AdaptiveThrottler",
    # Service
    "AnnotateClient",
    "Annotation",
    "Annotator",
    "AsyncPipeline",
    "AsyncWorkflow",
    "Backlog",
    "BacklogProbe",
    "BaseService",
    # Resilience
    "Bulkhead",
    "ByteCache",
    # Foundation
    "Cache",
    "CacheInvalidator",
    "CheckpointKey",
    "CheckpointStore",
    "ChunkContextualizer",
    "ChunkPayload",
    # AI/Retrieval
    "ChunkingStrategy",
    "CircuitBreakerInterface",
    "CitationExtractor",
    "Client",
    "ConfigLoader",
    "ContextDocument",
    "CrudService",
    "Deduplicator",
    "DocumentParser",
    "EmbeddingProvider",
    "Entity",
    "FilterAnd",
    "FilterEq",
    "FilterExpr",
    "FilterIn",
    "HealthChecker",
    "Hedger",
    "InflightClaim",
    "IntentClassifier",
    # Jobs
    "Job",
    "JobEnqueuer",
    "JobScheduler",
    "KeyedPublisher",
    "KnowledgeBase",
    "LLMProvider",
    "LanguageGuess",
    "LanguageIdentifier",
    "LeaderElector",
    "LeaseExtender",
    "Logger",
    "ManagedResource",
    "Message",
    "MessageConsumer",
    "MessageHandler",
    "MessagePublisher",
    "MetricCounter",
    "MetricGauge",
    "MetricHistogram",
    "MetricSample",
    "MetricsProvider",
    "MetricsQuerier",
    "PassagePolicy",
    "PeriodicJob",
    "Pipeline",
    "QueryRewriter",
    "RateLimiter",
    # Data Access
    "Repository",
    "RerankClient",
    "Reranker",
    "ResourceLimits",
    "ResourceProbe",
    "Retrier",
    "RetrievalEngine",
    "SentenceSplitter",
    "ServiceHooks",
    "Sink",
    "SinkDocument",
    "SinkStore",
    "Slot",
    "Stage",
    "StageArtifact",
    "StageState",
    "StageStateClient",
    "StartBudget",
    "StorageClient",
    "StorageObject",
    "Store",
    "TaskRegistry",
    "TaskResult",
    "TextBlock",
    "TextSpan",
    "TracerProvider",
    "TracerSpan",
    "Transaction",
    "TransactionManager",
    "VectorStore",
    "Worker",
    "WorkerRegistry",
    "Workflow",
    "inflight_key",
]
