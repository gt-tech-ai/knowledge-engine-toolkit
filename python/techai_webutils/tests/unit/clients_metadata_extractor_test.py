"""Tests for the langdetect metadata extractor + the ``metadata_extractor_from_config``.

Factory.
"""

from __future__ import annotations

import pytest

from techai_webutils.clients.metadata import (
    MetadataConfig,
    metadata_extractor_from_config,
)
from techai_webutils.clients.metadata.langdetect import (
    LangdetectMetadataExtractor,
    extract_metadata,
)
from techai_webutils.core.domain import DocumentFormat, ParsedDocument
from techai_webutils.core.interfaces.metadata import MetadataExtractor
from tests.child_python import run_python

_EXPECTED_PAGE_COUNT = 3
_EXPECTED_WORD_COUNT = 12


class TestExtractMetadata:
    """Tests for ``extract_metadata``."""

    @staticmethod
    def test_produces_all_fields() -> None:
        """Test that extraction reports format, page/word count, language, and timestamp.

        **Why this test is important:**
          - These fields feed the sidecar Bedrock filters on and the metadata callback to
            the API; a wrong count or language mislabels the document in the vector store.

        **What it tests:**
          - An English document yields format/pages/words verbatim, language 'en', and the
            injected extracted_at.
        """
        parsed = ParsedDocument(
            markdown_content=(
                "The quick brown fox jumps over the lazy dog every single morning."
            ),
            document_format=DocumentFormat.PDF,
            total_pages=3,
            word_count=12,
        )
        meta = extract_metadata(parsed, extracted_at="2026-07-11T00:00:00Z")
        assert meta.document_format == "pdf"
        assert meta.page_count == _EXPECTED_PAGE_COUNT
        assert meta.word_count == _EXPECTED_WORD_COUNT
        assert meta.language == "en"
        assert meta.extracted_at == "2026-07-11T00:00:00Z"

    @staticmethod
    def test_empty_text_language_is_undetermined() -> None:
        """Test that blank content yields language 'und' rather than raising.

        **Why this test is important:**
          - Empty/scanned-image documents must not crash extraction; 'und' is the safe
            sentinel.

        **What it tests:**
          - Whitespace-only content yields language 'und'.
        """
        parsed = ParsedDocument(
            markdown_content="   ", document_format=DocumentFormat.TXT
        )
        assert extract_metadata(parsed, extracted_at="t").language == "und"

    @staticmethod
    def test_featureless_text_language_is_undetermined() -> None:
        """Test that non-blank but featureless content.

        (no linguistic features) yields 'und'.

        **Why this test is important:**
          - Digit/punctuation-only documents make langdetect raise LangDetectException;
            extraction must swallow it and return the 'und' sentinel, not crash and fail
            the whole document.

        **What it tests:**
          - Digits-only content yields language 'und' (the exception branch, not the
            empty-string one).
        """
        parsed = ParsedDocument(
            markdown_content="1234567890 0987654321", document_format=DocumentFormat.TXT
        )
        assert extract_metadata(parsed, extracted_at="t").language == "und"


class TestMetadataExtractorFactory:
    """The ``langdetect`` MetadataExtractor backend + the config factory.

    (the metadata stage seam).
    """

    @staticmethod
    def test_factory_selects_langdetect_backend() -> None:
        """kind=langdetect builds a ``LangdetectMetadataExtractor`` satisfying the.

        ``MetadataExtractor`` seam.

        **Why this test is important:**
          - The metadata stage must be reachable through the factory so a richer extractor
            becomes a config swap, not a code edit.

        **What it tests:**
          - ``metadata_extractor_from_config`` returns a ``LangdetectMetadataExtractor``
            that is a ``MetadataExtractor`` and whose ``extract`` matches the pure
            ``extract_metadata``.
        """
        extractor = metadata_extractor_from_config(MetadataConfig())
        assert isinstance(extractor, LangdetectMetadataExtractor)
        assert isinstance(extractor, MetadataExtractor)
        parsed = ParsedDocument(
            markdown_content=(
                "The quick brown fox jumps over the lazy dog every single morning."
            ),
            document_format=DocumentFormat.PDF,
            total_pages=3,
            word_count=12,
        )
        assert extractor.extract(parsed, extracted_at="t") == extract_metadata(
            parsed, extracted_at="t"
        )

    @staticmethod
    def test_factory_rejects_unknown_kind() -> None:
        """An unknown metadata kind fails loudly with ValueError.

        (the Go NewFromConfig contract).
        """
        with pytest.raises(ValueError, match="unknown metadata extractor kind"):
            metadata_extractor_from_config(MetadataConfig(kind="bogus"))  # type: ignore[arg-type]


def test_metadata_package_imports_without_langdetect() -> None:
    """Importing ``techai_webutils.clients.metadata`` pulls no ``langdetect``.

    **Why this test is important:**
      - makes langdetect an optional ``[langdetect]`` extra; the metadata seam + factory
        must be importable with the package absent — the property a non-extracting
        deployable relies on. Run in a fresh interpreter with langdetect hard-blocked so
        the check is honest even though this test venv has it installed.

    **What it tests:**
      - A subprocess that blocks ``langdetect`` can still import the package; only when it
        *selects* the backend does the blocker bite (proving the import is deferred to
        backend selection).
    """
    script = (
        "import sys\n"
        "class _Blocker:\n"
        "    def find_spec(self, name, path=None, target=None):\n"
        "        if name == 'langdetect' or name.startswith('langdetect.'):\n"
        "            raise ImportError('blocked: ' + name)\n"
        "        return None\n"
        "sys.meta_path.insert(0, _Blocker())\n"
        "import techai_webutils.clients.metadata as m\n"
        "assert 'langdetect' not in sys.modules, 'langdetect was imported by the "
        "package'\n"
        "try:\n"
        "    m.metadata_extractor_from_config(m.MetadataConfig())\n"
        "except ImportError:\n"
        "    print('LAZY_OK')\n"
        "else:\n"
        "    raise AssertionError('expected the blocked langdetect import on "
        "backend selection')\n"
    )
    result = run_python(script)
    assert result.returncode == 0, result.stderr
    assert "LAZY_OK" in result.stdout
