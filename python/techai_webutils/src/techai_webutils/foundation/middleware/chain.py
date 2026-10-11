"""Middleware chain composition.

Mirrors Go's ``go/foundation/middleware/chain.go``:
``Chain(a, b, c)(handler) == a(b(c(handler)))``.
"""

from __future__ import annotations

from collections.abc import Callable

# A middleware is a function that wraps a handler and returns a new handler.
type Middleware[T] = Callable[[T], T]


def chain[T](*middlewares: Middleware[T]) -> Middleware[T]:
    """Compose middleware in order: first argument is outermost.

    ``chain(a, b, c)(h)`` produces ``a(b(c(h)))``.

    Args:
        *middlewares: Middleware functions to compose.

    Returns:
        A single middleware that applies all in order.

    """

    def composed(handler: T) -> T:
        """Wrap *handler* with every middleware, applying them inside-out."""
        result = handler
        for mw in reversed(middlewares):
            result = mw(result)
        return result

    return composed
