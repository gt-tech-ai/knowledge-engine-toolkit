"""Unit tests for the token counters.

``approx`` is real; the ``huggingface`` library is mocked.
"""

from __future__ import annotations

import sys
from types import SimpleNamespace
from typing import cast
from unittest.mock import MagicMock, patch

import pytest
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.clients.tokenizer import (
    TokenCounterConfig,
    TokenCounterKind,
    token_counter_from_config,
)
from techai_webutils.core.errors import AppValueError

_EXPECTED_COUNT = 3


@given(left=st.text(max_size=200), right=st.text(max_size=200))
def test_approx_counter_rounds_up_and_is_subadditive(left: str, right: str) -> None:
    """Test that the approximate counter never undercounts a part and is subadditive.

    **Why this test is important:**
      - The sentence chunker packs greedily, which is only safe when a text counts no more
        than its parts; rounding down would let a chunk overrun the embedder's budget.

    **What it tests:**
      - a text counts ``ceil(len / chars_per_token)`` (0 for the empty string)
      - the count of two texts joined is at most the sum of their counts

    Args:
        left: The first text.
        right: The second text.

    """
    counter = token_counter_from_config(TokenCounterConfig(chars_per_token=4.0))
    assert counter.count(left) == -(-len(left) // 4)
    assert counter.count(left + right) <= counter.count(left) + counter.count(right)


def test_huggingface_counter_counts_the_tokenizers_ids_without_special_tokens() -> None:
    """Test that the huggingface kind loads the configured tokenizer file.

    It counts the file's ids.

    **Why this test is important:**
      - Chunks are sized in the embedding model's own tokens; counting the [CLS]/[SEP] the
        model adds itself would shrink every chunk by two tokens.

    **What it tests:**
      - the tokenizer is loaded from ``path`` once
      - ``count`` is the number of ids of an encode with ``add_special_tokens=False``
      - a huggingface kind with no ``path``, and an approx kind with a ``chars_per_token``
        of 0, raise ``AppValueError``
    """
    tokenizer = MagicMock()
    cast("MagicMock", tokenizer.encode).return_value = SimpleNamespace(ids=[1, 2, 3])
    library = MagicMock()
    cast(
        "MagicMock", cast("MagicMock", library.Tokenizer).from_file
    ).return_value = tokenizer
    with patch.dict(sys.modules, {"tokenizers": library}):
        counter = token_counter_from_config(
            TokenCounterConfig(kind=TokenCounterKind.HUGGINGFACE, path="/m/tok.json")
        )
        assert counter.count("three words here") == _EXPECTED_COUNT
    cast(
        "MagicMock",
        cast(
            "MagicMock", cast("MagicMock", library.Tokenizer).from_file
        ).assert_called_once_with,
    )("/m/tok.json")
    cast("MagicMock", cast("MagicMock", tokenizer.encode).assert_called_once_with)(
        "three words here", add_special_tokens=False
    )

    with pytest.raises(AppValueError):
        _ = token_counter_from_config(
            TokenCounterConfig(kind=TokenCounterKind.HUGGINGFACE)
        )
    with pytest.raises(AppValueError):
        _ = token_counter_from_config(TokenCounterConfig(chars_per_token=0))
