"""``StaticTokenSource``: hands out one configured token, with no I/O."""

from __future__ import annotations

from typing import final, override

from techai_webutils.core.interfaces.token_source import TokenSource


@final
class StaticTokenSource(TokenSource):
    """Returns the token it was built with.

    (the default kind; empty when none is configured).
    """

    def __init__(self, token: str) -> None:
        """Hand out ``token`` on every call."""
        self._token = token

    @override
    async def token(self) -> str:
        """Return the configured token.

        Returns:
            The configured token.

        """
        return self._token
