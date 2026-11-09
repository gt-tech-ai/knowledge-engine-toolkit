"""Unit tests for the coded errors named after the builtin each one replaces.

Production code raises a coded ``AppError`` instead of a bare builtin. These classes are
coded only: a caller catches them by their coded base, never as the builtin.
"""

from collections.abc import Callable

import pytest

from techai_webutils.core.errors import (
    AppError,
    AppFileNotFoundError,
    AppRuntimeError,
    AppTypeError,
    AppValueError,
    ErrorCode,
    InternalError,
    InvalidInputError,
    NotFoundError,
)


@pytest.mark.parametrize(
    ("cls", "app_base", "builtin", "code"),
    [
        (AppValueError, InvalidInputError, ValueError, ErrorCode.INVALID_INPUT),
        (AppTypeError, InvalidInputError, TypeError, ErrorCode.INVALID_INPUT),
        (AppRuntimeError, InternalError, RuntimeError, ErrorCode.INTERNAL),
        (AppFileNotFoundError, NotFoundError, FileNotFoundError, ErrorCode.NOT_FOUND),
    ],
)
def test_coded_error_is_not_its_builtin(
    cls: Callable[[str], AppError],
    app_base: type[AppError],
    builtin: type[Exception],
    code: ErrorCode,
) -> None:
    """Test that each coded error carries its code and is caught by its coded base only.

    **Why this test is important:**
      - The code is what lets the transport edge map the status; a builtin base would let
        a bare ``except ValueError`` swallow a coded failure without seeing its code.

    **What it tests:**
      - an instance is an ``AppError`` and its coded base, and not its builtin
      - ``code`` and ``message`` are set, and ``str()`` is the message
      - ``except <coded base>`` catches it when raised
    """
    err = cls("boom")

    assert isinstance(err, AppError)
    assert isinstance(err, app_base)
    assert not isinstance(err, builtin)
    assert err.code is code
    assert err.message == "boom"
    assert str(err) == "boom"
    with pytest.raises(app_base, match="boom"):
        raise err
