"""Tests for the DTO leaf rules the generated Python validators compose."""

from __future__ import annotations

import pytest

from techai_webutils.core.errors import ErrorCode, InvalidInputError
from techai_webutils.foundation import dtovalidate


@pytest.mark.parametrize(
    ("check", "message"),
    [
        (
            lambda: dtovalidate.min_max_len("query", "", 1, 0),
            "query: length 0 not in [1, 0]",
        ),
        (
            lambda: dtovalidate.min_max_len("q", "héllo", 0, 4),
            "q: length 5 not in [0, 4]",
        ),
        (lambda: dtovalidate.max_bytes("q", "é", 1), "q: 2 bytes exceeds max 1"),
        (
            lambda: dtovalidate.max_items("passages", 3, 2),
            "passages: 3 items exceeds max 2",
        ),
        (
            lambda: dtovalidate.int_range("top_n", 0, 1, 100),
            "top_n: 0 out of range [1, 100]",
        ),
        (lambda: dtovalidate.int_range("n", 5, None, 4), "n: 5 out of range [-inf, 4]"),
        (lambda: dtovalidate.uuid("org_id", "nope"), "org_id: must be a valid UUID"),
    ],
)
def test_leaf_rules_refuse_out_of_bounds_values_as_invalid_input(
    check: object, message: str
) -> None:
    """Test that each leaf rule refuses a violating value with a coded, field-named error.

    **Why this test is important:**
      - The generated validators are a request DTO's only server-side bound check, and the
        transport maps their error code to ``INVALID_ARGUMENT``; an uncoded error would
        surface as an internal fault, and a vague message hides which field was wrong.

    **What it tests:**
      - Length counts code points (``héllo`` is 5), the byte cap counts UTF-8 bytes, a
        missing bound (``None``, or a 0 maximum length) is no bound, and each failure is an
        ``InvalidInputError`` (``INVALID_INPUT``) with the Go library's message.
    """
    assert callable(check)
    with pytest.raises(InvalidInputError) as excinfo:
        check()
    assert excinfo.value.code == ErrorCode.INVALID_INPUT
    assert str(excinfo.value) == message


def test_leaf_rules_accept_values_within_bounds() -> None:
    """Test that the leaf rules pass in-bound values, including the bounds themselves.

    **Why this test is important:**
      - An off-by-one at a bound would refuse a request the proto contract allows.

    **What it tests:**
      - Exactly the minimum and maximum length, items and range values, a 0 maximum length
        (unbounded) and a valid UUID all pass.
    """
    dtovalidate.min_max_len("q", "a", 1, 1)
    dtovalidate.min_max_len("q", "a" * 10_000, 1, 0)
    dtovalidate.max_bytes("q", "ab", 2)
    dtovalidate.max_items("p", 2, 2)
    dtovalidate.int_range("n", 100, 1, 100)
    dtovalidate.int_range("n", -5, None, None)
    dtovalidate.uuid("id", "123e4567-e89b-12d3-a456-426614174000")
