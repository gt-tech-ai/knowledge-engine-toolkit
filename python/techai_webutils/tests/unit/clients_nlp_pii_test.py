"""Unit tests for the in-process ``pii`` task: span detection, redact/mask.

And the annotator.
"""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.nlp import PII, PiiAnnotator, PiiKind, PiiMode, PiiSpan
from techai_webutils.clients.nlp.pii import apply_pii, find_pii
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.annotator import (
    ENTITIES,
    Annotation,
    Annotator,
    Entity,
    TaskRegistry,
    TextBlock,
)

# Synthetic values only: reserved example domain, 555-01xx fiction range, a test card
# number.
_TEXT = (
    "Mail jane@example.com or call +1 (555) 010-2368. SSN 123-45-6789, "
    "card 4111 1111 1111 1111, host 10.0.0.12. Order 1234567890123, card 4111 1111 "
    "1111 1112."
)


def _registry() -> TaskRegistry:
    """Return a registry holding ``ENTITIES`` and ``PII``.

    Returns:
        A registry holding ``ENTITIES`` and ``PII``.

    """
    registry = TaskRegistry()
    registry.register(PII, PiiSpan)
    return registry


def test_find_pii_finds_each_kind_and_skips_ids_and_bad_cards() -> None:
    """Test that every supported format is found once, and look-alikes are not.

    **Why this test is important:**
      - A missed span leaks personal data into the index; a false one redacts an order
        number or a version a user searches for.

    **What it tests:**
      - one span each for the e-mail, phone, SSN, Luhn-valid card and IPv4 address, in
        text order, each span's text equal to the text it covers
      - a bare 13-digit order number and a card failing the Luhn check are not reported
    """
    spans = find_pii(_TEXT)

    assert [(s.kind, s.text) for s in spans] == [
        (PiiKind.EMAIL, "jane@example.com"),
        (PiiKind.PHONE, "+1 (555) 010-2368"),
        (PiiKind.SSN, "123-45-6789"),
        (PiiKind.CARD, "4111 1111 1111 1111"),
        (PiiKind.IP_ADDRESS, "10.0.0.12"),
    ]
    assert all(_TEXT[s.start : s.end] == s.text for s in spans)


def test_redact_and_mask_leave_no_span_and_keep_leaves_the_text() -> None:
    """Test that ``apply_pii`` removes every detected value in both rewriting modes.

    **Why this test is important:**
      - Redaction is the guarantee a consumer relies on before text reaches an index or a
        model; one value surviving it is a leak.

    **What it tests:**
      - ``redact`` and ``mask`` outputs contain no detected value and ``find_pii`` finds
        nothing in them; ``mask`` keeps the length, ``redact`` writes ``[KIND]``
      - ``keep`` returns the text unchanged; an empty span list changes nothing
    """
    spans = find_pii(_TEXT)

    redacted = apply_pii(_TEXT, spans, PiiMode.REDACT)
    masked = apply_pii(_TEXT, spans, PiiMode.MASK)

    for rewritten in (redacted, masked):
        assert find_pii(rewritten) == []
        assert all(s.text not in rewritten for s in spans)
    assert "[EMAIL]" in redacted
    assert "[CARD]" in redacted
    assert len(masked) == len(_TEXT)
    assert apply_pii(_TEXT, spans, PiiMode.KEEP) == _TEXT
    assert apply_pii(_TEXT, [], PiiMode.REDACT) == _TEXT
    with pytest.raises(AppValueError):
        PiiSpan(PiiKind.EMAIL, 3, 3, "")


@pytest.mark.asyncio
async def test_annotator_answers_pii_and_passes_other_tasks_on() -> None:
    """Test that ``PiiAnnotator`` adds the ``pii`` results and calls the model only.

    When needed.

    **Why this test is important:**
      - Asking for ``pii`` must not cost a model call, and the model must never see a task
        it cannot run.

    **What it tests:**
      - with ``{entities, pii}``, the inner annotator is called with ``{entities}`` only,
        and each annotation keeps its entities and gains its block's ``pii`` spans
      - with ``{pii}`` alone, the inner annotator is not called
      - an unregistered task is refused with ``AppValueError`` before any call
    """
    blocks = [
        TextBlock("Ann wrote to jane@example.com", "c1"),
        TextBlock("no data", "c2"),
    ]
    ann = Entity("PERSON", 0, 3, "Ann")
    inner = MagicMock(spec=Annotator)
    inner.annotate_batch = AsyncMock(
        return_value=[Annotation(entities=(ann,)), Annotation()]
    )
    annotator = PiiAnnotator(inner, registry=_registry())

    both = await annotator.annotate_batch(blocks, frozenset({ENTITIES, PII}), "p")

    inner.annotate_batch.assert_awaited_once_with(blocks, frozenset({ENTITIES}), "p")
    assert both[0].entities == (ann,)
    assert [s.text for s in both[0].task_results[PII]] == ["jane@example.com"]
    assert both[1].task_results[PII] == ()

    inner.annotate_batch.reset_mock()
    only = await annotator.annotate_batch(blocks, frozenset({PII}), "p")

    inner.annotate_batch.assert_not_awaited()
    assert [len(a.task_results[PII]) for a in only] == [1, 0]
    with pytest.raises(AppValueError):
        await PiiAnnotator(inner, registry=TaskRegistry()).annotate_batch(
            blocks, frozenset({PII}), "p"
        )
    inner.annotate_batch.assert_not_awaited()
