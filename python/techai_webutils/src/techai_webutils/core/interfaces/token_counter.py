"""``TokenCounter``: how many tokens a text is, as an embedding model would count them.

A chunker sizes its chunks in the embedding model's tokens, so a chunk never overruns the
model's input limit and a budget means the same thing across models. A counter is pure and
in-process (no network call per chunk) and subadditive: a text counts no more than the sum
of its parts, which greedy packing relies on.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


class TokenCounter(ABC):
    """Counts a text's tokens the way one embedding model's tokenizer does."""

    @abstractmethod
    def count(self, text: str) -> int:
        """Return the number of tokens in ``text`` (0 for the empty string).

        Args:
            text: The text to count.

        Returns:
            The number of tokens in ``text``; 0 for the empty string.

        """
