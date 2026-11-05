"""``FastTextIdentifier``: a fastText language-ID model (``lid.176`` or GlotLID), in process.

The model file is the deployable's (``LanguageIdentifierConfig.model_path``); nothing is
downloaded. It loads on the first ``identify`` (construction does no I/O) through
``fasttext-predict`` (the ``fasttext`` extra: the inference-only fastText build, with wheels for
Linux and macOS on x86_64 and arm64, and no NumPy). fastText reads one line, so the text's
whitespace is collapsed to single spaces first; fastText splits words on whitespace either way.

Labels: ``lid.176`` reports ``__label__en`` (ISO 639-1); GlotLID ``__label__eng_Latn`` (ISO 639-3
and the ISO 15924 script), which becomes code ``eng`` and script ``Latn``.
"""

from __future__ import annotations

import importlib
import threading
from enum import StrEnum
from typing import TYPE_CHECKING, Protocol, cast, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.language_identifier import (
    UNDETERMINED_GUESS,
    LanguageGuess,
    LanguageIdentifier,
)

if TYPE_CHECKING:
    from collections.abc import Callable, Sequence

_LABEL_PREFIX = "__label__"


class LabelScheme(StrEnum):
    """How the model's labels encode the language."""

    ISO_639_1 = "iso_639_1"
    """``__label__en`` (the ``lid.176`` model)."""
    GLOTLID = "glotlid"
    """``__label__eng_Latn``: ISO 639-3 and the script (GlotLID)."""


class FastTextModel(Protocol):
    """The part of a ``fasttext-predict`` model this identifier calls."""

    def predict(self, text: str, k: int = 1) -> tuple[Sequence[str], Sequence[float]]:
        """Return the ``k`` most probable labels and their probabilities for one line."""
        ...


class FastTextIdentifier(LanguageIdentifier):
    """Identifies languages with a fastText model file."""

    def __init__(self, model_path: str, *, scheme: LabelScheme) -> None:
        """Load ``model_path`` on first use and read its labels with ``scheme``."""
        self._model_path = model_path
        self._scheme = scheme
        self._model: FastTextModel | None = None
        self._lock = threading.Lock()

    @override
    def identify(self, text: str) -> LanguageGuess:
        """Return the model's most probable language, or ``UNDETERMINED_GUESS``.

        Raises:
            AppValueError: The model file cannot be loaded (``INVALID_INPUT``).

        """
        line = " ".join(text.split())
        if not line:
            return UNDETERMINED_GUESS
        labels, probabilities = self._loaded().predict(line, k=1)
        if not labels:
            return UNDETERMINED_GUESS
        code, script = self._parse(labels[0])
        return LanguageGuess(code, min(1.0, max(0.0, float(probabilities[0]))), script)

    def _loaded(self) -> FastTextModel:
        """Return the model, loading it once (under a lock: concurrent first calls load once)."""
        with self._lock:
            if self._model is None:
                fasttext = importlib.import_module("fasttext")
                load = cast("Callable[[str], FastTextModel]", fasttext.load_model)
                try:
                    self._model = load(self._model_path)
                except ValueError as exc:
                    msg = f"cannot load the language-ID model at {self._model_path!r}"
                    raise AppValueError(msg) from exc
            return self._model

    def _parse(self, label: str) -> tuple[str, str]:
        """Split a label into the language code and the script (empty where none)."""
        name = label.removeprefix(_LABEL_PREFIX)
        if self._scheme == LabelScheme.GLOTLID:
            code, _, script = name.partition("_")
            return code, script
        return name, ""
