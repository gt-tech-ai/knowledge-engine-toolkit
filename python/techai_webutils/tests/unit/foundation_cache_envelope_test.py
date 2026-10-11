"""Tests for the cache envelope encode/decode functions."""

from __future__ import annotations

import json
import time

import pytest

from techai_webutils.clients.cache.envelope import CacheEnvelope, decode, encode


class TestEncode:
    """Tests for the encode function."""

    def test_produces_valid_json(self) -> None:
        """Test that encode wraps the value in a JSON envelope carrying data, version, and cached_at.

        **Why this test is important:**
          - The envelope is the on-the-wire cache format; readers (and any
            external tooling) depend on it being parseable JSON with exactly
            these three keys
          - If encode dropped the version or stored the payload under the wrong
            key, every cached entry would become undecodable or silently wrong

        **What it tests:**
          - The output parses as JSON
          - data holds the original value verbatim and version is preserved
          - A cached_at key is present
        """
        result = encode({"id": "1", "name": "alice"}, version=1)
        parsed = json.loads(result)
        assert parsed["data"] == {"id": "1", "name": "alice"}
        assert parsed["version"] == 1
        assert "cached_at" in parsed

    def test_sets_cached_at_timestamp(self) -> None:
        """Test that encode stamps cached_at with the current wall-clock time.

        **Why this test is important:**
          - cached_at is the basis for any age- or TTL-based reasoning about a
            cached entry; a stale or fabricated timestamp would make staleness
            checks meaningless
          - Confirms the stamp is taken at encode time, not hardcoded or zero

        **What it tests:**
          - cached_at falls within the [before, after] window bracketing the
            encode call
        """
        before = time.time()
        result = encode("test", version=1)
        after = time.time()
        parsed = json.loads(result)
        assert before <= parsed["cached_at"] <= after

    def test_returns_bytes(self) -> None:
        """Test that encode returns bytes rather than a str.

        **Why this test is important:**
          - Redis (and other byte-oriented cache backends) store and return
            bytes; returning a str would force every caller to encode again or
            fail at the client boundary
          - Locks the contract that encode output is directly storable

        **What it tests:**
          - The return value is a bytes instance
        """
        result = encode("hello", version=1)
        assert isinstance(result, bytes)


class TestDecode:
    """Tests for the decode function."""

    def test_round_trip_dict(self) -> None:
        """Test that a dict survives an encode/decode round trip with a matching version.

        **Why this test is important:**
          - Dict payloads (entity records, API responses) are the common cache
            case; a lossy round trip would corrupt cached data invisibly
          - Establishes the core invariant that decode(encode(x)) == x when
            versions agree

        **What it tests:**
          - decode returns ok=True
          - The decoded value equals the original dict
        """
        original = {"id": "1", "name": "alice"}
        encoded = encode(original, version=1)
        value, ok = decode(encoded, version=1)
        assert ok is True
        assert value == original

    def test_round_trip_primitive(self) -> None:
        """Test that a scalar primitive survives an encode/decode round trip.

        **Why this test is important:**
          - The cache must handle bare scalars (counts, flags, ids), not only
            container types; an int must come back as an int, not a string or
            float
          - Guards the round-trip invariant for the simplest payload shape

        **What it tests:**
          - decode returns ok=True
          - The decoded value equals the original integer 42
        """
        encoded = encode(42, version=1)
        value, ok = decode(encoded, version=1)
        assert ok is True
        assert value == 42

    def test_round_trip_list(self) -> None:
        """Test that a list survives an encode/decode round trip with element order intact.

        **Why this test is important:**
          - Cached collections (search hits, ordered results) must preserve both
            contents and order; a reordered or coerced list would silently break
            callers that rely on sequence
          - Confirms JSON array round-tripping does not mutate the payload

        **What it tests:**
          - decode returns ok=True
          - The decoded value equals the original list [1, 2, 3]
        """
        original = [1, 2, 3]
        encoded = encode(original, version=1)
        value, ok = decode(encoded, version=1)
        assert ok is True
        assert value == original

    def test_version_mismatch_returns_false(self) -> None:
        """Test that decoding an envelope under a different version is treated as a miss.

        **Why this test is important:**
          - Version-aware caching is the whole point of the envelope: after a
            schema change, stale entries written under the old version must be
            rejected rather than deserialized into the new shape
          - A failure here would let incompatible cached data leak into callers
            expecting the new schema

        **What it tests:**
          - decode of a version=1 envelope against expected version=2 returns
            ok=False
          - The returned value is None (no payload handed back on mismatch)
        """
        encoded = encode("data", version=1)
        value, ok = decode(encoded, version=2)
        assert ok is False
        assert value is None

    def test_invalid_json_raises(self) -> None:
        """Test that decoding corrupt (non-JSON) bytes raises rather than returning a miss.

        **Why this test is important:**
          - Corruption must be distinguishable from a normal version-mismatch
            miss: a miss is recoverable (recompute), corruption signals a real
            fault that should surface, not be silently swallowed
          - Prevents the cache from masking storage corruption as a benign miss

        **What it tests:**
          - decode of non-JSON bytes raises JSONDecodeError/ValueError instead
            of returning (None, False)
        """
        with pytest.raises((json.JSONDecodeError, ValueError)):
            decode(b"not json", version=1)

    def test_empty_bytes_raises(self) -> None:
        """Test that decoding empty bytes raises rather than returning a miss.

        **Why this test is important:**
          - An empty payload is a degenerate corruption case (truncated write,
            empty Redis value) that must not be mistaken for a clean miss or
            decoded into a null entry
          - Locks the same fail-loud contract as invalid JSON for the empty edge

        **What it tests:**
          - decode of b"" raises JSONDecodeError/ValueError
        """
        with pytest.raises((json.JSONDecodeError, ValueError)):
            decode(b"", version=1)


class TestCacheEnvelope:
    """Tests for the CacheEnvelope dataclass."""

    def test_frozen(self) -> None:
        """Test that a CacheEnvelope instance is immutable after construction.

        **Why this test is important:**
          - The envelope is shared metadata about a cached value; allowing
            mutation would let one consumer corrupt another's view of cached_at
            or version, reintroducing the exact staleness bugs the envelope
            exists to prevent
          - Confirms the frozen dataclass contract is actually enforced

        **What it tests:**
          - Assigning to a field after construction raises AttributeError
        """
        env = CacheEnvelope(data="test", cached_at=time.time(), version=1)
        with pytest.raises(AttributeError):
            env.data = "changed"  # type: ignore[misc]

    def test_fields(self) -> None:
        """Test that CacheEnvelope stores its three constructor arguments faithfully.

        **Why this test is important:**
          - The dataclass uses slots, so a typo'd or reordered field would be a
            silent data loss / attribute error; this pins the data/cached_at/
            version positional contract the encode/decode functions rely on
          - Guards against accidental field renames breaking the cache format

        **What it tests:**
          - data, cached_at, and version round-trip to the values passed to the
            constructor
        """
        now = time.time()
        env = CacheEnvelope(data={"key": "value"}, cached_at=now, version=3)
        assert env.data == {"key": "value"}
        assert env.cached_at == now
        assert env.version == 3
