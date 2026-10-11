"""Tests for the streaming response generator (token stream + non-streaming completion).

Prompt-agnostic: the generator takes an injected prompt strategy, so these tests inject a spec'd mock
prompt (``create_autospec(GenerationPrompt)``) and assert the generator's *logic* (streaming +
completion) — the prompt *content* (numbered passages, citation instruction) is the consuming app's
concern and is tested there.
"""

import asyncio
from collections.abc import AsyncIterator
from unittest.mock import create_autospec

import pytest

from techai_webutils.core.interfaces.llm import LLMProvider, LLMResponse
from techai_webutils.core.interfaces.retrieval import HistoryTurn, RetrievalResult
from techai_webutils.pipelines.generator import GenerationPrompt, ResponseGenerator


async def _aiter(items: list[str]) -> AsyncIterator[str]:
    """Yield the items as an async iterator."""
    await asyncio.sleep(0)
    for item in items:
        yield item


def _passage(text: str) -> RetrievalResult:
    """Return a retrieval result carrying ``text`` as its chunk."""
    return RetrievalResult(
        document_id="d1",
        document_name="Doc",
        chunk_content=text,
        score=0.9,
        page_number=1,
        metadata={},
    )


class TestResponseGenerator:
    """Tests for the response generator."""

    @pytest.mark.asyncio
    async def test_stream_yields_llm_tokens(self) -> None:
        """Test that streaming reassembles the LLM's tokens (generator logic, prompt-agnostic).

        **Why this test is important:**
          - The generator's job is to pass the injected prompt to the LLM and relay its token stream;
            a break here means answers never stream to the client.

        **What it tests:**
          - Tokens reassemble to the LLM output and the LLM stream is invoked once.
        """
        llm = create_autospec(LLMProvider, instance=True)
        llm.stream.return_value = _aiter(["Answer ", "[1]"])
        tokens = [
            token
            async for token in ResponseGenerator(
                llm, create_autospec(GenerationPrompt, instance=True)
            ).stream("q", [_passage("context text")])
        ]
        assert "".join(tokens) == "Answer [1]"
        llm.stream.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_complete_returns_answer(self) -> None:
        """Test that the non-streaming path returns the LLM completion.

        **What it tests:**
          - complete() returns the LLM response content.
        """
        llm = create_autospec(LLMProvider, instance=True)
        llm.complete.return_value = LLMResponse(
            content="the answer",
            model="m",
            input_tokens=1,
            output_tokens=1,
            finish_reason="stop",
        )
        assert (
            await ResponseGenerator(
                llm, create_autospec(GenerationPrompt, instance=True)
            ).complete("q", [_passage("c")])
            == "the answer"
        )

    @pytest.mark.asyncio
    async def test_complete_threads_history_to_prompt(self) -> None:
        """Test that complete() passes the conversation history through to the injected prompt.

        **Why this test is important:**
          - The generator must hand the prompt the history so it can render the non-citable history
            section; dropping it silently would lose conversational context.

        **What it tests:**
          - The prompt's messages() is invoked with the query, passages, AND the history turns.
        """
        llm = create_autospec(LLMProvider, instance=True)
        llm.complete.return_value = LLMResponse(
            content="a", model="m", input_tokens=1, output_tokens=1, finish_reason="stop"
        )
        prompt = create_autospec(GenerationPrompt, instance=True)
        passages = [_passage("c")]
        history = [HistoryTurn(role="user", content="hi")]
        _ = await ResponseGenerator(llm, prompt).complete("q", passages, history)
        prompt.messages.assert_called_once_with("q", passages, history)

    @pytest.mark.asyncio
    async def test_forwards_the_injected_decoding_config_to_the_llm(self) -> None:
        """Test that the generator forwards its injected per-call LLMConfig to the LLM.

        **Why this test is important:**
          - Without the config the provider decodes at its defaults (max_tokens=1024, temperature=0.7),
            truncating long cited answers and lowering faithfulness. The generator must hand its
            composition-root decoding config to the LLM on BOTH the streaming and completion paths.

        **What it tests:**
          - stream() and complete() invoke the LLM with the injected config as the second argument.
        """
        from techai_webutils.core.interfaces.llm import LLMConfig

        cfg = LLMConfig(max_tokens=4096, temperature=0.1)
        llm = create_autospec(LLMProvider, instance=True)
        llm.stream.return_value = _aiter(["x"])
        llm.complete.return_value = LLMResponse(
            content="a", model="m", input_tokens=1, output_tokens=1, finish_reason="stop"
        )
        prompt = create_autospec(GenerationPrompt, instance=True)
        gen = ResponseGenerator(llm, prompt, cfg)
        _ = [token async for token in gen.stream("q", [_passage("c")])]
        llm.stream.assert_awaited_once_with(prompt.messages.return_value, cfg)
        _ = await gen.complete("q", [_passage("c")])
        llm.complete.assert_awaited_once_with(prompt.messages.return_value, cfg)
