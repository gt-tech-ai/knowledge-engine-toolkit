"""Tests for CursorCodec encode/decode round-trip."""

import pytest

from techai_webutils.repos.cursor import CursorCodec, CursorPayload


class TestCursorCodec:
    """Test suite for CursorCodec encode/decode operations."""

    def test_encode_decode_round_trip(self) -> None:
        """Test that encoding and decoding preserves all cursor payload fields.

        **Why this test is important:**
          - CursorCodec is the foundation of cursor-based pagination across all list endpoints
          - Data loss during round-trip would silently break pagination, causing duplicate or missing results
          - Both last_id and last_value must survive encoding to correctly resume queries

        **What it tests:**
          - last_id field survives the round-trip
          - last_value field survives the round-trip
        """
        payload = CursorPayload(last_id="abc-123", last_value="2024-01-01")
        cursor = CursorCodec.encode(payload)
        decoded = CursorCodec.decode(cursor)

        assert decoded.last_id == "abc-123"
        assert decoded.last_value == "2024-01-01"

    def test_encode_decode_empty_value(self) -> None:
        """Test that omitting last_value defaults to empty string after round-trip.

        **Why this test is important:**
          - Some pagination use cases only need last_id without a sort value
          - The codec must handle optional fields gracefully to avoid KeyError crashes at runtime
          - Default empty string convention must be consistent for downstream comparison logic

        **What it tests:**
          - last_id is preserved when last_value is not provided
          - last_value defaults to empty string
        """
        payload = CursorPayload(last_id="id-only")
        cursor = CursorCodec.encode(payload)
        decoded = CursorCodec.decode(cursor)

        assert decoded.last_id == "id-only"
        assert not decoded.last_value

    def test_decode_invalid_cursor(self) -> None:
        """Test that malformed base64 input is rejected with a clear error.

        **Why this test is important:**
          - Cursors come from untrusted client input and may be tampered with or corrupted
          - Failing to reject invalid cursors could cause crashes or undefined pagination behavior
          - Clear error messages help API consumers diagnose pagination issues

        **What it tests:**
          - Non-base64 string raises ValueError with 'invalid cursor' message
        """
        with pytest.raises(ValueError, match="invalid cursor"):
            CursorCodec.decode("not-valid-base64!!!")

    def test_decode_invalid_json(self) -> None:
        """Test that valid base64 containing non-JSON data is rejected.

        **Why this test is important:**
          - An attacker could send valid base64 that is not JSON to bypass simple validation
          - The codec must validate the full deserialization chain, not just base64 decoding
          - Consistent error handling prevents information leakage about internal cursor format

        **What it tests:**
          - Base64-encoded non-JSON payload raises ValueError with 'invalid cursor' message
        """
        import base64

        bad = base64.urlsafe_b64encode(b"not json").decode()
        with pytest.raises(ValueError, match="invalid cursor"):
            CursorCodec.decode(bad)

    def test_decode_missing_id_field(self) -> None:
        """Test that valid JSON missing the required 'id' field is rejected.

        **Why this test is important:**
          - The 'id' field is mandatory for cursor-based pagination to resume at the correct position
          - Accepting JSON without 'id' would produce a CursorPayload with None, causing downstream failures
          - Schema validation at the codec layer prevents invalid state from propagating

        **What it tests:**
          - JSON without 'id' key raises ValueError with 'invalid cursor' message
        """
        import base64
        import json

        bad = base64.urlsafe_b64encode(json.dumps({"v": "x"}).encode()).decode()
        with pytest.raises(ValueError, match="invalid cursor"):
            CursorCodec.decode(bad)

    def test_cursor_payload_frozen(self) -> None:
        """Test that CursorPayload is immutable to prevent accidental mutation.

        **Why this test is important:**
          - Cursor payloads are shared across pagination logic and must not be modified after creation
          - Mutable payloads could lead to race conditions in concurrent request handling
          - Frozen dataclasses enforce value-object semantics required by the pagination contract

        **What it tests:**
          - Assigning to last_id raises AttributeError
        """
        payload = CursorPayload(last_id="x")
        with pytest.raises(AttributeError):
            payload.last_id = "y"  # type: ignore[misc]
