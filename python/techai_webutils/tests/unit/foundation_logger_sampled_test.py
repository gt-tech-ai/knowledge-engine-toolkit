"""Unit tests for ``SampledLogger``: success lines sampled, problem lines always kept."""

from __future__ import annotations

from itertools import cycle
from typing import NoReturn, cast
from unittest.mock import MagicMock, create_autospec

import pytest

from techai_webutils.core.errors import InvalidInputError
from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.foundation.logger import SampledLogger

_EXPECTED_CALL_COUNT = 2
_EXPECTED_CALL_COUNT_3 = 3


def _fail() -> NoReturn:
    """Raise the error whose handler logs ``exception``.

    Raises:
        RuntimeError: Boom.

    """
    msg = "boom"
    raise RuntimeError(msg)


def test_sampled_logger_keeps_rate_of_success_lines_and_every_problem_line() -> None:
    """Test that debug and info lines are sampled at the rate and the rest always pass.

    **Why this test is important:**
      - A log volume budget must cut only success lines: a dropped warning or error would
        hide the failure the logs exist to show, and a bound logger must keep sampling.

    **What it tests:**
      - At rate 0.5 with draws 0.2, 0.7, 0.4, 0.9, two of four info lines and two of four
        debug lines reach the inner logger; every warning, error and exception does.
      - A logger from ``bind`` binds on the inner logger and samples at the same rate.
      - Rate 1.0 keeps every line; a rate outside 0.0-1.0 is ``InvalidInputError``.
    """
    inner = cast("object", create_autospec(Logger, instance=True))
    draws = cycle([0.2, 0.7, 0.4, 0.9])
    log = SampledLogger(cast("Logger", inner), 0.5, draw=lambda: next(draws))

    for i in range(4):
        log.info("ok", n=i)
    for i in range(4):
        log.debug("detail", n=i)
    log.warning("slow")
    log.error("failed")
    try:
        _fail()
    except RuntimeError:
        log.exception("crashed")

    assert [
        cast("dict[str, object]", c.kwargs)["n"]
        for c in cast("list[object]", inner.info.call_args_list)
    ] == [0, 2]
    assert [
        cast("dict[str, object]", c.kwargs)["n"]
        for c in cast("list[object]", inner.debug.call_args_list)
    ] == [0, 2]
    assert (
        cast("object", inner.warning.call_count),
        cast("object", inner.error.call_count),
    ) == (1, 1)
    assert cast("object", inner.exception.call_count) == 1

    bound_inner = cast("object", create_autospec(Logger, instance=True))
    cast("MagicMock", inner.bind).return_value = bound_inner
    bound = log.bind(document_id="d")
    for _ in range(4):
        bound.info("ok")
    cast("MagicMock", inner.bind.assert_called_once_with)(document_id="d")
    assert cast("object", bound_inner.info.call_count) == _EXPECTED_CALL_COUNT

    every = cast("object", create_autospec(Logger, instance=True))
    full = SampledLogger(cast("Logger", every), 1.0, draw=lambda: 0.99)
    for _ in range(3):
        full.info("ok")
    assert cast("object", every.info.call_count) == _EXPECTED_CALL_COUNT_3
    with pytest.raises(InvalidInputError):
        _ = SampledLogger(cast("Logger", inner), 1.5)
