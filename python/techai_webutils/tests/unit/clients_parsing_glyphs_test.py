"""Tests for the PUA glyph-name table (``clients.parsing.isolated.glyphs``)."""

from typing import cast
from unittest.mock import Mock

import pymupdf
import pytest

from techai_webutils.clients.parsing.isolated.glyphs import (
    font_names,
    glyph_table,
    is_pua,
    name_text,
    pua_points,
    table_from_names,
)
from tests.pua_pdf import probe_font, pua_pdf

_CT = 0xE053
_A_SMALL = 0xF761
_UNNAMED = 0xE000


class TestNameText:
    """Tests for turning one glyph name into text."""

    @staticmethod
    @pytest.mark.parametrize(
        ("name", "text"),
        [
            ("c_t", "ct"),
            ("T_h", "Th"),
            ("f_f_i", "ffi"),
            ("Asmall", "a"),
            ("a.sc", "a"),
            ("zerooldstyle", "0"),
            ("uni0041", "A"),
        ],
    )
    def test_a_name_spells_its_text(name: str, text: str) -> None:
        """Test that ligature, small-cap, suffixed and uniXXXX names spell their text.

        **Why this test is important:**
          - Typesetters park ligatures and small caps at PUA code points; the glyph name
            is the only record of the letters, and a word like "fact" indexes as
            "fa" plus a PUA character unless the name is read.

        **What it tests:**
          - Each name maps to the letters it draws, small caps lowercased and the
            ``.suffix`` and ``oldstyle`` markers dropped.

        Args:
            name: The glyph name.
            text: The text it must spell.

        """
        assert name_text(name) == text

    @staticmethod
    @pytest.mark.parametrize("name", ["uniE000", "space", "small", ".notdef"])
    def test_a_name_with_no_visible_text_spells_nothing(name: str) -> None:
        """Test that a PUA, whitespace or empty name maps to nothing.

        **Why this test is important:**
          - Replacing a PUA character with another PUA character, a space or nothing
            would hide the glyph from OCR while still indexing junk.

        **What it tests:**
          - The name yields ``None``.

        Args:
            name: The glyph name.

        """
        assert name_text(name) is None


class TestTable:
    """Tests for building the translate table."""

    @staticmethod
    def test_font_names_come_before_the_legacy_list() -> None:
        """Test that a font's own name wins and the legacy list fills the rest.

        **Why this test is important:**
          - The font is the authority for what its glyph draws; the Adobe legacy list
            only covers the old Expert-set code points a font may not name.

        **What it tests:**
          - A code point the font names takes the font's name; a legacy Expert-set code
            point no font names takes the legacy name; an unnamed point is left out.
        """
        table = table_from_names(
            [_CT, _A_SMALL, _UNNAMED], [{_CT: "c_t", _UNNAMED: "uniE000"}]
        )
        assert table == {_CT: "ct", _A_SMALL: "a"}

    @staticmethod
    def test_an_unreadable_font_is_skipped() -> None:
        """Test that a font fontTools cannot read names no glyphs.

        **Why this test is important:**
          - Embedded CID CFF fonts carry no cmap fontTools can open; one must not fail
            the whole document's table.

        **What it tests:**
          - Bytes that are no font yield no cmap, beside a readable font's cmap.
        """
        cmaps = font_names([b"not a font", probe_font({_CT: "c_t"})])
        assert [dict(cmap) for cmap in cmaps] == [{_CT: "c_t"}]

    @staticmethod
    def test_glyph_table_reads_the_embedded_fonts() -> None:
        """Test that the table maps a PDF's PUA characters through its embedded font.

        **Why this test is important:**
          - This is the path a parse takes: the text layer holds PUA characters and the
            embedded font names them.

        **What it tests:**
          - The table maps the ligature code point to "ct".
        """
        doc = pymupdf.open(
            stream=pua_pdf("fa\ue053 x", {0x20: "space", 0x78: "x", _CT: "c_t"}),
            filetype="pdf",
        )
        text = cast("str", doc[0].get_text())
        assert glyph_table(doc, text) == {_CT: "ct"}

    @staticmethod
    def test_text_without_pua_reads_no_fonts() -> None:
        """Test that text with no PUA character yields an empty table.

        **Why this test is important:**
          - Nearly every PDF has no PUA text; reading its fonts would cost every parse.

        **What it tests:**
          - The table is empty, and the document's fonts are never listed.
        """
        get_page_fonts = Mock()
        doc = Mock(spec=["page_count", "get_page_fonts"], get_page_fonts=get_page_fonts)
        assert glyph_table(doc, "plain text") == {}
        get_page_fonts.assert_not_called()

    @staticmethod
    def test_pua_points_finds_every_plane() -> None:
        """Test that PUA detection covers the BMP block and planes 15 and 16.

        **Why this test is important:**
          - A supplementary-plane PUA character missed here would stay in the index.

        **What it tests:**
          - The BMP, plane-15 and plane-16 points are found; ASCII is not.
        """
        assert pua_points("a\ue000\U000f0000\U00100000") == {
            0xE000,
            0xF0000,
            0x100000,
        }
        assert not is_pua("a")
