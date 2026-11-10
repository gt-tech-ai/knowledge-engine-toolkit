"""Predicate-based exception observers: decide an error's fate in ``__exit__``.

Code that catches a broad ``Exception``, does something, then re-raises or absorbs it
cannot satisfy both ruff's DOC502 (a re-raised ``Exception`` never counts as explicitly
raised) and pydoclint's DOC501 (which requires it documented). These context managers
move that decision out of the caller, so the caller holds no ``raise`` statement:

- ``Absorb`` swallows only the errors one of its predicates matches and records them.
- ``OnError`` and ``AsyncOnError`` run a callback on every error and let it propagate.
- ``OnFailure`` runs a callback on a failure (an ``Exception``) and lets it propagate;
  a cancellation or interrupt passes without it.

They compose: ``with Absorb(pred): async with AsyncOnError(cleanup): ...`` runs
``cleanup`` on every error, then absorbs only the errors ``pred`` matches.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Self, final

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable
    from types import TracebackType


@final
class Absorb:
    """Absorb an error from the ``with`` body when a predicate matches it.

    The predicates run in order, each at most once, and the first that returns
    ``True`` absorbs the error. Anything no predicate matches, cancellation included,
    propagates unchanged: the same object, traceback and context. Usable inside
    ``async def``, since entering and leaving it never awaits.
    """

    error: BaseException | None
    """The absorbed error; ``None`` when the body succeeded or the error propagated."""

    def __init__(self, *matches: Callable[[BaseException], bool]) -> None:
        """Bind the predicates that decide which errors are absorbed.

        Args:
            *matches: Predicates over the raised exception, tried in order.

        """
        self._matches = matches
        self.error = None

    def __enter__(self) -> Self:
        """Start observing the body.

        Returns:
            This context manager, so the caller can read ``error`` afterwards.

        """
        self.error = None
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> bool:
        """Absorb ``exc`` when a predicate matches it, else let it propagate.

        Args:
            exc_type: The active exception's type, or ``None`` on success.
            exc: The active exception, or ``None`` on success.
            tb: The active exception's traceback, or ``None`` on success.

        Returns:
            ``True`` when the error was absorbed, ``False`` to let it propagate.

        """
        del exc_type, tb
        if exc is None:
            return False
        if any(match(exc) for match in self._matches):
            self.error = exc
            return True
        return False


@final
class OnError:
    """Call a callback once when the ``with`` body raises, then let the error propagate.

    The callback is not called on success. If the callback raises, its exception
    propagates instead, with the body's error as its ``__context__``.
    """

    def __init__(self, callback: Callable[[BaseException], None]) -> None:
        """Bind the callback that observes the body's error.

        Args:
            callback: Called with the active exception, once, when the body raises.

        """
        self._callback = callback

    def __enter__(self) -> Self:
        """Start observing the body.

        Returns:
            This context manager.

        """
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        """Call the callback with ``exc`` when there is one; never absorb.

        Args:
            exc_type: The active exception's type, or ``None`` on success.
            exc: The active exception, or ``None`` on success.
            tb: The active exception's traceback, or ``None`` on success.

        """
        del exc_type, tb
        if exc is not None:
            self._callback(exc)


@final
class AsyncOnError:
    """Await a callback once when the ``async with`` body raises; let the error propagate.

    The callback is not awaited on success. If it raises, its exception propagates
    instead, with the body's error as its ``__context__``.
    """

    def __init__(self, callback: Callable[[BaseException], Awaitable[None]]) -> None:
        """Bind the coroutine callback that observes the body's error.

        Args:
            callback: Awaited with the active exception, once, when the body raises.

        """
        self._callback = callback

    async def __aenter__(self) -> Self:
        """Start observing the body.

        Returns:
            This context manager.

        """
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        """Await the callback with ``exc`` when there is one; never absorb.

        Args:
            exc_type: The active exception's type, or ``None`` on success.
            exc: The active exception, or ``None`` on success.
            tb: The active exception's traceback, or ``None`` on success.

        """
        del exc_type, tb
        if exc is not None:
            await self._callback(exc)


@final
class OnFailure:
    """Call a callback once when the ``with`` body fails; let the failure propagate.

    A failure is an ``Exception``. A cancellation or interrupt (any other
    ``BaseException``) propagates without the callback, so an abandoned stream or a
    shutdown is never recorded as an error. If the callback raises, its exception
    propagates instead, with the body's failure as its ``__context__``.
    """

    def __init__(self, callback: Callable[[Exception], None]) -> None:
        """Bind the callback that observes the body's failure.

        Args:
            callback: Called with the failure, once, when the body raises an
                ``Exception``.

        """
        self._callback = callback

    def __enter__(self) -> Self:
        """Start observing the body.

        Returns:
            This context manager.

        """
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        """Call the callback with ``exc`` when it is a failure; never absorb.

        Args:
            exc_type: The active exception's type, or ``None`` on success.
            exc: The active exception, or ``None`` on success.
            tb: The active exception's traceback, or ``None`` on success.

        """
        del exc_type, tb
        if isinstance(exc, Exception):
            self._callback(exc)
