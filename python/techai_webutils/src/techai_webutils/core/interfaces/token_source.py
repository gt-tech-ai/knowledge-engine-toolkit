"""``TokenSource``: a bearer token for calling an API as a configured principal.

A source hands out a token that is valid for at least its refresh margin, minting a new
one when the cached token is about to expire, so a caller can attach ``token()`` to every
request without tracking expiry itself.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


class TokenSource(ABC):
    """Supplies a current bearer token."""

    @abstractmethod
    async def token(self) -> str:
        """Return a bearer token that is not about to expire, minting one when needed."""
        ...
