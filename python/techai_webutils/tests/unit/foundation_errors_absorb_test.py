"""Unit tests for the predicate-based exception observers in ``foundation.errors.absorb``.

``Absorb`` swallows only the errors a predicate matches; ``OnError`` and ``AsyncOnError``
observe every error and let it propagate; ``OnFailure`` observes only failures. Callers
use them so their own code holds no ``raise``. The tests run real exceptions through the
context managers; no doubles are needed.
"""

import asyncio
from collections.abc import Callable
from concurrent.futures import Future

import pytest

from techai_webutils.foundation.errors import Absorb, AsyncOnError, OnError, OnFailure

_ANSWER = 42
"""The value the success-path body computes."""


class _MatchedError(Exception):
    """The error the tests' predicates match."""


class _OtherError(Exception):
    """An error no predicate matches."""


def _is_matched(exc: BaseException) -> bool:
    """Report whether ``exc`` is a ``_MatchedError``.

    Args:
        exc: The raised exception.

    Returns:
        True for a ``_MatchedError``.

    """
    return isinstance(exc, _MatchedError)


def _raise(exc: BaseException) -> None:
    """Fail with ``exc`` the way a dependency would, from a library frame.

    A completed ``Future`` re-raises its exception from ``result()`` in the caller's
    thread state, so the very object propagates and Python chains it to any error
    being handled, with no ``raise`` statement here to document.

    Args:
        exc: The exception to fail with.

    """
    failed: Future[None] = Future()
    failed.set_exception(exc)
    failed.result()


async def _cancelled() -> None:
    """Yield to the loop once, then fail with ``asyncio.CancelledError``."""
    await asyncio.sleep(0)
    _raise(asyncio.CancelledError())


def _recording(
    seen: list[str], name: str, *, result: bool
) -> Callable[[BaseException], bool]:
    """Return a predicate that appends ``name`` to ``seen`` and answers ``result``.

    Args:
        seen: Where each call records ``name``.
        name: The predicate's label.
        result: What the predicate answers.

    Returns:
        The recording predicate.

    """

    def predicate(exc: BaseException) -> bool:
        del exc
        seen.append(name)
        return result

    return predicate


def test_absorb_records_and_swallows_a_matching_error() -> None:
    """Test that a matching error is absorbed and kept on ``error``.

    **Why this test is important:**
      - Absorbing is the reason ``Absorb`` exists: a fail-open caller relies on the
        block ending normally and on the error being there to log or count.

    **What it tests:**
      - the ``with`` block exits without raising
      - ``error`` is the very exception the body raised
    """
    boom = _MatchedError("boom")
    absorb = Absorb(_is_matched)

    with absorb:
        _raise(boom)

    assert absorb.error is boom


def test_absorb_propagates_a_non_matching_error_unchanged() -> None:
    """Test that an error no predicate matches leaves as the same object and traceback.

    **Why this test is important:**
      - A caller must see the original failure, not a copy or a wrapper, or its own
        ``except`` clauses and the traceback in the logs would lie about the cause.

    **What it tests:**
      - the propagated exception is the identical object
      - its traceback still runs from this test through the failing call
      - ``error`` stays ``None``
    """
    boom = _OtherError("boom")
    absorb = Absorb(_is_matched)

    with pytest.raises(_OtherError) as caught, absorb:
        _raise(boom)

    assert caught.value is boom
    frames = [entry.name for entry in caught.traceback]
    assert frames[0] == "test_absorb_propagates_a_non_matching_error_unchanged"
    assert "_raise" in frames
    assert absorb.error is None


def test_absorb_keeps_the_body_value_on_success() -> None:
    """Test that a body that succeeds keeps its result and records no error.

    **Why this test is important:**
      - The happy path must be untouched; a context manager that disturbed it would
        change behaviour for every caller that wraps working code.

    **What it tests:**
      - ``error`` is ``None``
      - a value computed in the body is available afterwards
    """
    absorb = Absorb(_is_matched)
    value = 0

    with absorb:
        value = 6 * 7

    assert absorb.error is None
    assert value == _ANSWER


@pytest.mark.asyncio
async def test_absorb_lets_cancellation_through() -> None:
    """Test that ``asyncio.CancelledError`` propagates when no predicate matches it.

    **Why this test is important:**
      - Swallowing cancellation would leave a cancelled task running and break shutdown;
        ``Absorb`` must absorb only what a predicate names.

    **What it tests:**
      - a ``CancelledError`` raised inside ``Absorb(...)`` inside ``async def`` escapes
      - ``error`` stays ``None``
    """
    absorb = Absorb(_is_matched)

    with pytest.raises(asyncio.CancelledError), absorb:
        await _cancelled()

    assert absorb.error is None


def test_absorb_tries_predicates_in_order_and_stops_at_the_first_match() -> None:
    """Test that predicates run in order, each at most once, until one matches.

    **Why this test is important:**
      - A predicate can be costly or have side effects (counting, logging); callers
        rely on the documented order and on later predicates not running.

    **What it tests:**
      - the predicates before the match run once each, in order
      - the predicates after the match never run
    """
    seen: list[str] = []
    absorb = Absorb(
        _recording(seen, "first", result=False),
        _recording(seen, "second", result=True),
        _recording(seen, "third", result=True),
    )

    with absorb:
        _raise(_OtherError("boom"))

    assert seen == ["first", "second"]


def test_on_error_calls_back_once_on_error_and_never_on_success() -> None:
    """Test that ``OnError`` sees each error once, lets it propagate, and ignores success.

    **Why this test is important:**
      - ``OnError`` is for cleanup and accounting; a missed or doubled call corrupts
        counters, and swallowing the error would hide the failure from the caller.

    **What it tests:**
      - one callback call with the raised exception, which still propagates
      - no callback call when the body succeeds
    """
    seen: list[BaseException] = []
    boom = _OtherError("boom")

    with pytest.raises(_OtherError) as caught, OnError(seen.append):
        _raise(boom)
    with OnError(seen.append):
        pass

    assert caught.value is boom
    assert seen == [boom]


def test_on_failure_sees_a_failure_but_not_a_cancellation() -> None:
    """Test that ``OnFailure`` calls back on an ``Exception`` only, and never absorbs.

    **Why this test is important:**
      - Tracing, metrics and access logs record failures through it; a cancelled stream
        or a shutdown recorded as an error would raise false alarms, and an absorbed
        failure would hide it from the caller.

    **What it tests:**
      - one callback call with a raised ``Exception``, which still propagates
      - no callback call for a ``CancelledError``, which still propagates
      - no callback call when the body succeeds
    """
    seen: list[Exception] = []
    boom = _OtherError("boom")
    cancelled = asyncio.CancelledError()

    with pytest.raises(_OtherError) as caught, OnFailure(seen.append):
        _raise(boom)
    with pytest.raises(asyncio.CancelledError) as stopped, OnFailure(seen.append):
        _raise(cancelled)
    with OnFailure(seen.append):
        pass

    assert caught.value is boom
    assert stopped.value is cancelled
    assert seen == [boom]


@pytest.mark.asyncio
async def test_async_on_error_awaits_the_callback_once_on_error_only() -> None:
    """Test that ``AsyncOnError`` awaits its callback once per error and not on success.

    **Why this test is important:**
      - Async cleanup (closing a stream, releasing a lease) must run exactly once and
        must not stop the error from reaching the caller.

    **What it tests:**
      - one awaited callback call with the raised exception, which still propagates
      - no callback call when the body succeeds
    """
    seen: list[BaseException] = []
    boom = _OtherError("boom")

    async def record(exc: BaseException) -> None:
        """Record the error.

        Args:
            exc: The body's error.

        """
        await asyncio.sleep(0)
        seen.append(exc)

    with pytest.raises(_OtherError) as caught:
        async with AsyncOnError(record):
            _raise(boom)
    async with AsyncOnError(record):
        await asyncio.sleep(0)

    assert caught.value is boom
    assert seen == [boom]


@pytest.mark.asyncio
async def test_a_raising_callback_propagates_its_own_error_chained() -> None:
    """Test that a callback's own failure wins and keeps the original as its context.

    **Why this test is important:**
      - A cleanup that fails must not be silent, and the first failure must stay
        reachable for diagnosis through the exception chain.

    **What it tests:**
      - ``OnError`` and ``AsyncOnError`` both raise the callback's error
      - its ``__context__`` is the body's original error
    """
    original = _OtherError("body")

    def fail(exc: BaseException) -> None:
        """Fail the callback itself.

        Args:
            exc: The body's error.

        """
        del exc
        _raise(_MatchedError("callback"))

    async def fail_async(exc: BaseException) -> None:
        """Fail the async callback itself.

        Args:
            exc: The body's error.

        """
        await asyncio.sleep(0)
        fail(exc)

    with pytest.raises(_MatchedError) as sync_caught, OnError(fail):
        _raise(original)
    with pytest.raises(_MatchedError) as async_caught:
        async with AsyncOnError(fail_async):
            _raise(original)

    assert sync_caught.value.__context__ is original
    assert async_caught.value.__context__ is original


@pytest.mark.asyncio
async def test_absorb_around_async_on_error_cleans_up_then_absorbs_matches() -> None:
    """Test the composition ``with Absorb(pred): async with AsyncOnError(cleanup)``.

    **Why this test is important:**
      - This is the shape a fail-open boundary uses: clean up after every failure, then
        decide which failures to swallow, with no ``raise`` in the caller.

    **What it tests:**
      - cleanup runs for a matched error, which is then absorbed and recorded
      - cleanup runs for an unmatched error, which then propagates
    """
    cleaned: list[BaseException] = []

    async def cleanup(exc: BaseException) -> None:
        """Record the cleanup.

        Args:
            exc: The body's error.

        """
        await asyncio.sleep(0)
        cleaned.append(exc)

    matched, other = _MatchedError("matched"), _OtherError("other")
    absorb = Absorb(_is_matched)
    with absorb:
        async with AsyncOnError(cleanup):
            _raise(matched)
    with pytest.raises(_OtherError), Absorb(_is_matched):
        async with AsyncOnError(cleanup):
            _raise(other)

    assert absorb.error is matched
    assert cleaned == [matched, other]
