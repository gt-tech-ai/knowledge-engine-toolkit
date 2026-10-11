"""Tests for document format detection (magic-bytes / declared / extension precedence)."""

import io
import zipfile

from techai_webutils.clients.parsing.isolated.detect import detect_format
from techai_webutils.core.domain import DocumentFormat


def _ooxml(kind_dir: str) -> bytes:
    """Build a minimal OOXML (ZIP) container whose part prefix identifies the kind."""
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as archive:
        archive.writestr("[Content_Types].xml", "<Types/>")
        archive.writestr(f"{kind_dir}/document.xml", "<x/>")
    return buf.getvalue()


class TestDetectFormat:
    """Tests for ``detect_format``."""

    def test_pdf_magic_wins_over_wrong_extension(self) -> None:
        """Test that PDF magic bytes override a mismatched .txt extension.

        **Why this test is important:**
          - "Handle mismatched extensions" is a stated requirement and a security posture: a file
            mislabeled by name (or a poisoned upload) must be parsed by what it *is*, not its name.

        **What it tests:**
          - Content starting with %PDF- named ``notes.txt`` detects as PDF.
        """
        assert (
            detect_format("", "notes.txt", b"%PDF-1.7\n1 0 obj\n") is DocumentFormat.PDF
        )

    def test_ooxml_zip_disambiguated_by_content(self) -> None:
        """Test that a ZIP (PK) container is disambiguated into DOCX/PPTX/XLSX by its parts.

        **Why this test is important:**
          - DOCX/PPTX/XLSX share the same ZIP magic; without inspecting the archive parts they are
            indistinguishable and would route to the wrong parser.

        **What it tests:**
          - word/ -> DOCX, ppt/ -> PPTX, xl/ -> XLSX.
        """
        assert detect_format("", "a.bin", _ooxml("word")) is DocumentFormat.DOCX
        assert detect_format("", "a.bin", _ooxml("ppt")) is DocumentFormat.PPTX
        assert detect_format("", "a.bin", _ooxml("xl")) is DocumentFormat.XLSX

    def test_text_formats_fall_back_to_extension(self) -> None:
        """Test that magic-less text formats are resolved by extension.

        **Why this test is important:**
          - TXT/MD/CSV/HTML have no magic bytes; extension is the only signal and must be honoured.

        **What it tests:**
          - .md -> MD, .csv -> CSV, .html -> HTML.
        """
        assert detect_format("", "readme.md", b"# hello") is DocumentFormat.MD
        assert detect_format("", "data.csv", b"a,b\n1,2") is DocumentFormat.CSV
        assert detect_format("", "page.html", b"<html></html>") is DocumentFormat.HTML

    def test_declared_format_used_when_no_magic_or_extension(self) -> None:
        """Test that the declared (event) format is used when name + bytes give no signal.

        **Why this test is important:**
          - Uploads may arrive with no extension; the upload-time detected format is the fallback
            that keeps such documents parseable.

        **What it tests:**
          - declared="csv" with an extension-less name and magic-less bytes detects as CSV.
        """
        assert detect_format("csv", "noext", b"a,b") is DocumentFormat.CSV

    def test_ole2_magic_detects_legacy_xls(self) -> None:
        """Test that the OLE2 magic signature detects a legacy .xls document.

        **Why this test is important:**
          - Legacy Excel is a distinct binary container (OLE2, not ZIP); missing it would route
            .xls documents to the wrong parser.

        **What it tests:**
          - Content starting with the OLE2 magic detects as XLS.
        """
        assert detect_format("", "a.bin", b"\xd0\xcf\x11\xe0rest") is DocumentFormat.XLS

    def test_bad_zip_is_unknown(self) -> None:
        """Test that ZIP magic with a corrupt archive body falls through to UNKNOWN.

        **Why this test is important:**
          - A truncated/corrupt OOXML must not crash detection; it should degrade to UNKNOWN so the
            document is failed cleanly.

        **What it tests:**
          - PK magic followed by non-archive bytes yields UNKNOWN.
        """
        assert detect_format("", "a.bin", b"PK\x03\x04garbage") is DocumentFormat.UNKNOWN

    def test_unknown_when_no_signal(self) -> None:
        """Test that a document with no usable signal is UNKNOWN (not a wrong guess).

        **Why this test is important:**
          - An unknown format must be surfaced (and later failed as permanent), not silently
            mis-parsed as text.

        **What it tests:**
          - No declared format, no extension, non-magic bytes -> UNKNOWN.
        """
        assert detect_format("", "mystery", b"\x00\x01\x02") is DocumentFormat.UNKNOWN

    def test_valid_zip_without_ooxml_part_is_unknown(self) -> None:
        """Test that a well-formed ZIP that is not an OOXML document resolves to UNKNOWN.

        **Why this test is important:**
          - Not every ZIP is Office; a plain archive (no word//ppt//xl/ part) must not be
            mis-detected as DOCX/PPTX/XLSX and routed to a converter that would fail it.

        **What it tests:**
          - A valid ZIP whose only entry is under a non-OOXML prefix yields UNKNOWN.
        """
        buf = io.BytesIO()
        with zipfile.ZipFile(buf, "w") as archive:
            archive.writestr("random/note.txt", "not office")
        assert detect_format("", "a.bin", buf.getvalue()) is DocumentFormat.UNKNOWN
