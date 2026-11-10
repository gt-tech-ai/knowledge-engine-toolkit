"""Structured error context, middleware and predicate-based exception observers."""

from techai_webutils.foundation.errors.absorb import (
    Absorb,
    AsyncOnError,
    OnError,
    OnFailure,
)

__all__ = ["Absorb", "AsyncOnError", "OnError", "OnFailure"]
