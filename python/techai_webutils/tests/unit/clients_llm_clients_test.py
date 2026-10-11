"""Tests for the stub LLM provider + env-aware LLM factory."""

from typing import Any, Self
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest

from techai_webutils.clients.llm.bedrock import BedrockLlmProvider
from techai_webutils.clients.llm.builder import LlmConfig, LlmKind, new_llm_from_config
from techai_webutils.clients.llm.ollama import OllamaLlmProvider
from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage


def _messages(user: str) -> list[LLMMessage]:
    """Return a system + user message pair."""
    return [
        LLMMessage(role="system", content="instruction"),
        LLMMessage(role="user", content=user),
    ]


async def _converse_args(
    model: str, messages: list[LLMMessage], config: LLMConfig | None
) -> dict[str, Any]:
    """Run ``complete`` against a mocked bedrock-runtime client; return the Converse kwargs sent."""
    runtime = MagicMock()
    runtime.converse = AsyncMock(return_value={})
    client_cm = MagicMock()
    client_cm.__aenter__ = AsyncMock(return_value=runtime)
    client_cm.__aexit__ = AsyncMock(return_value=None)
    session = MagicMock()
    session.create_client = MagicMock(return_value=client_cm)
    with patch("aiobotocore.session.get_session", return_value=session):
        async with BedrockLlmProvider(region="us-east-1", model=model) as provider:
            await provider.complete(messages, config)
    return runtime.converse.call_args.kwargs


class TestStubLlmProvider:
    """Tests for the stub LLM provider."""

    @pytest.mark.asyncio
    async def test_complete_echoes_last_user_message(self) -> None:
        """Test that the stub completion echoes the last user message deterministically.

        **Why this test is important:**
          - Dev has no Bedrock; a deterministic stub is what lets the query rewriter (passthrough)
            and generator run locally and be asserted.

        **What it tests:**
          - complete() returns the last user message verbatim with a stop finish_reason.
        """
        response = await StubLlmProvider().complete(_messages("hello world"))
        assert response.content == "hello world"
        assert response.finish_reason == "stop"

    @pytest.mark.asyncio
    async def test_stream_yields_tokens(self) -> None:
        """Test that the stub streams the last user message token-by-token.

        **Why this test is important:**
          - The generator relies on token streaming; the stub must produce awaitable-then-iterable
            token deltas matching the LLMProvider contract.

        **What it tests:**
          - awaiting stream() yields an async iterator whose tokens reassemble the message.
        """
        iterator = await StubLlmProvider().stream(_messages("alpha beta gamma"))
        tokens = [token async for token in iterator]
        assert "".join(tokens).strip() == "alpha beta gamma"

    def test_model_name(self) -> None:
        """Test that the stub reports its model identifier.

        **Why this test is important:**
          - ``model_name`` is what ends up in traces/metrics as the model dimension; the stub must
            report a distinct id so dev telemetry isn't mislabeled as a real Bedrock model.

        **What it tests:**
          - model_name() returns 'stub-llm'.
        """
        assert StubLlmProvider().model_name() == "stub-llm"


class TestConverseArgs:
    """The model-agnostic Converse request mapping (Nova / Claude / Llama all share this shape)."""

    @pytest.mark.asyncio
    async def test_splits_system_and_maps_turns(self) -> None:
        """Test that system messages become Converse ``system`` blocks and turns map to content lists.

        **Why this test is important:**
          - This mapping is what makes the Bedrock client provider-agnostic — Converse takes one
            shape for every model, so a model swap (Claude -> Nova) is a config change. A wrong shape
            (e.g. leaving system inline, or a bare string content) fails the API for all models.

        **What it tests:**
          - modelId passes through; system messages go to ``system=[{"text": ...}]``; non-system turns
            become ``{"role", "content":[{"text": ...}]}``; inferenceConfig carries the default maxTokens.
        """
        args = await _converse_args("amazon.nova-lite-v1:0", _messages("hi"), None)
        assert args["modelId"] == "amazon.nova-lite-v1:0"
        assert args["system"] == [{"text": "instruction"}]
        assert args["messages"] == [{"role": "user", "content": [{"text": "hi"}]}]
        assert args["inferenceConfig"]["maxTokens"] == 1024

    @pytest.mark.asyncio
    async def test_omits_system_when_no_system_message(self) -> None:
        """Test that ``system`` is omitted entirely when there is no system message.

        **Why this test is important:**
          - Converse rejects an empty ``system`` list, so the key must be absent (not ``[]``) when the
            caller sends only user turns — otherwise every such call errors.

        **What it tests:**
          - A user-only message list produces args without a ``system`` key.
        """
        args = await _converse_args(
            "amazon.nova-lite-v1:0", [LLMMessage(role="user", content="hi")], None
        )
        assert "system" not in args

    @pytest.mark.asyncio
    async def test_inference_config_carries_temperature_and_omits_top_p(self) -> None:
        """Test that a per-call LLMConfig's max_tokens/temperature reach inferenceConfig and topP is omitted.

        **Why this test is important:**
          - With config=None the provider decodes at 1024/0.7 — truncating long cited answers and
            reducing faithfulness — so the composition root's max_tokens/temperature must
            reach inferenceConfig verbatim.
          - Bedrock Converse rejects ``temperature`` and ``topP`` together for Claude models ("cannot
            both be specified"), so the request must carry temperature only; sending topP alongside it
            regressed every query to a 400 ValidationException.

        **What it tests:**
          - complete(config=LLMConfig(...)) sends maxTokens/temperature from the config and does
            not send topP; the config=None path falls back to the provider defaults, still without topP.
        """
        config = LLMConfig(max_tokens=4096, temperature=0.1, top_p=0.8)
        inf = (await _converse_args("m", _messages("hi"), config))["inferenceConfig"]
        default = (await _converse_args("m", _messages("hi"), None))["inferenceConfig"]
        assert inf == {"maxTokens": 4096, "temperature": 0.1}
        assert "topP" not in default


class TestLlmFactory:
    """Tests for the LLM factory."""

    def test_stub_kind_selects_stub(self) -> None:
        """Test that kind=stub builds the stub provider (dev, no Bedrock).

        **Why this test is important:**
          - This is the env-aware seam; dev must get the stub without loading AWS.

        **What it tests:**
          - new_llm_from_config(kind=STUB) returns a StubLlmProvider.
        """
        assert isinstance(
            new_llm_from_config(LlmConfig(kind=LlmKind.STUB)), StubLlmProvider
        )

    def test_ollama_kind_selects_ollama(self) -> None:
        """Test that kind=ollama builds the local Ollama provider (dev, real generation, no Bedrock).

        **Why this test is important:**
          - The whole point of the LLMProvider ABC is that pointing the retrieval engine at a local
            Ollama is a config change, not a code change. Dev must be able to select real local
            generation without loading AWS — this is the seam that makes that true.

        **What it tests:**
          - new_llm_from_config(kind=OLLAMA, host, model) returns an OllamaLlmProvider bound to the model.
        """
        from techai_webutils.clients.llm.ollama import OllamaLlmProvider

        provider = new_llm_from_config(
            LlmConfig(kind=LlmKind.OLLAMA, host="http://ollama:11434", model="llama3.2")
        )
        assert isinstance(provider, OllamaLlmProvider)
        assert provider.model_name() == "llama3.2"

    def test_bedrock_kind_selects_bedrock(self) -> None:
        """Test that kind=bedrock builds the real Bedrock provider (stage/prod).

        **Why this test is important:**
          - Stage/prod must use the real Bedrock LLM; the same config seam must flip to it without any
            code change, or production would silently run the echo stub.

        **What it tests:**
          - new_llm_from_config(kind=BEDROCK) returns a BedrockLlmProvider.
        """
        from techai_webutils.clients.llm.bedrock import BedrockLlmProvider

        assert isinstance(
            new_llm_from_config(LlmConfig(kind=LlmKind.BEDROCK, model="model-x")),
            BedrockLlmProvider,
        )

    def test_bedrock_with_fallback_model_wraps_in_fallback_chain(self) -> None:
        """Test that a bedrock provider with a fallback_model is wrapped in the fallback chain.

        **Why this test is important:**
          - The throttle/5xx fallback only protects generation if the factory actually wraps the
            primary; without a fallback_model it must stay a bare provider (no wasteful second client).

        **What it tests:**
          - kind=bedrock + fallback_model -> FallbackLlmProvider reporting the primary model; no
            fallback_model -> a bare BedrockLlmProvider.
        """
        from techai_webutils.clients.llm.bedrock import BedrockLlmProvider
        from techai_webutils.clients.llm.decorators import FallbackLlmProvider

        bare = new_llm_from_config(LlmConfig(kind=LlmKind.BEDROCK, model="primary"))
        assert isinstance(bare, BedrockLlmProvider)
        wrapped = new_llm_from_config(
            LlmConfig(kind=LlmKind.BEDROCK, model="primary", fallback_model="secondary")
        )
        assert isinstance(wrapped, FallbackLlmProvider)
        assert wrapped.model_name() == "primary"

    def test_unknown_kind_raises(self) -> None:
        """Test that an unknown LLM kind fails loudly.

        **Why this test is important:**
          - A misconfigured kind must fail at startup, not silently degrade to a wrong (or no) provider
            — the same loud-failure contract as the logger/cache/KB factories.

        **What it tests:**
          - A bogus kind raises ValueError naming the kind.
        """
        with pytest.raises(ValueError, match="llm kind"):
            new_llm_from_config(LlmConfig(kind="bogus"))  # type: ignore[arg-type]

    @pytest.mark.parametrize("kind", [LlmKind.BEDROCK, LlmKind.OLLAMA])
    def test_model_kinds_with_an_empty_model_fail_loudly(self, kind: LlmKind) -> None:
        """Test that the bedrock and ollama kinds with no model id fail loudly instead of building a provider.

        **Why this test is important:**
          - There is no built-in default model, so a config that selects bedrock or ollama but leaves the
            model unset would otherwise defer the failure to the first request, where it surfaces as an
            opaque Bedrock validation/AccessDenied error or an Ollama "model is required"/404. Failing at
            construction — like the unknown-kind guard — turns that into a loud, actionable startup error.

        **What it tests:**
          - new_llm_from_config(kind=<bedrock|ollama>, model="") raises ValueError naming the missing model,
            and so does a config of that kind that sets no model at all.
        """
        with pytest.raises(ValueError, match="model"):
            new_llm_from_config(LlmConfig(kind=kind, model=""))
        with pytest.raises(ValueError, match="model"):
            new_llm_from_config(LlmConfig(kind=kind))


class TestOllamaLlmProvider:
    """The Ollama LLM provider over an injected (mock) httpx client — /api/chat, no network."""

    @pytest.mark.asyncio
    async def test_complete_posts_api_chat_and_maps_the_response(self) -> None:
        """complete() calls /api/chat and maps message.content + token counts onto LLMResponse.

        **Why this test is important:**
          - This is the local generation path selected by config; if it posts the wrong endpoint or
            drops the assistant content, the RAG generator returns an empty answer for every query.

        **What it tests:**
          - complete() posts to "/api/chat" with the messages; the LLMResponse carries the assistant
            content, model, eval token count, and finish reason.
        """
        resp = MagicMock()
        resp.json.return_value = {
            "model": "llama3.2",
            "message": {"role": "assistant", "content": "Ishmael had little money."},
            "done_reason": "stop",
            "prompt_eval_count": 12,
            "eval_count": 6,
        }
        resp.raise_for_status.return_value = None
        client = MagicMock(spec=httpx.AsyncClient)
        client.post = AsyncMock(return_value=resp)

        response = await OllamaLlmProvider(client, model="llama3.2").complete(
            _messages("Did Ishmael have money?")
        )

        assert client.post.call_args.args[0] == "/api/chat"
        sent = client.post.call_args.kwargs["json"]
        assert sent["model"] == "llama3.2"
        assert [m["role"] for m in sent["messages"]] == ["system", "user"]
        assert response.content == "Ishmael had little money."
        assert response.model == "llama3.2"
        assert response.output_tokens == 6
        assert response.finish_reason == "stop"

    @pytest.mark.asyncio
    async def test_complete_wraps_transport_error_in_coded_app_error(self) -> None:
        """A non-200 from Ollama surfaces as a coded, transient AppError — not a raw httpx error.

        Why this test is important:
          - The resilience decorators (retry, circuit breaker) and the transport status mapping derive
            their behaviour from the ErrorCode (ARCHITECTURE.md#error-codes); a raw ``httpx.HTTPStatusError`` escaping the
            provider would bypass transient-vs-terminal classification, so a retryable 5xx would look
            terminal.

        What it tests:
          - A 503 from ``raise_for_status`` makes ``complete()`` raise an ``AppError`` coded
            ``UNAVAILABLE`` (transient), with the httpx error preserved as the cause.
        """
        request = httpx.Request("POST", "http://ollama/api/chat")
        resp = MagicMock()
        resp.raise_for_status.side_effect = httpx.HTTPStatusError(
            "service unavailable",
            request=request,
            response=httpx.Response(503, request=request),
        )
        client = MagicMock(spec=httpx.AsyncClient)
        client.post = AsyncMock(return_value=resp)

        with pytest.raises(AppError) as exc_info:
            await OllamaLlmProvider(client, model="llama3.2").complete(_messages("hi"))

        err = exc_info.value
        assert err.code == ErrorCode.UNAVAILABLE
        assert err.is_transient
        assert isinstance(err.__cause__, httpx.HTTPStatusError)

    @pytest.mark.asyncio
    async def test_stream_yields_content_deltas_until_done(self) -> None:
        """stream() yields each /api/chat NDJSON content delta and stops at the done chunk.

        **Why this test is important:**
          - The WS query streams tokens as they arrive; a stream that ignores ``done`` (never
            terminates) or drops deltas would hang or truncate the live answer.

        **What it tests:**
          - The concatenated stream equals the assistant text and iteration stops at ``done``.
        """
        lines = [
            '{"message":{"content":"Ish"},"done":false}',
            '{"message":{"content":"mael"},"done":false}',
            '{"message":{"content":""},"done":true,"done_reason":"stop"}',
        ]

        class _StreamCtx:
            async def __aenter__(self) -> Self:
                return self

            async def __aexit__(self, *_: object) -> bool:
                return False

            def raise_for_status(self) -> None:
                return None

            async def aiter_lines(self):  # noqa: ANN202
                for line in lines:
                    yield line

        client = MagicMock(spec=httpx.AsyncClient)
        client.stream = MagicMock(return_value=_StreamCtx())

        iterator = await OllamaLlmProvider(client, model="llama3.2").stream(
            _messages("hi")
        )
        tokens = [token async for token in iterator]

        assert "".join(tokens) == "Ishmael"

    def test_model_name_returns_the_bound_model(self) -> None:
        """model_name() returns the configured chat model id."""
        client = MagicMock(spec=httpx.AsyncClient)
        assert OllamaLlmProvider(client, model="llama3.2").model_name() == "llama3.2"
