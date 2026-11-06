"""``token_counter_from_config``: the config-selected ``TokenCounter``.

``approx`` (the default) needs no model file: it counts ``ceil(len(text) / chars_per_token)``,
which is subadditive and, at 4 characters per token, matches the 2,048-character / 512-token
equivalence the ``fixed`` chunker documents. ``huggingface`` loads the embedding model's own
``tokenizer.json`` from a local path (the deployable ships the file; the engine downloads
nothing) and counts its ids without the special tokens the model adds itself.
"""

from __future__ import annotations

import importlib
import math
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, Protocol, cast, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.token_counter import TokenCounter

if TYPE_CHECKING:
    from collections.abc import Sequence


class TokenCounterKind(StrEnum):
    """Which token counter to build."""

    APPROX = "approx"
    """Characters divided by ``chars_per_token``, rounded up (no model file)."""
    HUGGINGFACE = "huggingface"
    """The embedding model's ``tokenizer.json``, through the ``tokenizers`` library."""


@dataclass(frozen=True, slots=True)
class TokenCounterConfig:
    """Selects the token counter."""

    kind: TokenCounterKind = TokenCounterKind.APPROX
    """The counter to build; ``approx`` by default."""
    chars_per_token: float = 4.0
    """``approx``: the characters one token stands for (above 0)."""
    path: str = ""
    """``huggingface``: the local path of the model's ``tokenizer.json``."""


class _Encoding(Protocol):
    """The part of a ``tokenizers.Encoding`` a count reads."""

    ids: Sequence[int]
    """The token ids."""


class _Tokenizer(Protocol):
    """The part of a ``tokenizers.Tokenizer`` a count calls."""

    def encode(self, sequence: str, *, add_special_tokens: bool) -> _Encoding:
        """Return ``sequence``'s encoding."""
        ...


class ApproxTokenCounter(TokenCounter):
    """Counts ``ceil(len(text) / chars_per_token)`` tokens."""

    def __init__(self, chars_per_token: float) -> None:
        """Count one token per ``chars_per_token`` characters, rounded up."""
        self._per = chars_per_token

    @override
    def count(self, text: str) -> int:
        """Return ``text``'s length over ``chars_per_token``, rounded up."""
        return math.ceil(len(text) / self._per)


class HuggingFaceTokenCounter(TokenCounter):
    """Counts the ids a Hugging Face ``tokenizers`` tokenizer encodes a text to."""

    def __init__(self, tokenizer: _Tokenizer) -> None:
        """Count with ``tokenizer`` (loaded from the model's ``tokenizer.json``)."""
        self._tokenizer = tokenizer

    @override
    def count(self, text: str) -> int:
        """Return the number of ids ``text`` encodes to, without special tokens."""
        return len(self._tokenizer.encode(text, add_special_tokens=False).ids)


def token_counter_from_config(config: TokenCounterConfig) -> TokenCounter:
    """Build the ``TokenCounter`` ``config.kind`` selects.

    Raises:
        AppValueError: An unknown kind, ``approx`` with ``chars_per_token`` not above 0, or
            ``huggingface`` without a ``path`` (``INVALID_INPUT``).

    """
    if config.kind == TokenCounterKind.APPROX:
        if config.chars_per_token <= 0:
            msg = f"the approx token counter needs chars_per_token > 0, got {config.chars_per_token}"
            raise AppValueError(msg)
        return ApproxTokenCounter(config.chars_per_token)
    if config.kind == TokenCounterKind.HUGGINGFACE:
        if not config.path:
            msg = "the huggingface token counter needs the tokenizer.json path"
            raise AppValueError(msg)
        library = importlib.import_module("tokenizers")
        tokenizer = cast("_Tokenizer", library.Tokenizer.from_file(config.path))
        return HuggingFaceTokenCounter(tokenizer)
    msg = f"unknown token counter kind: {config.kind!r}"
    raise AppValueError(msg)
