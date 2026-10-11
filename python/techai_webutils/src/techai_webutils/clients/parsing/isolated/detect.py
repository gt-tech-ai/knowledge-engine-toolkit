"""Document format detection by magic bytes, declared format, and extension.

Precedence (magic-first, deliberately stronger than the plan's declared-first note): the
*bytes* are ground truth, so conclusive magic wins over a possibly-wrong extension or an
untrusted declared type ("handle mismatched extensions" + defence against mislabeled
uploads). ZIP (OOXML) is disambiguated by its archive parts; magic-less text formats fall
back to the declared format, then the filename extension.
"""

from __future__ import annotations

import io
import zipfile

from techai_webutils.core.domain import DocumentFormat

# Extension / declared-token aliases -> canonical format.
_TOKEN_MAP = {
    "pdf": DocumentFormat.PDF,
    "application/pdf": DocumentFormat.PDF,
    "docx": DocumentFormat.DOCX,
    "pptx": DocumentFormat.PPTX,
    "xlsx": DocumentFormat.XLSX,
    "xls": DocumentFormat.XLS,
    "md": DocumentFormat.MD,
    "markdown": DocumentFormat.MD,
    "text/markdown": DocumentFormat.MD,
    "txt": DocumentFormat.TXT,
    "text": DocumentFormat.TXT,
    "text/plain": DocumentFormat.TXT,
    "html": DocumentFormat.HTML,
    "htm": DocumentFormat.HTML,
    "text/html": DocumentFormat.HTML,
    "csv": DocumentFormat.CSV,
    "text/csv": DocumentFormat.CSV,
}
"""Maps an extension or declared-type token (lowercased) to its canonical ``DocumentFormat``."""

# OOXML part-prefix -> format (all share the ZIP magic ``PK\x03\x04``).
_OOXML_PARTS = (
    ("word/", DocumentFormat.DOCX),
    ("ppt/", DocumentFormat.PPTX),
    ("xl/", DocumentFormat.XLSX),
)
"""ZIP archive part-prefix → OOXML format, disambiguating the three formats sharing the ZIP magic."""

_PDF_MAGIC = b"%PDF-"
"""Leading magic bytes of a PDF file."""
_ZIP_MAGIC = b"PK\x03\x04"
"""Leading magic bytes of a ZIP archive (the OOXML container)."""
_OLE2_MAGIC = b"\xd0\xcf\x11\xe0"  # legacy Office (.xls / .doc)
"""Leading magic bytes of an OLE2 compound file (legacy Office .xls / .doc)."""


def _normalize_token(token: str) -> DocumentFormat:
    """Map a declared type or extension token to a canonical format (UNKNOWN if unmapped)."""
    return _TOKEN_MAP.get(token.strip().lower().lstrip("."), DocumentFormat.UNKNOWN)


def _ooxml_kind(content: bytes) -> DocumentFormat:
    """Disambiguate a ZIP container into DOCX/PPTX/XLSX by inspecting its parts."""
    try:
        with zipfile.ZipFile(io.BytesIO(content)) as archive:
            names = archive.namelist()
    except (zipfile.BadZipFile, OSError):
        return DocumentFormat.UNKNOWN
    for prefix, fmt in _OOXML_PARTS:
        if any(name.startswith(prefix) for name in names):
            return fmt
    return DocumentFormat.UNKNOWN


def _detect_by_magic(content: bytes) -> DocumentFormat:
    """Detect a format from leading magic bytes; UNKNOWN when there is no conclusive signature."""
    if content.startswith(_PDF_MAGIC):
        return DocumentFormat.PDF
    if content.startswith(_ZIP_MAGIC):
        return _ooxml_kind(content)
    if content.startswith(_OLE2_MAGIC):
        # OLE2 is the container for all legacy Office formats (.xls/.doc/.ppt), but only
        # legacy Excel (.xls) is in the supported set — legacy Word/PowerPoint aren't and
        # MarkItDown would reject them anyway — so we deliberately resolve OLE2 to XLS
        # rather than parsing the compound-file streams to disambiguate.
        return DocumentFormat.XLS
    return DocumentFormat.UNKNOWN


def _detect_by_extension(filename: str) -> DocumentFormat:
    """Detect a format from the filename extension (UNKNOWN when absent/unmapped)."""
    _, dot, ext = filename.rpartition(".")
    if not dot:
        return DocumentFormat.UNKNOWN
    return _normalize_token(ext)


def detect_format(declared: str, filename: str, content: bytes) -> DocumentFormat:
    """Resolve a document's format from its bytes, declared type, and filename.

    Order: conclusive magic bytes win; otherwise the declared (event) format; otherwise the
    filename extension; UNKNOWN if nothing resolves.
    """
    magic = _detect_by_magic(content)
    if magic is not DocumentFormat.UNKNOWN:
        return magic
    if declared:
        declared_fmt = _normalize_token(declared)
        if declared_fmt is not DocumentFormat.UNKNOWN:
            return declared_fmt
    return _detect_by_extension(filename)
