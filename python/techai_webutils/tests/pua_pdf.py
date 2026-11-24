"""Build PDFs whose text sits at Private Use Area code points, named by their font.

The glyph-table and parser tests embed a TrueType font whose cmap maps PUA code points
to glyph names such as ``c_t``, the way book typesetters set ligatures and small caps.
"""

from __future__ import annotations

import importlib
import io
from typing import TYPE_CHECKING, cast

import pymupdf

if TYPE_CHECKING:
    from collections.abc import Callable

# fontTools ships no type stubs; its builders are reached through typed casts, as the
# glyph table's production code reaches fontTools.
_FontBuilder = cast(
    "Callable[..., object]", importlib.import_module("fontTools.fontBuilder").FontBuilder
)
_TTGlyphPen = cast(
    "Callable[..., object]",
    importlib.import_module("fontTools.pens.ttGlyphPen").TTGlyphPen,
)


def _call(obj: object, method: str, *args: object, **kwargs: object) -> object:
    """Call ``obj.method(*args, **kwargs)`` on an untyped fontTools object.

    Args:
        obj: The fontTools object.
        method: The method name.
        *args: Its positional arguments.
        **kwargs: Its keyword arguments.

    Returns:
        The method's result.

    """
    return cast("Callable[..., object]", getattr(obj, method))(*args, **kwargs)


def probe_font(cmap: dict[int, str]) -> bytes:
    """Build a TrueType font whose cmap maps each code point to a box glyph.

    Args:
        cmap: Code point → glyph name.

    Returns:
        The font file.

    """
    names = [".notdef", *dict.fromkeys(cmap.values())]
    pen = _TTGlyphPen(None)
    for method, point in (
        ("moveTo", (100, 0)),
        ("lineTo", (100, 700)),
        ("lineTo", (500, 700)),
        ("lineTo", (500, 0)),
    ):
        _ = _call(pen, method, point)
    _ = _call(pen, "closePath")
    box = _call(pen, "glyph")
    builder = _FontBuilder(1000, isTTF=True)
    _ = _call(builder, "setupGlyphOrder", names)
    _ = _call(builder, "setupCharacterMap", cmap)
    _ = _call(builder, "setupGlyf", dict.fromkeys(names, box))
    _ = _call(builder, "setupHorizontalMetrics", dict.fromkeys(names, (600, 100)))
    _ = _call(builder, "setupHorizontalHeader", ascent=800, descent=-200)
    _ = _call(builder, "setupNameTable", {"familyName": "Probe", "styleName": "Regular"})
    _ = _call(builder, "setupOS2")
    _ = _call(builder, "setupPost")
    buffer = io.BytesIO()
    _ = _call(builder, "save", buffer)
    return buffer.getvalue()


def pua_pdf(text: str, cmap: dict[int, str]) -> bytes:
    """Build a one-page PDF that sets ``text`` in a font embedding ``cmap``.

    Args:
        text: The page text, PUA characters included.
        cmap: The embedded font's code point → glyph name map.

    Returns:
        The PDF.

    """
    doc = pymupdf.open()
    page = doc.new_page()
    _ = page.insert_font(fontname="probe", fontbuffer=probe_font(cmap))
    _ = page.insert_text((72, 72), text, fontname="probe", fontsize=20)
    return doc.tobytes()
