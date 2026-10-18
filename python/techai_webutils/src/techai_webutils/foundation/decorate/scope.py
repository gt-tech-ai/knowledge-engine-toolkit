"""Per-operation decoration beneath a component (mirrors Go ``decorate.Scope``).

The operation ``count`` of the repository ``api.docs`` is the repository-tier component
``api.docs.count``, so the config can tune one operation, or every operation of a component
through the component's own key.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.foundation.decorate.decorations import Decorations
    from techai_webutils.foundation.decorate.tier import Deps, Subject, Tier


class Scope:
    """Resolves the decoration of one component's operations, each under its own name.

    Built with ``decorations``, each operation takes its own Deps from the config; built with
    ``deps`` alone, every operation shares them.
    """

    __slots__ = ("_decorations", "_deps", "_name", "_tier")

    def __init__(
        self,
        tier: Tier,
        name: str,
        deps: Deps,
        decorations: Decorations | None = None,
    ) -> None:
        """Bind the component ``name`` in ``tier``; ``decorations`` (optional) resolves each op."""
        self._tier = tier
        self._name = name
        self._deps = deps
        self._decorations = decorations

    @property
    def name(self) -> str:
        """The component's name."""
        return self._name

    def subject(self, op: str, *, without_retry: bool = False) -> Subject:
        """Return the Subject of the operation ``op``, named ``<component>.<op>``.

        Resolved from Decorations, a resolution error is recorded for ``verify`` and the
        operation runs with recovery only until boot fails on it. ``without_retry`` drops the
        retrier, for a write that must not replay.
        """
        name = f"{self._name}.{op}" if self._name else op
        deps = (
            self._decorations.resolve(self._tier, name)
            if self._decorations is not None
            else self._deps
        )
        if without_retry:
            deps = deps.without_retry()
        return self._tier.subject(name, deps)


class Operation[In, Out]:
    """One operation (``core.interfaces.op.Op``): ``fn`` run as its subject's ``Execute``."""

    __slots__ = ("_fn", "_subject")

    def __init__(self, subject: Subject, fn: Callable[[In], Awaitable[Out]]) -> None:
        """Bind the subject and the operation's function."""
        self._subject = subject
        self._fn = fn

    @property
    def name(self) -> str:
        """The operation's dotted name."""
        return self._subject.name

    async def execute(self, value: In, /) -> Out:
        """Run the operation on ``value`` through its subject.

        The thunk is a coroutine function, not a lambda: the retry layer awaits only a
        coroutine function's result.
        """

        async def run() -> Out:
            return await self._fn(value)

        return await self._subject.exec("Execute", run)
