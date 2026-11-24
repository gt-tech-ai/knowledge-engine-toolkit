r"""Map a PDF's Private Use Area characters to the text their glyph names spell.

Some typesetters put ligatures (``c_t``, ``T_h``), small caps and oldstyle figures at
Private Use Area (PUA) code points, so the text layer carries characters no query or
embedder understands and words such as "fact" index as "fa\ue053". The glyph's *name*
still says what it is. ``glyph_table`` builds a ``str.translate`` table for the PUA
characters a text holds:

1. each embedded font that fontTools can read (TrueType and OpenType; a bare CFF has no
   code point map and is skipped) maps the code point to its glyph name through its
   cmap;
2. a code point no font names falls back to the Adobe legacy glyph list, which named the
   Expert-set small caps and oldstyle figures at fixed PUA code points.

A name becomes text by the Adobe Glyph List rules (``agl.toUnicode``: ``c_t`` → "ct",
``uni0041`` → "A") after dropping a ``.suffix`` (``a.sc`` → "a") and a trailing
``small`` (lowercased: a small cap is a lowercase letter drawn as a capital) or
``oldstyle`` (``zerooldstyle`` → "0"). A name that still spells PUA, nothing, or
whitespace and control characters is left unmapped, and so is its character; OCR can
read those (``PageOcr.read_glyphs``). No code point is hard-coded.

fontTools and pymupdf load lazily, inside the parse child only. Without fontTools (it
ships in the ``parsing`` extra) no table is built and the text is left as parsed.
"""

from __future__ import annotations

import functools
import importlib
import io
import operator
import unicodedata
from typing import TYPE_CHECKING, Protocol, cast

from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Callable, Iterable, Mapping

logger = get_logger(__name__)

_PUA_RANGES = ((0xE000, 0xF8FF), (0xF0000, 0xFFFFD), (0x100000, 0x10FFFD))
"""The Private Use Areas: the BMP block and planes 15 and 16."""

_SMALL = "small"
"""The legacy suffix of a small-cap glyph name (``Asmall``)."""

_OLDSTYLE = "oldstyle"
"""The legacy suffix of an oldstyle-figure glyph name (``zerooldstyle``)."""

_MAX_REPLACEMENT = 4
"""The longest text one glyph stands for (``f_f_i`` is three letters)."""


class _FontDocument(Protocol):
    """The slice of a pymupdf ``Document`` the table reads fonts through."""

    @property
    def page_count(self) -> int:
        """Number of pages in the document."""
        ...

    def get_page_fonts(self, pno: int) -> list[tuple[object, ...]]:
        """List the fonts page ``pno`` uses; each entry's first item is its xref.

        Args:
            pno: The zero-based page number.

        Returns:
            One tuple per font.

        """
        ...

    def extract_font(self, xref: int) -> tuple[object, ...]:
        """Return a font's (name, ext, type, buffer).

        Args:
            xref: The font's object number.

        Returns:
            The font's name, file extension, type and embedded bytes.

        """
        ...


def is_pua(char: str) -> bool:
    """Report whether ``char`` is a Private Use Area code point.

    Args:
        char: One character.

    Returns:
        True for a PUA code point.

    """
    point = ord(char)
    return any(low <= point <= high for low, high in _PUA_RANGES)


def pua_points(text: str) -> set[int]:
    """Return the distinct PUA code points in ``text``.

    Args:
        text: Any text.

    Returns:
        The PUA code points it holds.

    """
    return {ord(c) for c in set(text) if is_pua(c)}


def name_text(name: str) -> str | None:
    """Return the text glyph ``name`` stands for, or ``None`` when it names none.

    Args:
        name: A glyph name (``c_t``, ``Asmall``, ``a.sc``, ``uniE000``).

    Returns:
        The text, or ``None`` when it fails ``plain_text`` (empty, over four
        characters, or holding PUA, whitespace or control characters).

    """
    base = name.split(".", 1)[0]
    lower = False
    if base.endswith(_SMALL) and len(base) > len(_SMALL):
        base, lower = base[: -len(_SMALL)], True
    elif base.endswith(_OLDSTYLE) and len(base) > len(_OLDSTYLE):
        base = base[: -len(_OLDSTYLE)]
    if not base:
        return None
    to_unicode = cast(
        "Callable[[str], str]", importlib.import_module("fontTools.agl").toUnicode
    )
    text = to_unicode(base)
    if lower:
        text = text.lower()
    return text if plain_text(text) else None


def plain_text(text: str) -> bool:
    """Report whether ``text`` can replace one glyph in indexed text.

    It must be 1 to 4 characters, none of them PUA, whitespace or control, which is
    what a glyph name spells and what an OCR answer for one glyph must look like.

    Args:
        text: The candidate replacement.

    Returns:
        True when ``text`` can stand for one glyph.

    """
    return 0 < len(text) <= _MAX_REPLACEMENT and all(map(_plain, text))


def _plain(char: str) -> bool:
    """Report whether ``char`` can stand in indexed text: not PUA, space or control.

    Args:
        char: One character.

    Returns:
        True for a visible, assigned, non-PUA character.

    """
    return not is_pua(char) and not unicodedata.category(char).startswith(("C", "Z"))


@functools.cache
def _legacy_names() -> dict[int, str]:
    """Return the Adobe legacy glyph list's PUA code points → glyph name.

    A code point several names share takes the first in sorted order, so the table is
    deterministic.

    Returns:
        The reverse map, PUA code points only.

    """
    legacy = cast(
        "Mapping[str, list[int]]",
        importlib.import_module("fontTools.agl").LEGACY_AGL2UV,
    )
    names: dict[int, str] = {}
    for name in sorted(legacy):
        for point in legacy[name]:
            if is_pua(chr(point)):
                _ = names.setdefault(point, name)
    return names


def font_names(buffers: Iterable[bytes]) -> list[Mapping[int, str]]:
    """Read each embedded font's cmap; a font fontTools cannot read is skipped.

    Args:
        buffers: The embedded font files.

    Returns:
        One code point → glyph name map per readable font, in order.

    """
    tt_font = cast(
        "Callable[[io.BytesIO], object]",
        importlib.import_module("fontTools.ttLib").TTFont,
    )
    best_cmap = operator.methodcaller("getBestCmap")
    cmaps: list[Mapping[int, str]] = []
    for buffer in buffers:
        try:
            font = tt_font(io.BytesIO(buffer))
            cmap = cast("Mapping[int, str] | None", best_cmap(font))
        except Exception:
            # An unreadable font (a bare CFF, a subset fontTools rejects) names no glyphs.
            logger.debug(
                "embedded font has no readable cmap", size=len(buffer), exc_info=True
            )
            continue
        if cmap:
            cmaps.append(cmap)
    return cmaps


def table_from_names(
    points: Iterable[int], fonts: Iterable[Mapping[int, str]]
) -> dict[int, str]:
    """Build the translate table for ``points`` from font cmaps, then the legacy list.

    Args:
        points: The PUA code points to map.
        fonts: Each embedded font's code point → glyph name map.

    Returns:
        Code point → replacement text, for every point a name spells.

    """
    cmaps = list(fonts)
    table: dict[int, str] = {}
    for point in points:
        names = [cmap[point] for cmap in cmaps if point in cmap]
        names.append(_legacy_names().get(point, ""))
        for name in names:
            text = name_text(name) if name else None
            if text is not None:
                table[point] = text
                break
    return table


def glyph_table(doc: object, text: str) -> dict[int, str]:
    """Build the translate table for the PUA characters of ``text`` in open PDF ``doc``.

    A text with no PUA character returns ``{}`` before any font is read, and so does a
    process without fontTools (logged as a warning): the text is then left as parsed.

    Args:
        doc: The open pymupdf document ``text`` came from.
        text: The document's extracted text.

    Returns:
        Code point → replacement text; ``str.translate`` applies it.

    """
    points = pua_points(text)
    if not points:
        return {}
    try:
        _ = importlib.import_module("fontTools.agl")
    except ImportError:
        # fontTools ships in the ``parsing`` extra; a consumer that installs markitdown
        # and pymupdf on its own may lack it. The text is kept as parsed, never lost.
        logger.warning(
            "pua glyphs left unmapped: fontTools is not installed", found=len(points)
        )
        return {}
    pdf: _FontDocument = cast("_FontDocument", doc)
    xrefs = dict.fromkeys(
        cast("int", font[0])
        for pno in range(pdf.page_count)
        for font in pdf.get_page_fonts(pno)
    )
    buffers = (cast("bytes", pdf.extract_font(xref)[3]) for xref in xrefs)
    table = table_from_names(points, font_names(buffers))
    logger.info("pua glyphs mapped", found=len(points), mapped=len(table))
    return table
