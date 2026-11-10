"""Unit tests for the language identifiers (real langdetect; fastText's loader mocked)."""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import cast
from unittest.mock import MagicMock, patch

import pytest

from techai_webutils.clients.language import (
    LanguageIdentifierConfig,
    LanguageIdentifierKind,
    language_identifier_from_config,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.language_identifier import (
    UNDETERMINED_GUESS,
    LanguageGuess,
)

_PARITY = Path(__file__).resolve().parents[1] / "fixtures" / "language" / "parity.jsonl"


def _model(label: str, probability: float) -> MagicMock:
    """Return a mocked fastText model predicting ``label`` with ``probability``.

    Args:
        label: The detected language label.
        probability: Its probability.

    Returns:
        A mocked fastText model predicting ``label`` with ``probability``.

    """
    model = MagicMock()
    cast("MagicMock", model.predict).return_value = ((label,), (probability,))
    return model


def test_default_langdetect_kind_identifies_the_parity_set() -> None:
    """Test that the default kind is today's langdetect.

    And identifies the committed parity set.

    **Why this test is important:**
      - The default must report what documents carry today; the parity set is what a
        ``fasttext`` rollout is compared against.

    **What it tests:**
      - ``LanguageIdentifierConfig()`` builds langdetect, which names each parity row's
        language with a confidence in ``(0, 1]``
      - blank text is ``UNDETERMINED_GUESS``
    """
    identifier = language_identifier_from_config(LanguageIdentifierConfig())
    rows = [json.loads(line) for line in _PARITY.read_text().splitlines()]

    guesses = [
        identifier.identify(cast("str", row["text"]))
        for row in cast("list[dict[str, object]]", rows)
    ]

    assert [g.code for g in guesses] == [
        row["language"] for row in cast("list[dict[str, object]]", rows)
    ]
    assert all(0.0 < g.confidence <= 1.0 for g in guesses)
    assert identifier.identify(" \n ") == UNDETERMINED_GUESS


def test_fasttext_kind_loads_once_on_first_use_and_reads_one_line() -> None:
    """Test that the ``fasttext`` kind loads its model lazily, once.

    And predicts one line.

    **Why this test is important:**
      - Construction at the composition root must do no I/O; fastText refuses text with a
        newline, which every multi-line block has.

    **What it tests:**
      - building the identifier does not load the model; blank text never loads it
      - two calls load it once; the text reaches ``predict`` with whitespace collapsed
      - ``__label__de`` with a probability rounded above 1 becomes ``("de", 1.0)``
    """
    model = _model("__label__de", 1.00002)
    with patch("fasttext.load_model", return_value=model) as load:
        identifier = language_identifier_from_config(
            LanguageIdentifierConfig(
                kind=LanguageIdentifierKind.FASTTEXT, model_path="lid.ftz"
            )
        )
        blank = identifier.identify("\n\t ")
        loaded_at_build = load.call_count
        first = identifier.identify("Der Bericht\nist  fertig.")
        _ = identifier.identify("Noch einer.")

    assert (loaded_at_build, blank) == (0, UNDETERMINED_GUESS)
    load.assert_called_once_with("lid.ftz")
    cast("MagicMock", cast("MagicMock", model.predict).assert_any_call)(
        "Der Bericht ist fertig.", k=1
    )
    assert first == LanguageGuess("de", 1.0)


def test_glotlid_kind_splits_the_code_and_script() -> None:
    """Test that the ``glotlid`` kind reads GlotLID's ``<iso639-3>_<script>`` labels.

    **Why this test is important:**
      - GlotLID covers long-tail languages by ISO 639-3 code and script; reading its label
        as one opaque code would never match a configured language.

    **What it tests:**
      - ``__label__eng_Latn`` becomes code ``eng``, script ``Latn``; no prediction is
        ``UNDETERMINED_GUESS``
    """
    model = _model("__label__eng_Latn", 0.93)
    with patch("fasttext.load_model", return_value=model):
        identifier = language_identifier_from_config(
            LanguageIdentifierConfig(
                kind=LanguageIdentifierKind.GLOTLID, model_path="glotlid.bin"
            )
        )
        guess = identifier.identify("Hello there.")
        cast("MagicMock", model.predict).return_value = ((), ())
        none = identifier.identify("???")

    assert guess == LanguageGuess("eng", 0.93, "Latn")
    assert none == UNDETERMINED_GUESS


def test_unloadable_model_and_bad_config_are_refused(tmp_path: Path) -> None:
    """Test that a missing model file.

    A kind without a model path and an unknown kind fail.

    **Why this test is important:**
      - A wrong model path must fail with a coded error naming it, not a bare library
        error.

    **What it tests:**
      - the real ``fasttext-predict`` loader on a missing file raises ``AppValueError``
      - ``fasttext`` and ``glotlid`` without ``model_path``, an unknown kind, and a guess
        with an empty code or a confidence outside ``[0, 1]`` raise ``AppValueError``

    Args:
        tmp_path: A fresh directory for the test.

    """
    identifier = language_identifier_from_config(
        LanguageIdentifierConfig(
            kind=LanguageIdentifierKind.FASTTEXT, model_path=str(tmp_path / "missing.ftz")
        )
    )

    with pytest.raises(AppValueError):
        _ = identifier.identify("Hello there.")
    for config in (
        LanguageIdentifierConfig(kind=LanguageIdentifierKind.FASTTEXT),
        LanguageIdentifierConfig(kind=LanguageIdentifierKind.GLOTLID),
        LanguageIdentifierConfig(
            kind=cast("LanguageIdentifierKind", cast("object", "cld3"))
        ),
    ):
        with pytest.raises(AppValueError):
            _ = language_identifier_from_config(config)
    for code, confidence in (("", 0.5), ("en", 1.5), ("en", -0.1)):
        with pytest.raises(AppValueError):
            _ = LanguageGuess(code, confidence)


def _lingua(code: str, confidence: float) -> MagicMock:
    """Return a mocked ``lingua`` module whose detector answers ``code``.

    At ``confidence``.

    Args:
        code: The detected language code.
        confidence: Its confidence.

    Returns:
        A mocked ``lingua`` module whose detector answers ``code``.

    """
    value = MagicMock()
    cast(
        "MagicMock", cast("MagicMock", value.language).iso_code_639_1
    ).name = code.upper()
    value.value = confidence
    module = MagicMock()
    detector = cast(
        "object",
        cast(
            "MagicMock",
            cast(
                "MagicMock",
                cast("MagicMock", module.LanguageDetectorBuilder).from_all_languages,
            ).return_value,
        ).build,
    )
    cast(
        "MagicMock", detector.return_value.compute_language_confidence_values
    ).return_value = [value]
    return module


def test_short_text_goes_to_lingua_and_a_low_confidence_guess_is_und() -> None:
    """Test that short text is routed to ``lingua`` and a guess.

    Under the floor is ``und``.

    **Why this test is important:**
      - n-gram profilers guess on a few words; a query or interview answer must reach the
        backend built for short text, and a weak guess must not be reported as a language.

    **What it tests:**
      - with ``short_text_kind = lingua`` under 40 characters, ``Bonjour à tous`` is
        Lingua's ``fr`` at its confidence; a long English sentence is langdetect's ``en``
      - with ``min_confidence = 0.5``, a 0.3 guess is ``und`` at confidence 0.3
      - ``short_text_kind`` without ``short_text_chars`` and a ``min_confidence`` outside
        ``[0, 1]`` raise ``AppValueError``
    """
    config = LanguageIdentifierConfig(
        short_text_kind=LanguageIdentifierKind.LINGUA,
        short_text_chars=40,
        min_confidence=0.5,
    )
    long_text = "The quarterly report shows revenue grew in every region this year."
    with patch.dict(sys.modules, {"lingua": _lingua("fr", 0.9)}):
        identifier = language_identifier_from_config(config)
        short = identifier.identify("Bonjour à tous")
        long = identifier.identify(long_text)
    with patch.dict(sys.modules, {"lingua": _lingua("de", 0.3)}):
        weak = language_identifier_from_config(config).identify("Hallo")

    assert short == LanguageGuess("fr", 0.9)
    assert long.code == "en"
    assert weak == LanguageGuess("und", 0.3)
    for bad in (
        LanguageIdentifierConfig(short_text_kind=LanguageIdentifierKind.LINGUA),
        LanguageIdentifierConfig(min_confidence=1.5),
    ):
        with pytest.raises(AppValueError):
            _ = language_identifier_from_config(bad)
