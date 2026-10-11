"""Unit tests for the coded errors that also subclass a builtin exception.

Production code raises a coded ``AppError`` instead of a bare builtin, but callers written against
the builtin (``except ValueError``) must keep working, so each of these classes is both.
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
def test_coded_error_is_also_its_builtin(
    cls: Callable[[str], AppError],
    app_base: type[AppError],
    builtin: type[Exception],
    code: ErrorCode,
) -> None:
    """Test that each dual-base error carries its code and is caught as its builtin.

    **Why this test is important:**
      - Swapping a bare ``raise ValueError`` for a coded error must not break any caller that
        catches the builtin; the code is what lets the transport edge map the status.

    **What it tests:**
      - an instance is an ``AppError``, its coded base and its builtin
      - ``code`` and ``message`` are set, and ``str()`` is the message
      - ``except <builtin>`` catches it when raised
    """
    err = cls("boom")

    assert isinstance(err, AppError)
    assert isinstance(err, app_base)
    assert isinstance(err, builtin)
    assert err.code is code
    assert err.message == "boom"
    assert str(err) == "boom"
    with pytest.raises(builtin, match="boom"):
        raise err
