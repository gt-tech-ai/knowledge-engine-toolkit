"""Tests for the LLM query rewriter (conversation follow-up -> standalone query).

Prompt-agnostic: the rewriter takes an injected prompt strategy, so these tests inject a spec'd mock
prompt (``create_autospec(RewritePrompt)``) and assert the rewriter's *logic* (passthrough vs LLM
call) independent of any product prompt.
"""

from unittest.mock import create_autospec

import pytest

from techai_webutils.core.interfaces.llm import LLMProvider, LLMResponse
from techai_webutils.core.interfaces.retrieval import HistoryTurn
from techai_webutils.pipelines.rewriter import LlmQueryRewriter, RewritePrompt


class TestLlmQueryRewriter:
    """Tests for the LLM query rewriter."""

    @pytest.mark.asyncio
    async def test_passthrough_on_first_turn(self) -> None:
        """Test that with no conversation context the query is returned unchanged (no LLM call).

        **Why this test is important:**
          - Rewriting a first-turn question wastes an LLM call and can distort a already-standalone
            query; passthrough is the correct, cheaper behavior.

        **What it tests:**
          - rewrite(query, context=None) returns [query] and never calls the LLM.
        """
        llm = create_autospec(LLMProvider, instance=True)
        result = await LlmQueryRewriter(
            llm, create_autospec(RewritePrompt, instance=True)
        ).rewrite("What is Q3 revenue?")
        assert result == ["What is Q3 revenue?"]
        llm.complete.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_forwards_the_injected_decoding_config_to_the_llm(self) -> None:
        """Test that the rewriter forwards its injected per-call LLMConfig to the LLM.

        **Why this test is important:**
          - A low temperature keeps the follow-up→standalone rewrite deterministic; without the config
            the provider rewrites at temperature=0.7. The rewriter must pass its decoding config through.

        **What it tests:**
          - rewrite() (history present) invokes the LLM complete with the injected config as the second argument.
        """
        from techai_webutils.core.interfaces.llm import LLMConfig

        cfg = LLMConfig(temperature=0.1)
        llm = create_autospec(LLMProvider, instance=True)
        llm.complete.return_value = LLMResponse(
            content="standalone",
            model="m",
            input_tokens=1,
            output_tokens=1,
            finish_reason="stop",
        )
        prompt = create_autospec(RewritePrompt, instance=True)
        await LlmQueryRewriter(llm, prompt, cfg).rewrite(
            "follow up", [HistoryTurn(role="user", content="hi")]
        )
        llm.complete.assert_awaited_once_with(prompt.messages.return_value, cfg)

    @pytest.mark.asyncio
    async def test_rewrites_follow_up_with_history(self) -> None:
        """Test that a follow-up with role-tagged history is rewritten to the LLM's standalone query.

        **Why this test is important:**
          - Follow-ups ("What about Q4?") are meaningless to retrieval without context; the rewrite
            is what makes multi-turn conversation retrieve the right passages.

        **What it tests:**
          - With history turns, rewrite calls the LLM once and returns its standalone question.
        """
        llm = create_autospec(LLMProvider, instance=True)
        llm.complete.return_value = LLMResponse(
            content="Q4 revenue analysis",
            model="m",
            input_tokens=1,
            output_tokens=1,
            finish_reason="stop",
        )
        result = await LlmQueryRewriter(
            llm, create_autospec(RewritePrompt, instance=True)
        ).rewrite(
            "What about Q4?",
            history=[HistoryTurn(role="user", content="Tell me about Q3 revenue")],
        )
        assert result == ["Q4 revenue analysis"]
        llm.complete.assert_awaited_once()
