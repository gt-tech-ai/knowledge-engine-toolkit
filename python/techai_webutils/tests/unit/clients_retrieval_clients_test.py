"""Tests for the retrieval engines, the config-selected factory, and the policy filter."""

from collections.abc import Generator
from contextlib import contextmanager
from typing import Any, TypedDict, Unpack
from unittest.mock import AsyncMock, MagicMock, create_autospec, patch

import pytest

from techai_webutils.clients.retrieval.bedrock.engine import (
    BedrockRetrievalEngine,
    DocumentIdResolver,
    FilterBuilder,
)
from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalKind,
    new_retrieval_engine_from_config,
    wrap_retrieval_engine,
)
from techai_webutils.clients.retrieval.filtering import (
    FilteringRetrievalEngine,
    MetadataEquals,
    MinScore,
    OrdinalCeiling,
)
from techai_webutils.clients.retrieval.stub import StubRetrievalEngine
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult


class _EngineOptions(TypedDict, total=False):
    """The optional ``BedrockRetrievalEngine`` keyword arguments a test varies."""

    search_type: str
    reranking_model: str
    filter_builder: FilterBuilder | None
    document_id_resolver: DocumentIdResolver | None


def _passage(doc: str, score: float = 0.9, **metadata: str) -> RetrievalResult:
    """Build a RetrievalResult with the given score and metadata."""
    return RetrievalResult(
        document_id=doc,
        document_name=doc,
        chunk_content="chunk",
        score=score,
        page_number=None,
        metadata=dict(metadata),
    )


@contextmanager
def _bedrock_client(client: AsyncMock) -> Generator[None]:
    """Make every Bedrock engine built inside the block open ``client`` on first use.

    The engine opens its bedrock-agent-runtime client through ``aiobotocore``'s session, so this
    mocks that SDK boundary rather than reaching into the engine.
    """
    session = MagicMock()
    session.create_client.return_value.__aenter__.return_value = client
    with patch("aiobotocore.session.get_session", return_value=session):
        yield


def _inner_returning(*passages: RetrievalResult) -> RetrievalEngine:
    """Return a mocked inner RetrievalEngine whose ``retrieve`` returns ``passages``."""
    inner = create_autospec(RetrievalEngine, instance=True)
    inner.retrieve = AsyncMock(return_value=list(passages))
    return inner


class TestStubRetrievalEngine:
    """Tests for the stub retrieval engine."""

    @pytest.mark.asyncio
    async def test_returns_the_supplied_corpus_capped_at_top_k(self) -> None:
        """The stub returns the passages it was given, capped at top_k, and nothing by default.

        **Why this test is important:**
          - The stub lets a consumer run its retrieval path without a real index; it must return the
            consumer's own sample corpus (not a built-in one), and a caller mutating a result must not
            corrupt the corpus for the next query.

        **What it tests:**
          - A stub over three passages returns the first two for top_k=2; a result's metadata is a copy;
            a stub with no corpus returns nothing.
        """
        corpus = [
            _passage("a", tenant="t1"),
            _passage("b", tenant="t1"),
            _passage("c", tenant="t1"),
        ]
        stub = StubRetrievalEngine(corpus)

        results = await stub.retrieve("q", top_k=2)
        results[0].metadata["tenant"] = "mutated"

        assert [r.document_id for r in results] == ["a", "b"]
        assert corpus[0].metadata["tenant"] == "t1"
        assert await StubRetrievalEngine().retrieve("q") == []


class TestBedrockIndexRouting:
    """Per-call index_id selection + the no-fallback fail-closed backstop."""

    @staticmethod
    def _engine(kb_default: str) -> tuple[BedrockRetrievalEngine, AsyncMock]:
        """Return a BedrockRetrievalEngine and the mock client its lazy aiobotocore open yields."""
        from techai_webutils.clients.retrieval.bedrock.engine import (
            BedrockRetrievalEngine,
        )

        client = AsyncMock()
        client.retrieve = AsyncMock(return_value={"retrievalResults": []})
        with _bedrock_client(client):
            engine = BedrockRetrievalEngine(
                region="us-east-1", knowledge_base_id=kb_default
            )
        return engine, client

    @pytest.mark.asyncio
    async def test_uses_per_call_index_over_construction_default(self) -> None:
        """A per-call index_id overrides the construction-time knowledge base.

        **Why this test is important:**
          - Per-tenant routing selects the tenant's knowledge base per request; ignoring the per-call id
            would send every tenant to the same (wrong) index.

        **What it tests:**
          - ``retrieve(..., index_id="kb-per-call")`` issues Retrieve against ``kb-per-call``.
        """
        engine, client = self._engine("kb-default")

        await engine.retrieve("q", index_id="kb-per-call")

        assert client.retrieve.await_args.kwargs["knowledgeBaseId"] == "kb-per-call"

    @pytest.mark.asyncio
    async def test_uses_construction_index_when_no_per_call_id(self) -> None:
        """Without a per-call id, the construction-time knowledge base is queried.

        **Why this test is important:**
          - A single-index deployment passes no per-call id; the engine must use its configured default.

        **What it tests:**
          - ``retrieve(...)`` with no ``index_id`` issues Retrieve against the construction KB.
        """
        engine, client = self._engine("kb-default")

        await engine.retrieve("q")

        assert client.retrieve.await_args.kwargs["knowledgeBaseId"] == "kb-default"

    @pytest.mark.asyncio
    async def test_raises_when_no_index_configured_or_passed(self) -> None:
        """An engine with no default knowledge base, called with no index_id, raises (never falls back).

        **Why this test is important:**
          - An engine built for per-call routing has no default; a missed upstream routing check must
            error here, not silently query some other tenant's index.

        **What it tests:**
          - ``retrieve(..., index_id=None)`` on a no-default engine raises ``InternalError`` and never
            issues the Retrieve call.
        """
        from techai_webutils.core.errors import InternalError

        engine, client = self._engine("")

        with pytest.raises(InternalError):
            await engine.retrieve("q", index_id=None)
        client.retrieve.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_raises_on_an_empty_per_call_index_even_with_a_default(self) -> None:
        """An empty per-call index_id raises instead of quietly routing to the engine's default index.

        **Why this test is important:**
          - A per-tenant router that returns "" for an unmapped tenant must not land that tenant on the
            shared default knowledge base; only ``None`` means "use the default".

        **What it tests:**
          - ``retrieve(..., index_id="")`` on an engine with a default raises ``InternalError`` and never
            issues the Retrieve call.
        """
        from techai_webutils.core.errors import InternalError

        engine, client = self._engine("kb-default")

        with pytest.raises(InternalError, match="empty"):
            await engine.retrieve("q", index_id="")
        client.retrieve.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_maps_a_botocore_failure_to_a_coded_app_error(self) -> None:
        """A Bedrock throttle from Retrieve surfaces as a transient coded AppError, not a raw ClientError.

        **Why this test is important:**
          - The retry decorator retries only transient AppErrors and the gRPC edge maps codes to statuses;
            a raw ClientError fails on the first attempt and reports INTERNAL instead of UNAVAILABLE.

        **What it tests:**
          - A ThrottlingException from the client's retrieve raises an AppError with code UNAVAILABLE,
            is_transient True, and the ClientError as its cause.
        """
        from botocore.exceptions import ClientError

        from techai_webutils.core.errors import AppError, ErrorCode

        engine, client = self._engine("kb-default")
        throttle = ClientError(
            {
                "Error": {"Code": "ThrottlingException"},
                "ResponseMetadata": {
                    "RequestId": "",
                    "HostId": "",
                    "HTTPStatusCode": 429,
                    "HTTPHeaders": {},
                    "RetryAttempts": 0,
                },
            },
            "Retrieve",
        )
        client.retrieve = AsyncMock(side_effect=throttle)

        with pytest.raises(AppError) as excinfo:
            await engine.retrieve("q")

        assert excinfo.value.code is ErrorCode.UNAVAILABLE
        assert excinfo.value.is_transient is True
        assert excinfo.value.cause is throttle


class TestFilteringRetrievalEngine:
    """Tests for the filtering retrieval engine."""

    @pytest.mark.asyncio
    async def test_forwards_the_call_to_the_inner_engine(self) -> None:
        """The filter decorator forwards top_k, filters and index_id to the inner engine unchanged.

        **Why this test is important:**
          - Routing and backend push-down depend on the inner engine seeing the caller's arguments; a
            dropped index_id would send a routed query to the wrong index.

        **What it tests:**
          - The inner ``retrieve`` is awaited with the same query, top_k, filters and index_id.
        """
        inner = _inner_returning()
        engine = FilteringRetrievalEngine(inner, [])

        await engine.retrieve("q", top_k=5, filters={"tenant": "t1"}, index_id="kb-x")

        inner.retrieve.assert_awaited_once_with(
            "q", top_k=5, filters={"tenant": "t1"}, index_id="kb-x"
        )

    @pytest.mark.asyncio
    async def test_min_score_drops_low_relevance_passages(self) -> None:
        """Passages below the MinScore floor are excluded; a passage exactly at the floor is kept.

        **Why this test is important:**
          - The floor is the configured relevance cut-off; an off-by-one comparison (``>`` for ``>=``)
            would silently drop every passage scored exactly at the configured value.

        **What it tests:**
          - With MinScore(0.5), a 0.4 passage is dropped, and the 0.5 and 0.6 passages are kept.
        """
        engine = FilteringRetrievalEngine(
            _inner_returning(
                _passage("low", 0.4), _passage("edge", 0.5), _passage("high", 0.6)
            ),
            [MinScore(0.5)],
        )

        results = await engine.retrieve("q")

        assert [r.document_id for r in results] == ["edge", "high"]

    @pytest.mark.asyncio
    async def test_metadata_equals_isolates_scopes_and_fails_closed(self) -> None:
        """MetadataEquals keeps only the request's scope, and admits nothing without it.

        **Why this test is important:**
          - This is the tenant-isolation rule behind the backend's own filter: another scope's passage,
            or a passage with no scope label, must never reach the caller, and a request that forgot its
            scope must not widen to everything.

        **What it tests:**
          - For tenant t1: the t1 passage is kept; the t2 and the unlabelled passages are dropped.
          - A request without the tenant filter keeps nothing.
          - A request whose tenant is "" keeps nothing, even a passage stamped with tenant "".
        """
        engine = FilteringRetrievalEngine(
            _inner_returning(
                _passage("mine", tenant="t1"),
                _passage("theirs", tenant="t2"),
                _passage("bare"),
                _passage("blank", tenant=""),
            ),
            [MetadataEquals("tenant")],
        )

        scoped = await engine.retrieve("q", filters={"tenant": "t1"})
        unscoped = await engine.retrieve("q")
        blank_scope = await engine.retrieve("q", filters={"tenant": ""})

        assert [r.document_id for r in scoped] == ["mine"]
        assert unscoped == []
        assert blank_scope == []

    @pytest.mark.asyncio
    async def test_metadata_equals_can_read_a_differently_named_request_key(self) -> None:
        """MetadataEquals compares a passage key against a request key of another name.

        **Why this test is important:**
          - A consumer's stored metadata key and its request filter key often differ (``owner`` vs
            ``tenant``); comparing the wrong key would drop every in-scope passage or admit another's.

        **What it tests:**
          - MetadataEquals("owner", request_key="tenant") keeps the passage whose owner is the request's
            tenant.
        """
        engine = FilteringRetrievalEngine(
            _inner_returning(
                _passage("mine", owner="t1"), _passage("theirs", owner="t2")
            ),
            [MetadataEquals("owner", request_key="tenant")],
        )

        results = await engine.retrieve("q", filters={"tenant": "t1"})

        assert [r.document_id for r in results] == ["mine"]

    def test_ordinal_ceiling_fails_closed_both_ways(self) -> None:
        """OrdinalCeiling admits a passage at or below the caller's level and fails closed on unknowns.

        **Why this test is important:**
          - An unknown passage label must be denied to everyone and an unknown caller level must see
            only the lowest tier; either failing open leaks restricted content.

        **What it tests:**
          - "mid" is admitted for callers at "mid"/"high" and denied at "low".
          - An unknown or missing passage label is denied even at "high"; an unknown caller level acts
            as "low".
        """
        policy = OrdinalCeiling("level", "max_level", ("low", "mid", "high"))

        def admits(caller: str, label: str | None) -> bool:
            passage = _passage("d") if label is None else _passage("d", level=label)
            return policy.admits(passage, {"max_level": caller})

        assert admits("mid", "mid")
        assert admits("high", "mid")
        assert not admits("low", "mid")
        assert not admits("high", "unknown")
        assert not admits("high", None)
        assert admits("bogus", "low")
        assert not admits("bogus", "mid")

    def test_ordinal_ceiling_rejects_an_empty_ladder(self) -> None:
        """OrdinalCeiling refuses an empty ladder at construction instead of admitting everything.

        **Why this test is important:**
          - With no ranks an unknown label and an unknown caller level both rank 0, so every passage was
            admitted (fail open) — e.g. when the ladder is loaded from config that came back empty.

        **What it tests:**
          - ``OrdinalCeiling("level", "max_level", ())`` raises ValueError naming the ranks.
        """
        with pytest.raises(ValueError, match="ranks"):
            OrdinalCeiling("level", "max_level", ())

    @pytest.mark.asyncio
    async def test_every_policy_must_admit(self) -> None:
        """A passage survives only when every policy admits it; no policies admit everything.

        **Why this test is important:**
          - The rules are conjunctive: a passage that passes the score floor but not the scope (or the
            reverse) must still be dropped.

        **What it tests:**
          - With MinScore(0.1) + MetadataEquals("tenant"): only the high-scoring passage of the
            requested tenant survives; with no policies all three survive.
        """
        passages = (
            _passage("a", tenant="t1"),
            _passage("b", tenant="t2"),
            _passage("c", 0.05, tenant="t1"),
        )

        strict = FilteringRetrievalEngine(
            _inner_returning(*passages), [MinScore(0.1), MetadataEquals("tenant")]
        )
        open_ = FilteringRetrievalEngine(_inner_returning(*passages), [])

        assert [
            r.document_id for r in await strict.retrieve("q", filters={"tenant": "t1"})
        ] == ["a"]
        assert len(await open_.retrieve("q")) == 3

    @pytest.mark.asyncio
    async def test_logs_how_many_passages_each_policy_dropped(self) -> None:
        """The filter log attributes every dropped passage to the first policy that rejected it.

        **Why this test is important:**
          - "10 retrieved, 0 kept" is otherwise undiagnosable: it could be the score floor, a scope
            mismatch or a missing label, and each has a different fix.

        **What it tests:**
          - With MinScore(0.1) + MetadataEquals("tenant") over one kept, one low-score and one
            other-tenant passage, the info log reports retrieved=3, kept=1 and
            dropped_by={"MinScore": 1, "MetadataEquals": 1}.
        """
        passages = (
            _passage("a", tenant="t1"),
            _passage("b", tenant="t2"),
            _passage("c", 0.05, tenant="t1"),
        )
        engine = FilteringRetrievalEngine(
            _inner_returning(*passages), [MinScore(0.1), MetadataEquals("tenant")]
        )

        with patch("techai_webutils.clients.retrieval.filtering.logger") as mock_logger:
            await engine.retrieve("q", filters={"tenant": "t1"})

        fields = mock_logger.info.call_args.kwargs
        assert fields["retrieved"] == 3
        assert fields["kept"] == 1
        assert fields["dropped_by"] == {"MinScore": 1, "MetadataEquals": 1}


class TestRetrievalFactory:
    """Tests for the retrieval factory."""

    @pytest.mark.asyncio
    async def test_wrap_applies_the_factory_filtering_to_a_consumer_engine(self) -> None:
        """wrap_retrieval_engine gives a consumer's own engine the factory's filtering.

        **Why this test is important:**
          - A consumer with its own engine (e.g. a dev corpus) must be scoped exactly like the
            factory's kinds; repeating the wrapping in the consumer drifts from the library.

        **What it tests:**
          - A wrapped engine drops a passage under ``config.min_score`` and one a scope policy
            rejects, keeps the admitted one, and passes the query and filters through.
        """
        inner = _inner_returning(
            _passage("low", score=0.1, tenant="t1"),
            _passage("other-tenant", tenant="t2"),
            _passage("kept", tenant="t1"),
        )
        engine = wrap_retrieval_engine(
            inner, RetrievalConfig(min_score=0.5), policies=[MetadataEquals("tenant")]
        )

        got = await engine.retrieve("q", top_k=5, filters={"tenant": "t1"})

        assert [p.document_id for p in got] == ["kept"]
        inner.retrieve.assert_awaited_once()
        assert inner.retrieve.await_args.args[0] == "q"

    def test_config_rejects_a_none_min_score(self) -> None:
        """RetrievalConfig refuses min_score=None at construction instead of failing on every query.

        **Why this test is important:**
          - 0.1.x accepted ``None`` (a per-kind default); a consumer passing its old optional setting
            through would build fine and then raise TypeError inside the score policy on every retrieve.

        **What it tests:**
          - ``RetrievalConfig(min_score=None)`` raises TypeError naming min_score.
        """
        with pytest.raises(TypeError, match="min_score"):
            RetrievalConfig(min_score=None)  # type: ignore[arg-type]

    @pytest.mark.parametrize(
        ("config", "seams", "unused"),
        [
            (
                RetrievalConfig(kind=RetrievalKind.STUB),
                {"store_filter_keys": ("tenant",)},
                ["store_filter_keys"],
            ),
            (
                RetrievalConfig(kind=RetrievalKind.BEDROCK, knowledge_base_id="kb"),
                {"stub_passages": [_passage("a")]},
                ["stub_passages"],
            ),
        ],
        ids=["stub-ignores-store-filter-keys", "bedrock-ignores-stub-passages"],
    )
    def test_logs_a_seam_the_selected_kind_ignores(
        self, config: RetrievalConfig, seams: dict[str, Any], unused: list[str]
    ) -> None:
        """The factory logs a supplied seam that does not apply to the selected kind.

        **Why this test is important:**
          - Each seam reaches one kind; flipping ``kind`` for local development silently drops the
            other kind's seams, so the startup log must say which seams the selected kind ignores.

        **What it tests:**
          - A seam belonging to another kind produces an info record naming the kind and the unused seam.
        """
        with patch("techai_webutils.clients.retrieval.builder.logger") as mock_logger:
            new_retrieval_engine_from_config(config, policies=[], **seams)

        mock_logger.info.assert_any_call(
            "retrieval seams unused by the selected kind",
            kind=str(config.kind),
            seams=unused,
        )

    def test_warns_when_a_scope_policy_is_not_pushed_down(self) -> None:
        """The factory warns when a MetadataEquals scope is enforced only after the backend's top_k.

        **Why this test is important:**
          - Without a push-down the backend returns the index-wide top_k and the scope policy then drops
            the other scopes' hits, so a scope can get few or no passages although it has relevant ones.

        **What it tests:**
          - kind=bedrock with MetadataEquals("tenant") and no filter_builder warns, naming the tenant key.
        """
        with patch("techai_webutils.clients.retrieval.builder.logger") as mock_logger:
            new_retrieval_engine_from_config(
                RetrievalConfig(kind=RetrievalKind.BEDROCK, knowledge_base_id="kb"),
                policies=[MetadataEquals("tenant")],
            )

        mock_logger.warning.assert_any_call(
            "retrieval scope policies are not pushed down to the backend",
            kind="bedrock",
            scope_keys=["tenant"],
        )

    @pytest.mark.asyncio
    async def test_bedrock_kind_wraps_bedrock_in_filter(self) -> None:
        """kind=bedrock builds a filtering engine wrapping the Bedrock engine.

        **Why this test is important:**
          - Stage/prod select the Bedrock backend by config alone; the server-side re-validation must
            wrap it exactly as it wraps the stub.

        **What it tests:**
          - The engine is a FilteringRetrievalEngine whose retrieve reaches Bedrock's Retrieve on
            the configured knowledge base.
        """
        client = AsyncMock()
        client.retrieve.return_value = {"retrievalResults": []}
        with _bedrock_client(client):
            engine = new_retrieval_engine_from_config(
                RetrievalConfig(kind=RetrievalKind.BEDROCK, knowledge_base_id="kb"),
                policies=[],
            )

        assert isinstance(engine, FilteringRetrievalEngine)
        assert await engine.retrieve("q") == []
        assert client.retrieve.call_args.kwargs["knowledgeBaseId"] == "kb"

    def test_unknown_kind_raises(self) -> None:
        """An unknown retrieval kind fails loudly.

        **Why this test is important:**
          - A typo'd kind must stop the service at wiring, not fall through to some default backend.

        **What it tests:**
          - A bogus kind raises ValueError naming the kind.
        """
        with pytest.raises(ValueError, match="retrieval kind"):
            new_retrieval_engine_from_config(RetrievalConfig(kind="bogus"), policies=[])  # type: ignore[arg-type]

    @pytest.mark.asyncio
    async def test_applies_the_config_floor_and_the_consumer_policies(self) -> None:
        """The factory's filter applies ``config.min_score`` and then the consumer's scope policies.

        **Why this test is important:**
          - The score floor is config data and the scope rules are the consumer's code; dropping either
            would return irrelevant or out-of-scope passages.

        **What it tests:**
          - With min_score=0.5 and MetadataEquals("tenant"): the in-scope 0.9 passage is kept, the
            in-scope 0.3 passage and the out-of-scope 0.9 passage are dropped.
        """
        engine = new_retrieval_engine_from_config(
            RetrievalConfig(kind=RetrievalKind.STUB, min_score=0.5),
            policies=[MetadataEquals("tenant")],
            stub_passages=[
                _passage("keep", 0.9, tenant="t1"),
                _passage("weak", 0.3, tenant="t1"),
                _passage("other", 0.9, tenant="t2"),
            ],
        )

        results = await engine.retrieve("q", filters={"tenant": "t1"})

        assert [r.document_id for r in results] == ["keep"]

    @staticmethod
    async def _factory_vector_config(config: RetrievalConfig) -> dict[str, Any]:
        """Build a Bedrock engine through the factory and return the vectorSearchConfiguration it sends."""
        client = AsyncMock()
        client.retrieve.return_value = {"retrievalResults": []}
        with _bedrock_client(client):
            engine = new_retrieval_engine_from_config(config, policies=[])
        await engine.retrieve("q")
        return client.retrieve.call_args.kwargs["retrievalConfiguration"][
            "vectorSearchConfiguration"
        ]

    @pytest.mark.asyncio
    async def test_bedrock_config_threads_search_type_and_reranker(self) -> None:
        """kind=bedrock threads the config-selected search_type + reranker into the Bedrock engine.

        **Why this test is important:**
          - Hybrid search and the reranker are config-selected precision levers; a factory that dropped
            either would silently fall back to semantic-only, no-rerank retrieval.

        **What it tests:**
          - search_type=hybrid + reranking_kind=bedrock_rerank + a model id reach the Retrieve request.
        """
        vsc = await self._factory_vector_config(
            RetrievalConfig(
                kind=RetrievalKind.BEDROCK,
                knowledge_base_id="kb",
                search_type="hybrid",
                reranking_kind="bedrock_rerank",
                reranking_model="cohere.rerank-v3-5:0",
            )
        )
        assert vsc["overrideSearchType"] == "HYBRID"
        assert vsc["rerankingConfiguration"]["bedrockRerankingConfiguration"][
            "modelConfiguration"
        ]["modelArn"].endswith("/cohere.rerank-v3-5:0")

    @pytest.mark.asyncio
    async def test_reranking_kind_none_disables_reranker_even_with_a_model_id(
        self,
    ) -> None:
        """reranking_kind=none disables the reranker regardless of a stray model id.

        **Why this test is important:**
          - The kind is the switch; a leftover model id must not quietly turn on (and bill for) reranking.

        **What it tests:**
          - reranking_kind=none with a model id set sends no rerankingConfiguration.
        """
        vsc = await self._factory_vector_config(
            RetrievalConfig(
                kind=RetrievalKind.BEDROCK,
                knowledge_base_id="kb",
                reranking_kind="none",
                reranking_model="cohere.rerank-v3-5:0",
            )
        )
        assert "rerankingConfiguration" not in vsc

    @pytest.mark.asyncio
    async def test_factory_threads_the_bedrock_seams(self) -> None:
        """The factory hands the consumer's KB filter and id resolver to the Bedrock engine it builds.

        **Why this test is important:**
          - Consumers build engines through the factory; a seam the factory drops is unusable.

        **What it tests:**
          - A factory-built Bedrock engine sends the injected filter and resolves ids with the injected
            resolver.
        """
        client = AsyncMock()
        client.retrieve.return_value = {
            "retrievalResults": [
                {"content": {"text": "c"}, "score": 0.1, "metadata": {"doc": "d1"}}
            ]
        }
        with _bedrock_client(client):
            engine = new_retrieval_engine_from_config(
                RetrievalConfig(
                    kind=RetrievalKind.BEDROCK, knowledge_base_id="kb-1", min_score=0.0
                ),
                policies=[],
                filter_builder=lambda f: {
                    "equals": {"key": "tenant", "value": f["tenant"]}
                },
                document_id_resolver=lambda metadata, _filters: metadata["doc"],
            )

        results = await engine.retrieve("q", filters={"tenant": "t1"})

        sent = client.retrieve.call_args.kwargs["retrievalConfiguration"][
            "vectorSearchConfiguration"
        ]
        assert sent["filter"] == {"equals": {"key": "tenant", "value": "t1"}}
        assert [r.document_id for r in results] == ["d1"]


class TestBedrockRetrievalEngineRequest:
    """The Bedrock KB Retrieve request shape (mocked bedrock-agent-runtime client, no AWS)."""

    @staticmethod
    async def _vector_config(**engine_kwargs: Unpack[_EngineOptions]) -> dict[str, Any]:
        """Drive engine.retrieve against a mocked client and return the vectorSearchConfiguration sent."""
        from techai_webutils.clients.retrieval.bedrock.engine import (
            BedrockRetrievalEngine,
        )

        client = AsyncMock()
        client.retrieve.return_value = {"retrievalResults": []}
        with _bedrock_client(client):
            engine = BedrockRetrievalEngine(
                region="us-east-1", knowledge_base_id="kb-1", **engine_kwargs
            )
        await engine.retrieve("q", top_k=25, filters={"tenant": "t1"})
        config = client.retrieve.call_args.kwargs["retrievalConfiguration"]
        return config["vectorSearchConfiguration"]

    @pytest.mark.asyncio
    async def test_default_request_has_no_filter_override_or_rerank(self) -> None:
        """A default engine sends numberOfResults only: no metadata filter, override or rerank.

        **Why this test is important:**
          - Which metadata to pre-filter on is the consumer's choice; a built-in filter would drop every
            passage of a consumer whose chunks use other keys. Override and rerank are opt-in.

        **What it tests:**
          - The request carries numberOfResults=top_k and none of filter, overrideSearchType or
            rerankingConfiguration.
        """
        vsc = await self._vector_config()
        assert vsc == {"numberOfResults": 25}

    @pytest.mark.asyncio
    async def test_filter_builder_returning_none_sends_no_filter(self) -> None:
        """A filter builder that returns None pushes no filter down.

        **Why this test is important:**
          - ``None`` is the builder's way to say "no push-down for this request"; sending it as a filter
            would make Bedrock reject the call.

        **What it tests:**
          - With filter_builder=lambda f: None, the request carries no filter key.
        """
        vsc = await self._vector_config(filter_builder=lambda _f: None)
        assert "filter" not in vsc

    @pytest.mark.asyncio
    async def test_hybrid_request_sets_override_search_type(self) -> None:
        """search_type=hybrid sets Retrieve's overrideSearchType=HYBRID (vector + keyword).

        **Why this test is important:**
          - Hybrid search is a config-selected precision lever; without the override Bedrock runs
            semantic-only search.

        **What it tests:**
          - A hybrid engine's request carries overrideSearchType == "HYBRID".
        """
        vsc = await self._vector_config(search_type="hybrid")
        assert vsc["overrideSearchType"] == "HYBRID"

    @pytest.mark.asyncio
    async def test_rerank_request_sets_the_cohere_reranking_configuration(self) -> None:
        """A configured reranker sets the Cohere bedrockRerankingConfiguration with the model ARN.

        **Why this test is important:**
          - The exact nesting (rerankingConfiguration.type +
            bedrockRerankingConfiguration.modelConfiguration.modelArn) is what Bedrock accepts; a wrong
            shape fails the call.

        **What it tests:**
          - reranking_model builds rerankingConfiguration.type == BEDROCK_RERANKING_MODEL and the model
            ARN under bedrockRerankingConfiguration.modelConfiguration.modelArn.
        """
        vsc = await self._vector_config(reranking_model="cohere.rerank-v3-5:0")
        rerank = vsc["rerankingConfiguration"]
        assert rerank["type"] == "BEDROCK_RERANKING_MODEL"
        model_arn = rerank["bedrockRerankingConfiguration"]["modelConfiguration"][
            "modelArn"
        ]
        assert (
            model_arn
            == "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0"
        )


class TestBedrockRetrieveContract:
    """The Bedrock KB `Retrieve` response → RetrievalResult contract, on a realistic payload."""

    @staticmethod
    def _retrieve_payload(*metadatas: dict[str, object]) -> dict[str, object]:
        """Return a `Retrieve` response shaped like Bedrock's: text content, S3 location, chunk metadata."""
        return {
            "retrievalResults": [
                {
                    "content": {"text": "Quarterly revenue grew 12%.", "type": "TEXT"},
                    "location": {
                        "type": "S3",
                        "s3Location": {"uri": "s3://docs-bucket/a/b.pdf"},
                    },
                    "score": 0.71,
                    "metadata": {
                        "x-amz-bedrock-kb-source-uri": "s3://docs-bucket/a/b.pdf",
                        "x-amz-bedrock-kb-chunk-id": "1%3A0%3AabcDEF",
                        "x-amz-bedrock-kb-data-source-id": "DS123",
                        **metadata,
                    },
                }
                for metadata in metadatas
            ],
            "ResponseMetadata": {"HTTPStatusCode": 200},
        }

    async def _retrieve(
        self, *metadatas: dict[str, object], **engine_kwargs: Unpack[_EngineOptions]
    ) -> list[RetrievalResult]:
        """Run the Bedrock engine over one Retrieve payload (mocked client) and return its results."""
        from techai_webutils.clients.retrieval.bedrock.engine import (
            BedrockRetrievalEngine,
        )

        client = AsyncMock()
        client.retrieve.return_value = self._retrieve_payload(*metadatas)
        with _bedrock_client(client):
            engine = BedrockRetrievalEngine(
                region="us-east-1", knowledge_base_id="kb-1", **engine_kwargs
            )
        return await engine.retrieve("q", filters={"tenant": "t1"})

    @pytest.mark.asyncio
    async def test_maps_text_score_page_names_and_metadata(self) -> None:
        """A result carries the chunk's text, score, page, document id/name and full metadata.

        **Why this test is important:**
          - Citations are built from these fields; a mis-mapped field shows the wrong source or page.

        **What it tests:**
          - With document_id/document_name/page_number in the chunk metadata, the result uses them, plus
            the text, the score and every metadata key.
        """
        [result] = await self._retrieve({
            "document_id": "doc-42",
            "document_name": "Q3 Report",
            "page_number": "7",
            "tenant": "t1",
        })
        assert result.document_id == "doc-42"
        assert result.document_name == "Q3 Report"
        assert result.chunk_content == "Quarterly revenue grew 12%."
        assert result.score == pytest.approx(0.71)
        assert result.page_number == 7
        assert result.metadata["tenant"] == "t1"

    @pytest.mark.asyncio
    async def test_default_resolver_falls_back_to_the_source_uri(self) -> None:
        """Without a document_id the default id is the chunk's source URI; a resolver can decide otherwise.

        **Why this test is important:**
          - Standard Bedrock KB chunks carry no ``document_id``; an empty id loses provenance and makes
            every citation collapse into one entry. The source URI Bedrock stamps on every chunk is a
            stable id that guesses nothing about the consumer's object-key layout.

        **What it tests:**
          - The default returns the chunk's ``x-amz-bedrock-kb-source-uri`` for a chunk with no
            document_id; a resolver reading an object key and the request's tenant returns its own id.
        """
        [default] = await self._retrieve({"s3_key": "t1/doc-7/a.pdf"})
        [resolved] = await self._retrieve(
            {"s3_key": "t1/doc-7/a.pdf"},
            document_id_resolver=lambda metadata, filters: (
                metadata["s3_key"].removeprefix(f"{filters['tenant']}/").split("/")[0]
            ),
        )
        assert default.document_id == "s3://docs-bucket/a/b.pdf"
        assert resolved.document_id == "doc-7"

    @pytest.mark.asyncio
    async def test_chunk_without_the_scope_key_is_dropped_by_the_scope_policy(
        self,
    ) -> None:
        """Through the factory, a chunk missing the scope metadata is dropped, never trusted.

        **Why this test is important:**
          - The post-filter is the defense-in-depth check behind the knowledge base's own filter; if a
            missing scope label were filled in from the request, every unlabelled chunk would pass.

        **What it tests:**
          - With MetadataEquals("tenant"), the unlabelled chunk is dropped and the labelled one kept.
        """
        engine = new_retrieval_engine_from_config(
            RetrievalConfig(
                kind=RetrievalKind.BEDROCK, knowledge_base_id="kb-1", min_score=0.0
            ),
            policies=[MetadataEquals("tenant")],
        )
        client = AsyncMock()
        client.retrieve.return_value = self._retrieve_payload(
            {}, {"tenant": "t1", "document_id": "d2"}
        )
        engine._inner._client = client  # type: ignore[attr-defined]  # noqa: SLF001

        results = await engine.retrieve("q", filters={"tenant": "t1"})

        assert [r.document_id for r in results] == ["d2"]
